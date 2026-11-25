package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/sshx"
	"github.com/20Youssef10/golab/internal/state"
)

// staleSSHDKillCode is upstream cda02bc's --force payload, byte-for-byte. It
// is a constant: no user input is ever spliced into code executed on the VM.
const staleSSHDKillCode = "import os; os.system('pkill -P $(pgrep -o -x sshd)')"

// Upstream execute_code(timeout=10.0) bounds the kill; the kernel is always
// closed afterwards (rt.stop() in the finally block).
const staleSSHDKillTimeout = 10 * time.Second

// killStaleSSHdFn and attachSSHForceKernel are package variables so CLI-level
// tests can inject a recorder/spy (upstream patches ssh_module._killStaleSshd).
var killStaleSSHdFn = killStaleSSHd

var attachSSHForceKernel = func(ctx context.Context, d *app.Deps, s *state.Session) (runtime.Kernel, error) {
	return d.RuntimeFor(ctx, s)
}

// killStaleSSHd kills stale per-connection sshd children on the runtime before
// connecting. Failures are tolerated (the connection may still succeed) and
// reported on stderr; the piped stdout byte stream is never touched.
func killStaleSSHd(ctx context.Context, d *app.Deps, s state.Session) {
	fmt.Fprintln(d.Stderr, "[colab] --force: killing stale SSH sessions...")
	kctx, cancel := context.WithTimeout(ctx, staleSSHDKillTimeout)
	defer cancel()
	kernel, err := attachSSHForceKernel(kctx, d, &s)
	if err != nil {
		fmt.Fprintf(d.Stderr, "[colab] --force: failed to kill sshd: %s\n", logx.Redact(err.Error()))
		return
	}
	defer func() { _ = kernel.Close() }()
	if _, err := kernel.Execute(kctx, staleSSHDKillCode, runtime.ExecOptions{
		IdleTimeout: staleSSHDKillTimeout,
	}); err != nil {
		fmt.Fprintf(d.Stderr, "[colab] --force: failed to kill sshd: %s\n", logx.Redact(err.Error()))
	}
}

// forceKillApplies mirrors upstream's branches (ssh.py:842-866): every SSH path
// honours --force except the hidden `proxy_mode + cmd-mode` bridge re-invocation,
// which never forwards the flag.
func forceKillApplies(force, proxyMode bool, mode sshx.Mode) bool {
	return force && (!proxyMode || mode != sshx.ModeCmd)
}

// signalsDisabledForTest reports whether the COLAB_TEST_NO_SIGNALS escape
// hatch is active. It exists only for Go tests that must skip assertions
// involving real process signals in sandboxes where delivery is unreliable.
// The variable is inert in release builds: a binary has no "test.v" flag, so
// the environment is never consulted and cannot change signal-handling
// behavior (the same gate internal/auth uses for its loopback exception).
func signalsDisabledForTest() bool {
	if flag.CommandLine.Lookup("test.v") == nil {
		return false
	}
	return os.Getenv("COLAB_TEST_NO_SIGNALS") == "1"
}

// sshTarget is the resolved session plus whether this command created it.
// `created` gates --rm (interactive --rm only removes a runtime we allocated).
type sshTarget struct {
	session state.Session
	created bool
}

func selectSSHMode(proxy bool, internal string, cmdArgs []string) sshx.Mode {
	switch {
	case len(cmdArgs) > 0:
		return sshx.ModeCmd // `ssh -- CMD…`
	case internal != "":
		return sshx.NormalizeMode(sshx.Mode(internal)) // hidden re-invocation
	case proxy:
		return sshx.ModeProxy
	default:
		return sshx.ModeInteractive
	}
}

func warnSSHIgnoredFlags(d *app.Deps, gpu, tpu string, highMem bool) {
	if gpu != "" || tpu != "" || highMem {
		fmt.Fprintln(d.Stderr, "[colab] --gpu/--tpu/--high-mem ignored: only applies to a created runtime.")
	}
}

// autoCreateSSHSession validates the accelerator BEFORE allocating anything,
// then reuses the `new` allocation path so the two commands cannot drift.
func autoCreateSSHSession(ctx context.Context, d *app.Deps, name, gpu, tpu string, highMem bool) (sshTarget, error) {
	opts, err := resolveRuntimeOptions(gpu, tpu, highMem)
	if err != nil {
		return sshTarget{}, err
	}
	if name == "" {
		name = randomName("")
	}
	if highMem && highMemOnly(opts.accelerator) {
		fmt.Fprintln(d.Stderr, "[colab] --high-mem ignored: this accelerator only offers one machine shape.")
	}
	fmt.Fprintf(d.Stderr, "[colab] Creating runtime '%s'...\n", name)
	s, err := createSession(ctx, d, name, opts)
	if err != nil {
		return sshTarget{}, mapAssignFailure(err, opts)
	}
	fmt.Fprintln(d.Stderr, "[colab] Session READY.")
	return sshTarget{session: s, created: true}, nil
}

// resolveSSHTarget implements upstream's auto-create rules. A `ssh -- CMD`
// (or the hidden cmd-mode proxy re-invocation) MUST use an existing session.
func resolveSSHTarget(ctx context.Context, d *app.Deps, cmd *cobra.Command, name, gpu, tpu string, highMem bool, mode sshx.Mode, cmdArgs []string) (sshTarget, error) {
	cmdMode := len(cmdArgs) > 0 || mode == sshx.ModeCmd
	if cmdMode {
		s, err := d.ResolveSession(ctx, name)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return sshTarget{}, exitf(2, "[colab] session '%s' not found. Run `colab sessions` to list active sessions.", name)
			}
			return sshTarget{}, sessionErr(name, err)
		}
		warnSSHIgnoredFlags(d, gpu, tpu, highMem)
		return sshTarget{session: *s}, nil
	}

	// --proxy-mode with an explicit but unknown name creates it on first connect.
	if mode == sshx.ModeProxy && name != "" {
		if _, err := d.Store.Get(name); errors.Is(err, state.ErrNotFound) {
			// Only allocation paths consult the budget guard; attaching to an
			// existing session is not billable.
			if err := guardBudget(ctx, d, cmd); err != nil {
				return sshTarget{}, err
			}
			return autoCreateSSHSession(ctx, d, name, gpu, tpu, highMem)
		}
	}
	// Bare interactive ssh auto-creates only when the local store is empty.
	if mode == sshx.ModeInteractive && name == "" {
		local, err := d.Store.List()
		if err == nil && len(local) == 0 {
			if err := guardBudget(ctx, d, cmd); err != nil {
				return sshTarget{}, err
			}
			return autoCreateSSHSession(ctx, d, "", gpu, tpu, highMem)
		}
	}

	s, err := d.ResolveSession(ctx, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return sshTarget{}, exitf(2, "[colab] session '%s' not found. Run `colab sessions` to list active sessions.", name)
		}
		return sshTarget{}, sessionErr(name, err)
	}
	warnSSHIgnoredFlags(d, gpu, tpu, highMem)
	return sshTarget{session: *s}, nil
}

// sshTeardown is the --rm callback: unassign on the control plane and remove
// local state. Diagnostics go to stderr because proxy-mode stdout is the raw
// SSH byte stream. sshx wraps this in a sync.Once so it runs at most once.
func sshTeardown(ctx context.Context, d *app.Deps, s state.Session) error {
	fmt.Fprintf(d.Stderr, "[colab] Stopping session '%s'...\n", s.Name)
	var firstErr error
	if client, err := d.ColabClient(ctx); err == nil {
		firstErr = client.Unassign(ctx, s.Endpoint)
	} else {
		firstErr = err
	}
	// Local state is always removed, even when the backend is already gone.
	_ = d.Store.Delete(s.Name)
	writeEvent(d, s.Name, "session_terminated", map[string]any{"reason": "ssh_rm"})
	fmt.Fprintln(d.Stderr, "[colab] Session terminated.")
	return firstErr
}

// mapSSHRunError maps the typed errors returned by sshx.Runner to the CLI's
// exit-code contract. Handshake explanations are produced by
// sshx.ExplainHandshakeFailure (never by matching status strings here).
func mapSSHRunError(err error) error {
	if err == nil {
		return nil
	}
	var he *sshx.HandshakeError
	if errors.As(err, &he) {
		return exitf(1, "[colab] %s", logx.Redact(he.Message))
	}
	var ke *sshx.KeyError
	if errors.As(err, &ke) {
		return exitf(2, "%s", logx.Redact(ke.Error()))
	}
	if errors.Is(err, sshx.ErrNoSSHKey) || errors.Is(err, sshx.ErrUnsupportedKeyType) {
		return exitf(2, "[colab] %s", logx.Redact(err.Error()))
	}
	return exitf(1, "[colab] %s", logx.Redact(err.Error()))
}

func sshCommand() *cobra.Command {
	var name, identity, gpu, tpu, internalMode string
	var proxyMode, highMem, rm, force bool
	cmd := &cobra.Command{
		Use:   "ssh [-- CMD...]",
		Short: "Connect to a Colab runtime via SSH",
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			if proxyMode && len(args) > 0 {
				return usagef("[colab] --proxy-mode takes no command.")
			}
			mode := selectSSHMode(proxyMode, internalMode, args)
			if explainRequested(cmd) {
				creates := name == "" && len(args) == 0 && mode != sshx.ModeCmd
				fmt.Fprintln(d.Stdout, ExplainPlan(cmd, PlanFields{
					Operation: "ssh", Session: name, Cleanup: "none",
					CreatesSession: creates, Billable: creates,
				}))
				return nil
			}
			target, err := resolveSSHTarget(cmd.Context(), d, cmd, name, gpu, tpu, highMem, mode, args)
			if err != nil {
				return err
			}
			// Upstream kills stale sshd children via the kernel BEFORE dialling
			// the WebSocket. Diagnostics go to stderr, never to the piped stream.
			if forceKillApplies(force, proxyMode, mode) {
				killStaleSSHdFn(cmd.Context(), d, target.session)
			}

			// --rm is honoured for proxy bridges and auto-created runtimes, but
			// never for `ssh -- CMD` or a reused interactive session.
			rmApplies := rm && len(args) == 0 && (target.created || mode != sshx.ModeInteractive)
			if rm {
				switch {
				case len(args) > 0:
					fmt.Fprintln(d.Stderr, "[colab] --rm ignored with -- CMD.")
				case !target.created && mode == sshx.ModeInteractive:
					fmt.Fprintln(d.Stderr, "[colab] --rm ignored: only a runtime auto-created by `colab ssh` is removed on exit.")
				}
			}
			var teardown func(context.Context) error
			if rmApplies {
				s := target.session
				teardown = func(ctx context.Context) error { return sshTeardown(ctx, d, s) }
			}

			runner, err := sshx.New(sshx.Config{
				ProxyURL: target.session.URL,
				Token:    target.session.Token,
				Mode:     mode,
				Identity: identity,
				Command:  args,
				Stdin:    d.Stdin,
				Stdout:   d.Stdout,
				Stderr:   d.Stderr,
				Teardown: teardown,
				Logger:   d.Log,
			})
			if err != nil {
				return err
			}
			rc, err := runner.Run(cmd.Context())
			if err != nil {
				return mapSSHRunError(err)
			}
			if rc != 0 {
				return exitf(rc, "")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().BoolVar(&proxyMode, "proxy-mode", false,
		"Act as an OpenSSH ProxyCommand-compatible WebSocket-stdio bridge")
	cmd.Flags().StringVarP(&identity, "identity", "i", "",
		"SSH key (public .pub or private key path) for the X-Colab-Ssh-Pubkey header")
	cmd.Flags().StringVar(&gpu, "gpu", "", "GPU accelerator for an auto-created runtime")
	cmd.Flags().StringVar(&tpu, "tpu", "", "TPU accelerator for an auto-created runtime")
	cmd.Flags().BoolVar(&highMem, "high-mem", false, "Request a high-RAM machine shape")
	cmd.Flags().BoolVar(&rm, "rm", false, "Stop the runtime when the session ends")
	cmd.Flags().BoolVar(&force, "force", false,
		"Kill all existing SSH sessions on the runtime before connecting")
	cmd.Flags().StringVar(&internalMode, "colab-internal-mode", "", "Internal SSH mode")
	_ = cmd.Flags().MarkHidden("colab-internal-mode")
	// Options after the first CMD word belong to CMD, not to golab.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func registerSSHCommands(root *cobra.Command) { root.AddCommand(sshCommand()) }
