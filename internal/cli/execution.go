package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/agentout"
	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/platform"
	"github.com/20Youssef10/golab/internal/render"
	"github.com/20Youssef10/golab/internal/repl"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
)

const contentPrelude = "import os; os.makedirs('/content', exist_ok=True); os.chdir('/content')"

// execWarningsPrelude suppresses only IPython's harmless "To exit: use ..."
// hint that a clean sys.exit emits. It is message-scoped: every other warning
// keeps its normal behaviour (no global simplefilter/filterwarnings override).
const execWarningsPrelude = `import warnings; warnings.filterwarnings("ignore", message="To exit: use")` + "\n"

// attachSession resolves a session, attaches its kernel and performs the
// mandatory /content chdir. Terminal 401/404 errors prune the local session.
func attachSession(ctx context.Context, d *app.Deps, name string) (*state.Session, runtime.Kernel, error) {
	s, err := d.ResolveSession(ctx, name)
	if err != nil {
		return nil, nil, sessionErr(name, err)
	}
	kernel, err := d.RuntimeFor(ctx, s)
	if err != nil {
		return nil, nil, pruneLost(d, s.Name, err)
	}
	if _, err := kernel.Execute(ctx, contentPrelude, runtime.ExecOptions{IdleTimeout: 30 * time.Second}); err != nil {
		_ = kernel.Close()
		return nil, nil, pruneLost(d, s.Name, err)
	}
	return s, kernel, nil
}

func timeoutOption(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

type codeBlock struct {
	code    string
	id      string
	cellIdx int
}

func readCodeBlocks(d *app.Deps, file string) ([]codeBlock, *nb.Notebook, error) {
	if file == "" {
		if platform.IsStdinTTY() {
			return nil, nil, exitf(1, "[colab] Error: No input provided. Pipe code or provide a file.")
		}
		data, err := io.ReadAll(d.Stdin)
		if err != nil {
			return nil, nil, err
		}
		return []codeBlock{{code: string(data)}}, nil, nil
	}
	if strings.HasSuffix(strings.ToLower(file), ".ipynb") {
		fmt.Fprintf(d.Stderr, "[colab] Parsing notebook '%s'...\n", file)
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, nil, err
		}
		notebook, err := nb.Parse(data)
		if err != nil {
			return nil, nil, err
		}
		blocks := make([]codeBlock, 0)
		for i := range notebook.Cells {
			cell := &notebook.Cells[i]
			if cell.CellType != "code" {
				continue
			}
			if cell.ID == "" {
				cell.ID = uuid.NewString()
			}
			blocks = append(blocks, codeBlock{code: string(cell.Source), id: cell.ID, cellIdx: i})
		}
		return blocks, notebook, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	return []codeBlock{{code: string(data)}}, nil, nil
}

// systemExitMessage is the single CPython translation used by BOTH output
// modes. IPython keeps the kernel alive by reporting sys.exit as an error
// output: empty/None/integer codes are silent, and only a non-integer value
// is the message CPython would print to stderr (without the "%tb" banner).
func systemExitMessage(value string) (message string, quiet bool) {
	v := strings.TrimSpace(value)
	if v == "" || v == "None" {
		return "", true
	}
	if _, err := strconv.Atoi(strings.Trim(v, `"'`)); err == nil {
		return "", true
	}
	return v, false
}

// execOutputHook renders a cell's outputs. SystemExit is translated back to
// CPython's UX; every other output is rendered exactly as before. Exit codes
// come from exitCodeFromOutputs and are deliberately untouched.
func execOutputHook(d *app.Deps, outputImage string) func(runtime.Output) error {
	return func(o runtime.Output) error {
		if o.OutputType == "error" && o.EName == "SystemExit" {
			value, quiet := systemExitMessage(o.EValue)
			if quiet {
				return nil
			}
			_, err := fmt.Fprintln(d.Stderr, render.Sanitize(value))
			return err
		}
		return renderOne(d, o, outputImage)
	}
}

// ansiEscapeRE matches the CSI sequences IPython uses to colour tracebacks.
// --json promises plain text, so they are removed before the record is built.
var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// plainTraceback joins a traceback into a single control-character-free line
// block. render.Sanitize additionally removes OSC/DCS and stray C0 controls.
func plainTraceback(lines []string) string {
	return render.Sanitize(ansiEscapeRE.ReplaceAllString(strings.Join(lines, "\n"), ""))
}

// ipythonExitHintRE matches only IPython's "To exit: use ..." UserWarning
// (including its indented warn(...) source line) on kernel stderr. The
// prelude stops it at the source; this pass keeps an already-emitted copy out
// of a --json record. Other warnings are untouched.
var ipythonExitHintRE = regexp.MustCompile(`(?m)^[^\r\n]*UserWarning: To exit: use[^\r\n]*(?:\r?\n[ \t]+warn\("To exit: use[^\r\n]*)?\r?\n?`)

func filterIPythonExitHint(s string) string {
	return ipythonExitHintRE.ReplaceAllString(s, "")
}

// saveCellImage decodes a kernel image payload and saves it through the shared
// render path (0600 file, --output-image honoured). Nothing is written to the
// record's stdout: only the sanitized path is returned.
func saveCellImage(value any, mime, target string) (string, error) {
	var data []byte
	switch x := value.(type) {
	case []byte:
		data = x
	case string:
		if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(x)); err == nil {
			data = b
		} else {
			data = []byte(x)
		}
	default:
		return "", fmt.Errorf("exec: unsupported %s payload %T", mime, value)
	}
	var buf bytes.Buffer
	err := render.ImageWithConfig(&buf, data, mime, render.ImageConfig{
		TargetPath: target,
		Protocol:   "none", // never emit Kitty/iTerm2 escapes into a JSON record
		Stderr:     io.Discard,
	})
	if err != nil {
		return "", err
	}
	const prefix = "[Image saved to: "
	s := buf.String()
	i := strings.Index(s, prefix)
	if i < 0 {
		return "", errors.New("exec: image save did not report a path")
	}
	rest := s[i+len(prefix):]
	// render appends the closing ']' itself after the (sanitized) path. A
	// --output-image path may legitimately contain ']', so the delimiter is
	// the *last* ']' in the report, not the first one.
	j := strings.LastIndexByte(rest, ']')
	if j < 0 {
		return "", errors.New("exec: image save reported a malformed path")
	}
	return rest[:j], nil
}

// buildCellRecord summarises one cell's kernel outputs as the upstream PR #237
// record. Inputs are already redacted by execRedactor before reaching here.
func buildCellRecord(outs []runtime.Output, index int, cellID *string, outputImage string, red *execRedactor, stderr io.Writer) (agentout.CellResult, error) {
	rec := agentout.CellResult{
		CellIndex: index,
		CellID:    cellID,
		Status:    "ok",
		Data:      []agentout.DataItem{},
		Images:    []string{},
	}
	for _, o := range outs {
		switch {
		case o.OutputType == "stream":
			text := red.Redact(o.Text)
			if o.Name == "stderr" {
				rec.Stderr += filterIPythonExitHint(text)
			} else {
				rec.Stdout += text
			}
		case o.OutputType == "error":
			if o.EName == "SystemExit" {
				// --json must agree with text mode: a successful sys.exit is
				// not a failed cell, and IPython's "%tb" banner belongs to no
				// record. Only the CPython message is kept, if any.
				message, quiet := systemExitMessage(o.EValue)
				if quiet && systemExitCode(o.EValue) == 0 {
					continue // sys.exit()/None/0: status stays "ok", error null
				}
				value := message
				if quiet {
					value = strings.Trim(strings.TrimSpace(o.EValue), `"'`)
				}
				value = red.Redact(plainTraceback([]string{value}))
				rec.Status = "error"
				rec.Error = &agentout.CellError{
					EName: "SystemExit", EValue: value, Traceback: "",
				}
				if !quiet {
					// Captured in the record for machines, and echoed on
					// stderr exactly as text mode does for humans.
					rec.Stderr += value + "\n"
					if stderr != nil {
						_, _ = fmt.Fprintln(stderr, value)
					}
				}
				continue
			}
			rec.Status = "error"
			rec.Error = &agentout.CellError{
				EName:     red.Redact(o.EName),
				EValue:    red.Redact(o.EValue),
				Traceback: red.Redact(plainTraceback(o.Traceback)),
			}
		case o.Data != nil:
			for _, mime := range []string{"image/png", "image/jpeg"} {
				value, ok := o.Data[mime]
				if !ok {
					continue
				}
				path, err := saveCellImage(value, mime, outputImage)
				if err != nil {
					return rec, err
				}
				rec.Images = append(rec.Images, red.Redact(path))
				break
			}
			kind := o.OutputType
			if kind != "display_data" && kind != "execute_result" {
				kind = "display_data"
			}
			item := agentout.DataItem{"output_type": kind}
			for mime, value := range o.Data {
				if strings.HasPrefix(strings.ToLower(mime), "image/") {
					continue // images are reported by path, never inlined
				}
				item[mime] = red.Any(value)
			}
			rec.Data = append(rec.Data, item)
		}
	}
	return rec, nil
}

// clientErrorEname names a client-side failure like upstream's
// type(e).__name__ (TimeoutError, SessionLostError, …) without echoing its
// message, which may contain user data.
func clientErrorEname(err error) string {
	name := fmt.Sprintf("%T", err)
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimPrefix(name, "*")
	if name == "" || name == "errorString" || name == "exitError" {
		return "RuntimeError"
	}
	return name
}

// redactedExecError keeps the mapped exit code but removes any injected secret
// from an error message before it reaches stderr.
func redactedExecError(err error, red *execRedactor) error {
	mapped := mapError(err)
	var ee *exitError
	if errors.As(mapped, &ee) {
		return exitf(ee.code, "%s", red.Redact(ee.msg))
	}
	return exitf(1, "%s", red.Redact(mapped.Error()))
}

func execCommand() *cobra.Command {
	var name, file, outputImage string
	var timeout float64
	var env []string
	var secretNames, envFiles []string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Execute code in a session",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			envVars, err := parseEnvVars(env)
			if err != nil {
				return err
			}
			// Resolve every injected value before session resolution or any
			// allocation: a bad key/mode must exit 2 with zero network calls.
			var cliSecrets []injectedSecret
			var sensitive []logx.Secret
			for _, secretName := range secretNames {
				value, err := resolveSecretValue(cmd.Context(), secretName)
				if err != nil {
					return err
				}
				cliSecrets = append(cliSecrets, injectedSecret{name: secretName, value: value})
				sensitive = append(sensitive, value)
			}
			for _, envFile := range envFiles {
				vars, secrets, err := parseEnvFile(envFile)
				if err != nil {
					return err
				}
				envVars = append(envVars, vars...)
				for _, s := range secrets {
					sensitive = append(sensitive, s.value)
				}
			}
			red := newExecRedactor(sensitive...)
			blocks, notebook, err := readCodeBlocks(d, file)
			if err != nil {
				return err
			}
			hasCode := false
			for _, b := range blocks {
				if strings.TrimSpace(b.code) != "" {
					hasCode = true
					break
				}
			}
			if !hasCode {
				return nil
			}
			s, kernel, err := attachSession(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			// --json keeps stdout for records only; every [colab] progress line
			// already goes to stderr. Default to NDJSON (one record per cell)
			// and let the root --output json flag upgrade to a single document.
			var cells *agentout.CellWriter
			if jsonOutput {
				format := "ndjson"
				if agentout.IsWrapped(d.Stdout) {
					format = "json"
				}
				cells, err = agentout.NewCellWriter(d.Stdout, format)
				if err != nil {
					return mapAgentOutError(err)
				}
				defer func() { _ = cells.Close() }()
			}
			prelude := execWarningsPrelude + injectedPrelude(envVars, cliSecrets)
			isNB := strings.HasSuffix(strings.ToLower(file), ".ipynb")
			defer func() {
				setRunning(d, s, nil)
				_ = kernel.Close()
				if isNB && notebook != nil {
					out := strings.TrimSuffix(file, filepath.Ext(file)) + "_output.ipynb"
					if err := saveNotebookResult(d, notebook, out); err != nil {
						if runErr == nil {
							// The outputs exist nowhere else: fail the command
							// rather than claim a notebook was saved.
							runErr = err
						} else if d.Log != nil {
							d.Log.Debug("notebook output save failed after command error", "error", err)
						}
					}
				}
			}()
			setRunning(d, s, running("exec("+sourceLabel(file)+")"))
			outputHook := execOutputHook(d, outputImage)
			for i, block := range blocks {
				code := prelude + block.code
				var cellID *string
				if isNB {
					// Read the title from the user's cell only: the prelude may
					// contain injected secrets and must never become an ID that
					// is persisted in session state or history.
					id := nb.Title(block.code)
					if id == "" {
						id = block.id
					}
					if id != "" {
						cellID = &id
					}
					suffix := ""
					if id != "" {
						suffix = " - " + id
					}
					fmt.Fprintf(d.Stderr, "[colab] Executing cell %d/%d%s...\n", i+1, len(blocks), suffix)
				}
				setLastExecution(d, s, sourceLabel(file), cellID)
				var renderErr error
				res, err := kernel.Execute(cmd.Context(), code, runtime.ExecOptions{
					IdleTimeout: timeoutOption(timeout),
					OnOutput: func(o runtime.Output) {
						if jsonOutput {
							return // records are built from the returned outputs
						}
						if e := outputHook(red.Output(o)); e != nil && renderErr == nil {
							renderErr = e
						}
					},
				})
				if err == nil && renderErr != nil {
					err = renderErr
				}
				if err != nil {
					if jsonOutput {
						// A client-side failure never reaches the kernel's
						// outputs; report it in the same record shape before
						// failing with the original exit status.
						rec := agentout.CellResult{
							CellIndex: i, CellID: cellID, Status: "error",
							Data: []agentout.DataItem{}, Images: []string{},
							Error: &agentout.CellError{
								EName:     clientErrorEname(err),
								EValue:    red.Redact(err.Error()),
								Traceback: "",
							},
						}
						if e := cells.Emit(rec); e != nil {
							return e
						}
						if e := pruneLost(d, s.Name, err); e != nil {
							return e
						}
						return redactedExecError(err, red)
					}
					return pruneLost(d, s.Name, err)
				}
				if jsonOutput {
					rec, buildErr := buildCellRecord(res.Outputs, i, cellID, outputImage, red, d.Stderr)
					if buildErr != nil {
						return buildErr
					}
					if e := cells.Emit(rec); e != nil {
						return e
					}
				}
				// Redact before anything is persisted or echoed: history, the
				// saved notebook and every output hook share these copies.
				safeOutputs := red.Outputs(res.Outputs)
				if notebook != nil {
					notebookCellResult(&notebook.Cells[block.cellIdx], safeOutputs, res.ExecutionCount)
				}
				extra := map[string]any{}
				if isNB {
					extra["cell_index"] = i
					extra["cell_id"] = block.id
				}
				logExecution(d, s.Name, red.Redact(code), safeOutputs, extra)
				// A persistent IPython kernel keeps running after sys.exit, so
				// the kernel's error outputs are the only signal of a failed
				// cell. Mirror run's CPython mapping and stop at the first
				// non-zero cell; the deferred notebook write still runs.
				if exitCode := exitCodeFromOutputs(res.Outputs); exitCode != 0 {
					return exitf(exitCode, "")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().StringVarP(&file, "file", "f", "", "File to execute")
	cmd.Flags().StringVar(&outputImage, "output-image", "", "Path to save a displayed image")
	cmd.Flags().Float64Var(&timeout, "timeout", 30, "Seconds without output before failing (0 for no limit)")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Set KEY=VALUE in the remote kernel (repeatable)")
	cmd.Flags().StringArrayVar(&secretNames, "secret", nil, "Inject NAME from the process environment or OS keyring into the kernel (repeatable)")
	cmd.Flags().StringArrayVar(&envFiles, "env-file", nil, "Load KEY=VALUE from a mode-0600 file into the kernel (repeatable)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print one JSON record per executed cell on stdout (exit 1 if any cell failed)")
	return cmd
}

func sourceLabel(file string) string {
	if file == "" {
		return "stdin"
	}
	return file
}

// replKernel adapts runtime.Kernel to repl.Kernel and records execution events.
type replKernel struct {
	kernel      runtime.Kernel
	d           *app.Deps
	name        string
	outputImage string
}

func (k *replKernel) Execute(ctx context.Context, code string, opts repl.ExecOptions) (repl.Result, error) {
	res, err := k.kernel.Execute(ctx, code, runtime.ExecOptions{
		IdleTimeout: opts.Timeout,
		OnOutput: func(o runtime.Output) {
			if opts.OutputHook == nil {
				return
			}
			converted := repl.Output{
				OutputType: o.OutputType, Name: o.Name, Text: o.Text,
				Ename: o.EName, Evalue: o.EValue, Traceback: o.Traceback,
				Data: map[string]string{},
			}
			for mime, value := range o.Data {
				if s, ok := value.(string); ok {
					converted.Data[mime] = s
				}
			}
			opts.OutputHook(converted)
		},
	})
	if err != nil {
		return repl.Result{}, err
	}
	writeEvent(k.d, k.name, "execution", map[string]any{"code": code, "outputs": runtimeOutputsAsMaps(res.Outputs)})
	out := repl.Result{Outputs: make([]repl.Output, 0, len(res.Outputs))}
	for _, o := range res.Outputs {
		converted := repl.Output{
			OutputType: o.OutputType, Name: o.Name, Text: o.Text,
			Ename: o.EName, Evalue: o.EValue, Traceback: o.Traceback,
			Data: map[string]string{},
		}
		for mime, value := range o.Data {
			if s, ok := value.(string); ok {
				converted.Data[mime] = s
			}
		}
		out.Outputs = append(out.Outputs, converted)
	}
	return out, err
}

func replRenderer(kind, data string) string {
	switch kind {
	case "text/html":
		if md, err := render.HTMLToMarkdown(data); err == nil {
			return md
		}
	case "text/markdown":
		if out, err := render.RenderMarkdown(data); err == nil {
			return out
		}
	}
	return data
}

func replCommand() *cobra.Command {
	var name, outputImage string
	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Start an interactive REPL",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			s, kernel, err := attachSession(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			defer func() { _ = kernel.Close() }()
			opts := []repl.Option{
				repl.WithRenderer(replRenderer),
				repl.WithOutputImage(outputImage),
				repl.WithLogger(d.Log),
			}
			rk := &replKernel{kernel: kernel, d: d, name: s.Name, outputImage: outputImage}
			if !platform.IsStdinTTY() {
				setRunning(d, s, running("repl(stdin)"))
				setLastExecution(d, s, "stdin", nil)
				defer setRunning(d, s, nil)
				frontend := repl.NewPiped(d.Stdin, d.Stdout, d.Stderr, opts...)
				if err := frontend.Run(cmd.Context(), rk); err != nil {
					return pruneLost(d, s.Name, err)
				}
				return nil
			}
			setRunning(d, s, running("repl"))
			defer setRunning(d, s, nil)
			writeEvent(d, s.Name, "repl_started", map[string]any{})
			frontend, err := repl.NewTTY(d.Stdin, d.Stdout, d.Stderr, opts...)
			if err != nil {
				return err
			}
			if err := frontend.Run(cmd.Context(), rk); err != nil {
				return pruneLost(d, s.Name, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().StringVar(&outputImage, "output-image", "", "Path to save a displayed image")
	return cmd
}

func registerExecutionCommands(root *cobra.Command) {
	root.AddCommand(execCommand())
	root.AddCommand(replCommand())
}
