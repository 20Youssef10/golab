package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/envlock" // TIER4: --lockfile probe type
	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
)

// extractInlineEnv mirrors upstream's `_extract_env_args_from_script_args`:
// --env after the script path is still a colab option, everything else is
// forwarded verbatim to the script's sys.argv.
func extractInlineEnv(args []string) ([]string, []string) {
	var forwarded, env []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--env":
			if i+1 >= len(args) {
				env = append(env, "")
				i++
			} else {
				env = append(env, args[i+1])
				i++
			}
		case strings.HasPrefix(args[i], "--env="):
			env = append(env, strings.TrimPrefix(args[i], "--env="))
		default:
			forwarded = append(forwarded, args[i])
		}
	}
	return forwarded, env
}

func stripShebang(body string) string {
	if !strings.HasPrefix(body, "#!") {
		return body
	}
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		return body[i+1:]
	}
	return ""
}

// buildScriptPayload produces Python source that runs the script body with
// python-like sys.argv/__name__ semantics. Every user string is JSON-encoded
// into a Python literal before being embedded (never string-spliced). A read
// failure is returned to the caller, which must surface it before allocating
// a runtime — silently returning an empty payload would run nothing on a
// billable VM and exit 0.
func buildScriptPayload(script string, args []string, env []envVar) (string, error) {
	body, err := os.ReadFile(script)
	if err != nil {
		return "", err
	}
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, filepath.Base(script))
	argv = append(argv, args...)
	literals := make([]string, 0, len(argv))
	for _, a := range argv {
		literals = append(literals, pyRepr(a))
	}
	var b strings.Builder
	b.WriteString("import sys, warnings\n")
	fmt.Fprintf(&b, "sys.argv = [%s]\n", strings.Join(literals, ", "))
	b.WriteString("__name__ = '__main__'\n")
	b.WriteString("warnings.filterwarnings('ignore', message=\"To exit: use\")\n")
	b.WriteString(envPrelude(env))
	b.WriteString(stripShebang(string(body)))
	return b.String(), nil
}

func systemExitCode(value string) int {
	v := strings.TrimSpace(value)
	if v == "" || v == "None" || v == "0" || v == "'0'" || v == `"0"` {
		return 0
	}
	v = strings.Trim(v, `'"`)
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return 1
}

// exitCodeFromOutputs mirrors CPython's sys.exit conventions.
func exitCodeFromOutputs(outs []runtime.Output) int {
	code := 0
	for _, o := range outs {
		if o.OutputType != "error" {
			continue
		}
		if o.EName == "SystemExit" {
			if c := systemExitCode(o.EValue); c != 0 {
				code = c
			}
			continue
		}
		return 1
	}
	return code
}

// isTransportError reports errors that may leave a kernel execution still
// running without a live socket. teardown must be skipped for these so the
// session and its Pending handle survive for `colab resume`.
func isTransportError(err error) bool {
	return errors.Is(err, runtime.ErrOutputGap) ||
		errors.Is(err, runtime.ConnectionLost) ||
		errors.Is(err, runtime.ErrTimeout)
}

// writePending records a recovery handle before code is submitted. The code's
// hash is kept for identification only; no source is reconstructed on resume.
// A failed write is fatal: an unpersisted handle makes `colab resume` fail
// with ErrNoPendingExecution, so the caller must abort before submission.
func writePending(d *app.Deps, s *state.Session, k runtime.Kernel, msgID, code string) error {
	sum := sha256.Sum256([]byte(code))
	s.Pending = &state.PendingExecution{
		MsgID:      msgID,
		SessionID:  k.SessionID(),
		StartedAt:  time.Now().UTC(),
		CodeSHA256: hex.EncodeToString(sum[:]),
	}
	if err := storePut(d, *s); err != nil {
		s.Pending = nil // never leave an unpersisted handle looking live
		return fmt.Errorf("could not record pending execution before submission: %w", err)
	}
	return nil
}

func bumpPending(d *app.Deps, s *state.Session, delivered int) {
	if s.Pending == nil {
		return
	}
	s.Pending.Delivered = delivered
	reportStateWriteError(d, "pending", storePut(d, *s))
}

func clearPending(d *app.Deps, s *state.Session) {
	if s.Pending == nil {
		return
	}
	s.Pending = nil
	reportStateWriteError(d, "pending", storePut(d, *s))
}

// executeTracked wraps Execute with Pending bookkeeping: a handle is written
// before submission, cleared on terminal success, and kept (with the recovered
// count) on any failure.
func executeTracked(ctx context.Context, d *app.Deps, s *state.Session, k runtime.Kernel, code string, opts runtime.ExecOptions) (*runtime.Result, error) {
	msgID := uuid.NewString()
	opts.MsgID = msgID
	if err := writePending(d, s, k, msgID, code); err != nil {
		return nil, err // no cell is submitted without a durable recovery handle
	}
	res, err := k.Execute(ctx, code, opts)
	if err != nil {
		var gap *runtime.OutputGapError
		if errors.As(err, &gap) {
			if s.Pending != nil && gap.MsgID != "" {
				s.Pending.MsgID = gap.MsgID
			}
			bumpPending(d, s, len(gap.Recovered))
		}
		return nil, err
	}
	clearPending(d, s)
	return res, nil
}

// runErrorReason classifies a failed Execute. A transport interruption keeps
// the session (no prune) and prints the recovery hint; only ErrSessionLost is
// pruned, as before.
func runErrorReason(d *app.Deps, name string, err error) (string, error) {
	if errors.Is(err, runtime.ErrSessionLost) {
		return "run_failed", pruneLost(d, name, err)
	}
	if isTransportError(err) {
		fmt.Fprintf(d.Stderr, "[colab] Work may still be running. Recover with: golab resume -s %s\n", name)
		return "transport_interrupted", err
	}
	return "run_failed", err
}

func teardown(ctx context.Context, d *app.Deps, name string, s state.Session, reason string) {
	fmt.Fprintf(d.Stderr, "[colab] Stopping session '%s'...\n", name)
	if fresh, err := d.Store.Get(name); err == nil && fresh.KernelID != "" {
		if kernel, err := d.RuntimeFor(ctx, fresh); err == nil {
			_ = kernel.Shutdown(ctx)
			_ = kernel.Close()
		}
	}
	if client, err := d.ColabClient(ctx); err == nil {
		_ = client.UnassignRuntime(ctx, s.Endpoint, s.RuntimeName)
	}
	_ = d.Store.Delete(name)
	writeEvent(d, name, "session_terminated", map[string]any{"reason": reason})
	fmt.Fprintln(d.Stderr, "[colab] Session terminated.")
}

func runCommand() *cobra.Command {
	var name, gpu, tpu string
	var highMem, keep bool
	var timeout float64
	var env []string
	// TIER4 additions: notebook params, lockfile verification, run provenance.
	var paramsFlag []string
	var paramsFileFlag, lockfile, bundleOut string
	var bundleFlag bool
	// Tier 1 item 4: in-run compute-unit ceiling (advisory unless --enforce).
	var maxCU float64
	var runEnforce bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a Python script on a fresh Colab VM, then release it",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usagef("[colab] Missing SCRIPT argument.")
			}
			// Validate before PersistentPreRunE/app.New: a typo must never
			// reach the allocation path or even initialise credentials.
			info, err := os.Stat(args[0])
			if err != nil || info.IsDir() {
				return usagef("[colab] Script not found: %s", args[0])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			d := depsOf(cmd)
			script := args[0]
			forwarded, inlineEnv := extractInlineEnv(args[1:])
			envVars, err := parseEnvVars(append(append([]string(nil), env...), inlineEnv...))
			if err != nil {
				return err
			}
			// TIER4: resolve papermill params and the lockfile before any
			// allocation: a bad file must never cost a billable VM.
			params := append([]string(nil), paramsFlag...)
			paramsFile := paramsFileFlag
			if strings.HasSuffix(strings.ToLower(script), ".ipynb") {
				var inlineParams []string
				var inlineFile string
				forwarded, inlineParams, inlineFile = extractInlineParams(forwarded)
				params = append(params, inlineParams...)
				if inlineFile != "" {
					paramsFile = inlineFile
				}
			}
			var lock *envlock.Lockfile
			if lockfile != "" {
				raw, readErr := os.ReadFile(lockfile)
				if readErr != nil {
					return usagef("[colab] Lockfile '%s' not found.", filepath.Base(lockfile))
				}
				lock, err = envlock.Parse(raw)
				if err != nil {
					return usagef("[colab] Invalid lockfile: %s", err.Error())
				}
			}
			if err := validateSessionName(name); err != nil {
				return err
			}
			// Validate before allocating: a typo must not cost real compute.
			info, err := os.Stat(script)
			if err != nil || info.IsDir() {
				return usagef("[colab] Script not found: %s", script)
			}
			opts, err := resolveRuntimeOptions(gpu, tpu, highMem)
			if err != nil {
				return err
			}
			if name == "" {
				name = randomName("run-")
			}
			if highMem && highMemOnly(opts.accelerator) {
				fmt.Fprintln(d.Stderr, "[colab] --high-mem ignored: this accelerator only offers one machine shape.")
			}
			if explainRequested(cmd) {
				cleanup := "unassign"
				if keep {
					cleanup = "keep"
				}
				fmt.Fprintln(d.Stdout, ExplainPlan(cmd, PlanFields{
					Operation: "run", Method: "POST", API: apiLabel(d),
					Variant: string(opts.variant), Accelerator: string(opts.accelerator),
					Shape: opts.shape.String(), Session: name, Cleanup: cleanup,
					CreatesSession: true, Billable: true,
				}))
				return nil
			}
			// Issue #74 / PR #200: a notebook is parsed up front so a malformed
			// file never costs a VM allocation. readCodeBlocks (execution.go)
			// is the single notebook parser; markdown cells are dropped there.
			isNB := strings.HasSuffix(strings.ToLower(script), ".ipynb")
			var notebook *nb.Notebook
			var blocks []codeBlock
			var payload string
			if isNB {
				var err error
				blocks, notebook, err = readCodeBlocks(d, script)
				if err != nil {
					return err
				}
				// TIER4: inject -p/--params-file assignments after the
				// parameters-tagged cell, before any VM is allocated.
				blocks, err = applyNotebookParams(notebook, blocks, params, paramsFile)
				if err != nil {
					return err
				}
				if len(forwarded) > 0 {
					// No sys.argv injection for notebooks: never echo the values.
					fmt.Fprintf(d.Stderr, "[colab] Warning: extra arguments ignored for notebook '%s' (no sys.argv injection).\n", filepath.Base(script))
				}
			} else {
				// Read the plain script up front, next to the notebook parse
				// above: an unreadable file must fail before createSession so it
				// costs zero requests and never runs an empty (false-success)
				// payload on a billable VM. The Stat checks above only narrow
				// the TOCTOU/permission/special-file window.
				var err error
				payload, err = buildScriptPayload(script, forwarded, envVars)
				if err != nil {
					return usagef("[colab] Could not read script '%s': %v", script, err)
				}
			}
			if err := guardBudget(cmd.Context(), d, cmd); err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()

			fmt.Fprintf(d.Stderr, "[colab] Creating session '%s'...\n", name)
			s, err := createSession(ctx, d, name, opts)
			if err != nil {
				return mapAssignFailure(err, opts)
			}
			writeEvent(d, name, "session_created", map[string]any{"via": "run"})
			// Tier 1 item 4: start the bounded in-run CU monitor after the
			// billable runtime exists, so a breach can interrupt and stop it.
			monCtx, cuMon, cuErr := startRunCUMonitor(ctx, d, cmd, maxCU, runEnforce)
			if cuErr != nil {
				return cuErr
			}
			if cuMon != nil {
				ctx = monCtx
				defer cuMon.Stop()
			}
			fmt.Fprintf(d.Stderr, "[colab] Session READY (%s). Executing %s...\n", name, script)

			kernel, err := d.RuntimeFor(ctx, &s)
			if err != nil {
				if !keep {
					// A CU-breach cancellation must still release the runtime:
					// cleanup runs on a detached context (already the case in
					// the deferred teardown below).
					teardown(context.Background(), d, name, s, "run_failed")
				}
				return pruneLost(d, name, err)
			}
			exitCode := 0
			reason := "run_completed"
			defer func() {
				setRunning(d, &s, nil)
				_ = kernel.Close()
				if isNB && notebook != nil {
					out := strings.TrimSuffix(script, filepath.Ext(script)) + "_output.ipynb"
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
				// A transport interruption may leave the kernel still working.
				// Keep the session and Pending for `colab resume`; only Close
				// the local socket (above), never Shutdown/Unassign/Delete.
				if reason == "transport_interrupted" {
					return
				}
				if !keep {
					teardown(context.Background(), d, name, s, reason)
				}
			}()

			if _, err := executeTracked(ctx, d, &s, kernel, contentPrelude, runtime.ExecOptions{IdleTimeout: 30 * time.Second}); err != nil {
				var e error
				reason, e = runErrorReason(d, name, err)
				return e
			}

			// TIER4: verify hardware/interpreter and install the lockfile on
			// the freshly allocated VM before the user's code runs.
			var probedEnv *envlock.Lockfile
			if lock != nil {
				var e error
				probedEnv, e = applyLockfile(ctx, d, &s, kernel, lock)
				if e != nil {
					reason = "run_failed"
					return e
				}
			}

			if isNB {
				// One kernel for every cell: state set by an earlier cell is
				// visible to later cells. --env is re-applied per cell because
				// each Execute is an independent kernel request.
				prelude := envPrelude(envVars)
			cells:
				for i, block := range blocks {
					code := prelude + string(block.code)
					label := nb.Title(code)
					if label == "" {
						label = block.id
					}
					var cellID *string
					suffix := ""
					if label != "" {
						cellID = &label
						suffix = " - " + label
					}
					fmt.Fprintf(d.Stderr, "[colab] Executing cell %d/%d%s...\n", i+1, len(blocks), suffix)
					setRunning(d, &s, running("run("+filepath.Base(script)+")"))
					setLastExecution(d, &s, script, cellID)
					var renderErr error
					res, err := executeTracked(ctx, d, &s, kernel, code, runtime.ExecOptions{
						IdleTimeout: timeoutOption(timeout),
						OnOutput: func(o runtime.Output) {
							if o.OutputType == "error" && o.EName == "SystemExit" {
								return // never print a SystemExit traceback
							}
							if e := renderOne(d, o, ""); e != nil && renderErr == nil {
								renderErr = e
							}
						},
					})
					switch {
					case err != nil:
						var e error
						reason, e = runErrorReason(d, name, err)
						return e
					case renderErr != nil:
						reason = "run_failed"
						return renderErr
					default:
						notebookCellResult(&notebook.Cells[block.cellIdx], res.Outputs, res.ExecutionCount)
						logExecution(d, name, code, res.Outputs, map[string]any{
							"via": "run", "cell_index": i, "cell_id": block.id,
						})
						if c := exitCodeFromOutputs(res.Outputs); c != 0 {
							exitCode = c
							reason = "run_failed"
							break cells // CPython-style: stop on the first failing cell
						}
					}
				}
			} else {
				// payload was built before createSession, so an unreadable
				// script could never reach the allocation path above.
				setRunning(d, &s, running("run("+filepath.Base(script)+")"))
				setLastExecution(d, &s, script, nil)
				var renderErr error
				res, err := executeTracked(ctx, d, &s, kernel, payload, runtime.ExecOptions{
					IdleTimeout: timeoutOption(timeout),
					OnOutput: func(o runtime.Output) {
						if o.OutputType == "error" && o.EName == "SystemExit" {
							return // never print a SystemExit traceback
						}
						if e := renderOne(d, o, ""); e != nil && renderErr == nil {
							renderErr = e
						}
					},
				})
				switch {
				case err != nil:
					var e error
					reason, e = runErrorReason(d, name, err)
					return e
				case renderErr != nil:
					reason = "run_failed"
					return renderErr
				default:
					exitCode = exitCodeFromOutputs(res.Outputs)
					if exitCode != 0 {
						reason = "run_failed"
					}
					logExecution(d, name, payload, res.Outputs, map[string]any{"via": "run"})
				}
			}
			// TIER4: record provenance only after a successful execution;
			// it stays "recorded", never a verified replay claim.
			if bundleFlag {
				codeBytes, readErr := os.ReadFile(script)
				if readErr != nil {
					reason = "run_failed"
					return readErr
				}
				out := bundleOut
				if out == "" {
					out = "run.json"
				}
				if err := recordRunBundle(cmd.Context(), d, &s, kernel, script, codeBytes, out, probedEnv); err != nil {
					reason = "run_failed"
					return err
				}
			}
			if ctx.Err() != nil {
				return exitf(130, "")
			}
			if exitCode != 0 {
				return exitf(exitCode, "")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Ephemeral session name")
	cmd.Flags().StringVar(&gpu, "gpu", "", "GPU accelerator variant")
	cmd.Flags().StringVar(&tpu, "tpu", "", "TPU accelerator variant")
	cmd.Flags().BoolVar(&highMem, "high-mem", false, "Request a high-RAM machine shape")
	cmd.Flags().BoolVar(&keep, "keep", false, "Do not stop the session after the script finishes")
	cmd.Flags().Float64Var(&timeout, "timeout", 30, "Seconds without output before failing (0 for no limit)")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Set KEY=VALUE in the remote kernel (repeatable)")
	// TIER4 flags.
	cmd.Flags().StringArrayVarP(&paramsFlag, "param", "p", nil, "Set NAME=VALUE notebook parameter (repeatable)")
	cmd.Flags().StringVar(&paramsFileFlag, "params-file", "", "JSON/YAML notebook params file")
	cmd.Flags().StringVar(&lockfile, "lockfile", "", "Verify and install this environment lockfile before running")
	cmd.Flags().BoolVar(&bundleFlag, "bundle", false, "Record a provenance manifest for this run")
	cmd.Flags().StringVar(&bundleOut, "out", "", "Provenance manifest path for --bundle (default run.json)")
	// Tier 1 item 4 (surgical additions; keep this block together).
	cmd.Flags().Float64Var(&maxCU, "max-cu", 0, "Minimum compute-unit balance to maintain during the run (0 disables)")
	cmd.Flags().BoolVar(&runEnforce, "enforce", false, "Interrupt and stop the run when --max-cu is breached")
	// Unknown options after SCRIPT belong to the script, not to golab.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func registerRunCommands(root *cobra.Command) { root.AddCommand(runCommand()) }
