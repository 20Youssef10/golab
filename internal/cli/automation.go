package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/contents"
	"github.com/20Youssef10/golab/internal/history"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/platform"
	"github.com/20Youssef10/golab/internal/runtime"
)

// INTERACTIVE_AUTOMATION_TIMEOUT_SEC upstream commands/automation.py: the
// kernel is silent while the user completes a browser OAuth flow; 10 minutes
// is long enough for any realistic ceremony without an unbounded CI hang.
const interactiveAutomationTimeout = 600 * time.Second

const authCode = "import os\nos.environ['USE_AUTH_EPHEM'] = '0'\n" +
	"from google.colab import auth\nauth.authenticate_user()"

// automationSpec is the plain-value description of one kernel automation.
type automationSpec struct {
	name       string
	op         string
	code       string
	path       string
	allowStdin bool
	timeout    time.Duration
	// hook handles a colab_request. It receives the kernel so it can answer via
	// SendColabReply; returning true marks the request intercepted.
	hook func(runtime.Request, runtime.Kernel) bool
}

// buildInstallCode encodes packages as a JSON literal decoded remotely. User
// input is NEVER spliced into generated Python source (fixes upstream D-3).
func buildInstallCode(packages []string) string {
	payload, err := json.Marshal(packages)
	if err != nil {
		payload = []byte("[]")
	}
	var b strings.Builder
	b.WriteString("import subprocess, sys, json\n")
	fmt.Fprintf(&b, "packages = json.loads(%s)\n", pyRepr(string(payload)))
	b.WriteString("def install():\n")
	b.WriteString("    try:\n")
	b.WriteString("        subprocess.check_call(['uv', 'pip', 'install', '--system'] + packages)\n")
	b.WriteString("        print('Installation Complete (via uv)!')\n")
	b.WriteString("    except:\n")
	b.WriteString("        subprocess.check_call([sys.executable, '-m', 'pip', 'install'] + packages)\n")
	b.WriteString("        print('Installation Complete (via pip)!')\n")
	b.WriteString("install()\n")
	return b.String()
}

// redactPromptReply returns the history-safe representation of a value typed
// at an interactive prompt. The real value is still returned to the runtime so
// the kernel receives it unchanged; only the persisted record is redacted.
// The byte length keeps logs useful without retaining any of the content.
func redactPromptReply(value string) string {
	return fmt.Sprintf("[redacted:%d bytes]", len(value))
}

// runAutomation resolves the session, records history state, executes the
// snippet on the kernel and streams outputs. Stdin and streaming can be used
// together: the runtime's stdin loop is independent of its WS reader.
func runAutomation(ctx context.Context, d *app.Deps, spec automationSpec) error {
	s, err := d.ResolveSession(ctx, spec.name)
	if err != nil {
		return sessionErr(spec.name, err)
	}
	kernel, err := d.RuntimeFor(ctx, s)
	if err != nil {
		return pruneLost(d, s.Name, err)
	}
	defer func() {
		setRunning(d, s, nil)
		_ = kernel.Close()
	}()
	setRunning(d, s, running("automation("+spec.op+")"))
	setLastExecution(d, s, "automation:"+spec.op, nil)

	event := map[string]any{"op": spec.op, "code": spec.code}
	if spec.op == "drivemount" {
		event["path"] = spec.path
	}
	writeEvent(d, s.Name, history.EventAutomation, event)

	reader := bufio.NewReader(d.Stdin)
	var renderErr error
	opts := runtime.ExecOptions{
		IdleTimeout: spec.timeout,
		AllowStdin:  spec.allowStdin,
		OnOutput: func(o runtime.Output) {
			if e := renderOne(d, o, ""); e != nil && renderErr == nil {
				renderErr = e
			}
		},
	}
	if spec.allowStdin {
		opts.StdinHook = func(prompt string) (string, error) {
			writeEvent(d, s.Name, history.EventStdinRequest, map[string]any{"prompt": prompt})
			fmt.Fprint(d.Stderr, prompt)
			value, readErr := reader.ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return "", readErr
			}
			value = strings.TrimRight(value, "\r\n")
			// Persist only a redacted form: a password typed at a getpass/auth
			// prompt must never reach plaintext history (or any export of it).
			writeEvent(d, s.Name, history.EventInputReply,
				map[string]any{"value": redactPromptReply(value)})
			return value, nil
		}
	}
	if spec.hook != nil {
		opts.ColabRequestHook = func(req runtime.Request) bool { return spec.hook(req, kernel) }
	}

	res, err := kernel.Execute(ctx, spec.code, opts)
	if err != nil {
		return pruneLost(d, s.Name, err)
	}
	if renderErr != nil {
		return renderErr
	}
	writeEvent(d, s.Name, history.EventAutomationResult, map[string]any{
		"op": spec.op, "outputs": runtimeOutputsAsMaps(res.Outputs),
	})
	return nil
}

// --- install ----------------------------------------------------------------

func installCommand() *cobra.Command {
	var name, requirement string
	cmd := &cobra.Command{
		Use:   "install [PKGS...]",
		Short: "Install python packages on the VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			if len(args) == 0 && requirement == "" {
				fmt.Fprintln(d.Stderr, "[colab] No packages or requirements specified.")
				return exitf(1, "")
			}
			// Resolve early: the Contents client for -r needs the session.
			s, err := d.ResolveSession(cmd.Context(), name)
			if err != nil {
				return sessionErr(name, err)
			}
			pkgs := make([]string, 0, len(args)+2)
			if requirement != "" {
				if info, err := os.Stat(requirement); err != nil || info.IsDir() {
					return exitf(1, "[colab] Requirements file '%s' not found locally.", requirement)
				}
				remote := "content/" + filepath.Base(requirement)
				client, err := d.ContentsFor(s)
				if err != nil {
					return err
				}
				f, err := os.Open(requirement)
				if err != nil {
					return err
				}
				err = client.WriteStream(cmd.Context(), remote, f, contents.DefaultChunkSize)
				_ = f.Close()
				if err != nil {
					return err
				}
				pkgs = append(pkgs, "-r", "/"+remote)
			}
			pkgs = append(pkgs, args...)
			fmt.Fprintf(d.Stderr, "[colab] Installing packages on %s (preferring uv)...\n", s.Name)
			return runAutomation(cmd.Context(), d, automationSpec{
				name: s.Name, op: "install", code: buildInstallCode(pkgs),
			})
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().StringVarP(&requirement, "requirement", "r", "", "Local requirements file to install")
	return cmd
}

// --- auth (hidden) ----------------------------------------------------------

func authCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:    "auth",
		Short:  "Authenticate with Google on the VM",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			s, err := d.ResolveSession(cmd.Context(), name)
			if err != nil {
				return sessionErr(name, err)
			}
			fmt.Fprintf(d.Stderr, "[colab] Starting Google Auth flow on %s...\n", s.Name)
			return runAutomation(cmd.Context(), d, automationSpec{
				name: s.Name, op: "auth", code: authCode,
				allowStdin: true, timeout: interactiveAutomationTimeout,
			})
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	return cmd
}

// --- drivemount -------------------------------------------------------------

func ttyName() string {
	if goruntime.GOOS == "windows" {
		return "CONIN$"
	}
	return "/dev/tty"
}

// waitForEnter reads one line from the controlling terminal (stdin fallback),
// bounded by the automation context so a headless host cannot hang forever.
func waitForEnter(ctx context.Context, d *app.Deps) {
	fmt.Fprint(d.Stderr, "Press Enter after you have granted access... ")
	defer fmt.Fprintln(d.Stderr)

	// Every blocking read is covered by a source we can close on cancellation,
	// so no reader goroutine can outlive the caller's context. On normal
	// completion nothing is closed: stdin stays owned by the caller.
	var ttyFile *os.File
	if platform.IsStdinTTY() {
		// Only touch the controlling terminal when stdin really is a TTY.
		// On Windows CONIN$ can open successfully in a headless runner and
		// then block forever, so the injected stdin must be used instead.
		if f, err := os.Open(ttyName()); err == nil {
			ttyFile = f
		}
	}
	var once sync.Once
	closeSources := func() {
		once.Do(func() {
			if ttyFile != nil {
				_ = ttyFile.Close()
			}
			if c, ok := d.Stdin.(io.Closer); ok {
				_ = c.Close()
			}
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if ttyFile != nil {
			if _, err := bufio.NewReader(ttyFile).ReadString('\n'); err == nil || ctx.Err() != nil {
				return
			}
			// e.g. EIO when there is no controlling terminal: fall back to stdin.
		}
		_, _ = bufio.NewReader(d.Stdin).ReadString('\n')
	}()
	select {
	case <-done:
	case <-ctx.Done():
		closeSources() // unblocks ReadString so the goroutine can exit
	}
}

// Consent in a browser is inherently interactive, so a fully click-free
// drivemount is impossible. The most we can automate is the *wait*: poll the
// control plane until it observes that propagation succeeded, instead of
// asking the user to press Enter. Bound the wait so a headless host cannot
// hang forever (matches interactiveAutomationTimeout's rationale).
const (
	consentPollInterval = 2 * time.Second
	consentPollTimeout  = 5 * time.Minute
)

// pollForConsent calls probe until it reports Success or the derived deadline
// expires. It intentionally has no stdin or terminal dependency: the only
// signal it waits for is the server reflecting a completed browser consent.
// Each probe receives the bounded context, so a stalled HTTP call cannot
// outlive the overall timeout.
func pollForConsent(ctx context.Context, timeout, interval time.Duration, probe func(context.Context) (*colabapi.CredProp, error)) (*colabapi.CredProp, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return nil, consentTimeoutError(err, lastErr)
		}
		cp, err := probe(ctx)
		if err == nil && cp != nil && cp.Success {
			return cp, nil
		}
		// A transient probe failure (5xx, stalled request, ...) is not a
		// consent decision: remember it and keep polling until the deadline.
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, consentTimeoutError(ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

// consentTimeoutError reports a bounded timeout without echoing server text.
// lastErr is deliberately NOT wrapped with %w: a StatusError body must never
// re-enter the terminal through errors.As in reportDriveFailure. Only its
// HTTP status is surfaced, which is bounded and carries no HTML or token.
func consentTimeoutError(ctxErr, lastErr error) error {
	if lastErr == nil {
		return ctxErr
	}
	var se *colabapi.StatusError
	if errors.As(lastErr, &se) && se.Code != 0 {
		return fmt.Errorf("%w: last propagation probe returned HTTP %d", ctxErr, se.Code)
	}
	return fmt.Errorf("%w: last propagation probe failed", ctxErr)
}

// cleanDetail strips ANSI/CSI/OSC escapes and control characters from an
// untrusted server string, collapses whitespace and caps the result. Only the
// returned text may reach the terminal or the history log.
func cleanDetail(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b: // ESC: CSI, OSC or a two-byte escape.
			i++
			if i < len(s) && s[i] == '[' { // CSI: skip params, stop at 0x40..0x7e.
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
				continue
			}
			for i < len(s) && s[i] != 0x07 { // OSC: BEL or ESC \ terminates.
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
			if i < len(s) && s[i] == 0x07 {
				i++
			}
		case c < 0x20 || c == 0x7f:
			// Keep word boundaries for whitespace controls; drop the rest.
			if c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' {
				b.WriteByte(' ')
			}
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	d := strings.Join(strings.Fields(b.String()), " ")
	runes := []rune(d)
	if len(runes) > 300 {
		d = string(runes[:300])
	}
	return d
}

// propagationDetail mirrors upstream's _parse_propagation_response followed by
// its whitelist of error/message fields. It accepts the "<prefix>\n{json}"
// bodies Colab returns, never raises, and never returns raw HTML or unknown
// fields (which may carry tokens).
func propagationDetail(body string) string {
	const maxBody = 64 << 10
	if len(body) > maxBody {
		body = body[:maxBody]
	}
	candidates := []string{body}
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		candidates = append(candidates, body[i+1:])
	}
	if i := strings.LastIndexByte(body, '\n'); i >= 0 {
		candidates = append(candidates, body[i+1:])
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || candidate[0] != '{' {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(candidate), &obj); err != nil || obj == nil {
			continue
		}
		for _, key := range []string{"error", "message"} {
			if s, ok := obj[key].(string); ok {
				if d := cleanDetail(s); d != "" {
					return d
				}
			}
		}
		return ""
	}
	return ""
}

// driveFailureMessage is the actionable message from upstream's
// drive_propagation_failure_message. The raw response body (often HTML) is
// never echoed; only a sanitised detail string is included.
func driveFailureMessage(status int, detail string) string {
	headline := "[colab] Drive credentials could not be propagated"
	if status != 0 && status != 200 {
		headline += fmt.Sprintf(" (HTTP %d)", status)
	}
	lines := []string{headline + "."}
	if d := cleanDetail(detail); d != "" {
		lines = append(lines, "[colab] Server said: "+d)
	}
	return strings.Join(append(lines,
		"[colab] If the browser showed an account chooser, re-run the command",
		"[colab] and select the account reported by `golab whoami`. Approving",
		"[colab] Drive access as a different Google account makes propagation fail",
		"[colab] even though the consent itself succeeded."), "\n")
}

// safeAuthURI sanitises a server-supplied authorization URL before it can
// reach the terminal, the browser launcher or the history log. Terminal
// escapes are stripped first (cleanDetail), then only absolute http(s) URLs
// are accepted. An empty return means "never display or launch this".
func safeAuthURI(raw string) string {
	s := cleanDetail(raw)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return s
}

// showDriveAuthURL prints the sanitized authorization URL and returns the
// exact same value so the caller records it in history. No untrusted text is
// returned to either sink, and an invalid link is never handed to openURL.
func showDriveAuthURL(d *app.Deps, uri string) string {
	safe := safeAuthURI(uri)
	if safe == "" {
		fmt.Fprintln(d.Stderr, "[colab] Server returned an invalid authorization URL; not displaying it.")
		return ""
	}
	fmt.Fprintf(d.Stderr, "\n[colab] REQUIRED: Google Drive Authorization needed.\nPlease visit:\n\n%s\n", safe)
	if !openURL(safe) {
		return safe
	}
	fmt.Fprintln(d.Stderr, "[colab] Opened the authorization page in your default browser.")
	return safe
}

func reportDriveFailure(d *app.Deps, name string, err error) {
	status, detail := 0, ""
	var se *colabapi.StatusError
	if errors.As(err, &se) {
		status = se.Code
		// Never echo StatusError.Body: it is untrusted and often raw HTML.
		detail = propagationDetail(se.Body)
		if detail == "" {
			// Fall back to the status code's reason, not the body.
			detail = logx.Redact(http.StatusText(se.Code))
		}
	} else if err != nil {
		detail = cleanDetail(logx.Redact(err.Error()))
	}
	fmt.Fprintln(d.Stderr, driveFailureMessage(status, detail))
	writeEvent(d, name, "drive_auth_failed", map[string]any{
		"status": status, "body": detail,
	})
}

func driveCommand() *cobra.Command {
	var name string
	var waitForConsent bool
	cmd := &cobra.Command{
		Use:   "drivemount [PATH]",
		Short: "Mount Google Drive at PATH (default /content/drive)",
		Args:  maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			path := "/content/drive"
			if len(args) > 0 {
				path = args[0]
			}
			s, err := d.ResolveSession(cmd.Context(), name)
			if err != nil {
				return sessionErr(name, err)
			}
			fmt.Fprintf(d.Stderr, "[colab] Mounting Google Drive to '%s' on %s...\n", path, s.Name)
			// pyRepr keeps a hostile mount path from becoming Python source.
			code := "from google.colab import drive\ndrive.mount(" + pyRepr(path) + ")"

			hook := func(req runtime.Request, kernel runtime.Kernel) bool {
				if req.AuthType != "dfs_ephemeral" {
					return false
				}
				writeEvent(d, s.Name, history.EventColabRequest, map[string]any{
					"type": "dfs_ephemeral", "colab_msg_id": req.ColabMsgID,
				})
				fmt.Fprintln(d.Stderr, "\n[colab] Intercepted Drive Auth Request. Connecting...")
				// The runtime already calls hooks off its WS reader; a second
				// goroutine keeps the reply send fully decoupled from Execute.
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), interactiveAutomationTimeout)
					defer cancel()
					defer func() {
						if e := kernel.SendColabReply(ctx, req, nil); e != nil {
							d.Log.Debug("drive mount: colab_reply failed", "error", e)
						}
					}()
					client, err := d.ColabClient(ctx)
					if err != nil {
						reportDriveFailure(d, s.Name, err)
						return
					}
					cp, err := client.CredentialsPropagation(ctx, s.Endpoint)
					if err != nil {
						reportDriveFailure(d, s.Name, err)
						return
					}
					if !cp.Success {
						if cp.UnauthorizedRedirectURI == "" {
							reportDriveFailure(d, s.Name, nil)
							return
						}
						safeURI := showDriveAuthURL(d, cp.UnauthorizedRedirectURI)
						writeEvent(d, s.Name, history.EventDriveAuthNeeded, map[string]any{"uri": safeURI})
						// The browser consent itself stays human-only. --wait-for-consent
						// replaces the Enter prompt with a bounded poll of the control
						// plane; --yes skips the prompt too (and goes straight to Confirm).
						switch {
						case waitForConsent:
							cp, err = pollForConsent(ctx, consentPollTimeout, consentPollInterval,
								func(c context.Context) (*colabapi.CredProp, error) {
									return client.CredentialsPropagation(c, s.Endpoint)
								})
							if err != nil {
								reportDriveFailure(d, s.Name, err)
								return
							}
						case assumeYes(cmd):
							// Non-interactive confirmation already granted.
						default:
							waitForEnter(ctx, d)
						}
					}
					final, err := cp.Confirm(ctx)
					if err != nil {
						reportDriveFailure(d, s.Name, err)
						return
					}
					if final.Success {
						fmt.Fprintln(d.Stderr, "[colab] Credentials propagated. Resuming mount...")
						writeEvent(d, s.Name, history.EventDriveAuthSuccess, map[string]any{})
					} else {
						reportDriveFailure(d, s.Name, nil)
					}
				}()
				return true
			}

			return runAutomation(cmd.Context(), d, automationSpec{
				name: s.Name, op: "drivemount", path: path, code: code,
				allowStdin: true, timeout: interactiveAutomationTimeout, hook: hook,
			})
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().BoolVar(&waitForConsent, "wait-for-consent", false,
		"Poll for Drive consent instead of waiting for Enter (browser consent is still human-only)")
	return cmd
}

func registerAutomationCommands(root *cobra.Command) {
	root.AddCommand(authCommand())
	root.AddCommand(driveCommand())
	root.AddCommand(installCommand())
}
