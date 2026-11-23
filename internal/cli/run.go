package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
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
// into a Python literal before being embedded (never string-spliced).
func buildScriptPayload(script string, args []string, env []envVar) string {
	body, err := os.ReadFile(script)
	if err != nil {
		return ""
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
	return b.String()
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

func teardown(ctx context.Context, d *app.Deps, name string, s state.Session, reason string) {
	fmt.Fprintf(d.Stderr, "[colab] Stopping session '%s'...\n", name)
	if fresh, err := d.Store.Get(name); err == nil && fresh.KernelID != "" {
		if kernel, err := d.RuntimeFor(ctx, fresh); err == nil {
			_ = kernel.Shutdown(ctx)
			_ = kernel.Close()
		}
	}
	if client, err := d.ColabClient(ctx); err == nil {
		_ = client.Unassign(ctx, s.Endpoint)
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
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			script := args[0]
			forwarded, inlineEnv := extractInlineEnv(args[1:])
			envVars, err := parseEnvVars(append(append([]string(nil), env...), inlineEnv...))
			if err != nil {
				return err
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

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()

			fmt.Fprintf(d.Stderr, "[colab] Creating session '%s'...\n", name)
			s, err := createSession(ctx, d, name, opts)
			if err != nil {
				return mapAssignFailure(err, opts)
			}
			writeEvent(d, name, "session_created", map[string]any{"via": "run"})
			fmt.Fprintf(d.Stderr, "[colab] Session READY (%s). Executing %s...\n", name, script)

			kernel, err := d.RuntimeFor(ctx, &s)
			if err != nil {
				if !keep {
					teardown(ctx, d, name, s, "run_failed")
				}
				return pruneLost(d, name, err)
			}
			exitCode := 0
			reason := "run_completed"
			defer func() {
				setRunning(d, &s, nil)
				_ = kernel.Close()
				if !keep {
					teardown(context.Background(), d, name, s, reason)
				}
			}()

			if _, err := kernel.Execute(ctx, contentPrelude, runtime.ExecOptions{IdleTimeout: 30 * time.Second}); err != nil {
				reason = "run_failed"
				if e := pruneLost(d, name, err); e != nil {
					return e
				}
				return err
			}

			payload := buildScriptPayload(script, forwarded, envVars)
			setRunning(d, &s, running("run("+filepath.Base(script)+")"))
			setLastExecution(d, &s, script, nil)
			var renderErr error
			res, err := kernel.Execute(ctx, payload, runtime.ExecOptions{
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
				reason = "run_failed"
				return pruneLost(d, name, err)
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
	// Unknown options after SCRIPT belong to the script, not to golab.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func registerRunCommands(root *cobra.Command) { root.AddCommand(runCommand()) }
