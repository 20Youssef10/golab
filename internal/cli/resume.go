package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/runtime"
)

// resumeCommand recovers an execution that was interrupted by a transport
// failure. It reattaches with the saved Jupyter session id and waits for the
// late terminal frames WITHOUT resubmitting the code. It never shuts the kernel
// down: the session is left in place for inspection or a second attempt.
//
// This is an honest thin layer: output the kernel published while the socket
// was down cannot be replayed (no iopub ack/resend, no REST output endpoint),
// so only messages observed by this process are recovered and emitted.
func resumeCommand() *cobra.Command {
	var name string
	var timeout float64
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Recover an interrupted execution without resubmitting code",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			s, err := d.ResolveSession(cmd.Context(), name)
			if err != nil {
				return sessionErr(name, err)
			}
			if s.Pending == nil {
				return fmt.Errorf("%w: session %q", ErrNoPendingExecution, s.Name)
			}
			p := s.Pending

			if explainRequested(cmd) {
				fmt.Fprintln(d.Stdout, ExplainPlan(cmd, PlanFields{
					Operation: "resume", Method: "POST", API: apiLabel(d),
					Session: s.Name, CreatesSession: false, Billable: false,
				}))
				return nil
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()

			target := runtime.Target{
				URL: s.URL, Token: s.Token, Endpoint: s.Endpoint,
				KernelID: s.KernelID, SessionID: p.SessionID,
			}
			kernel, err := d.Runtime.Attach(ctx, target, runtime.AttachOptions{
				OnKernelStarted: func(id string) {
					s.KernelID = id
					_ = d.Store.Put(*s)
				},
				OnSessionStarted: func(id string) {
					s.SessionID = id
					if s.Pending != nil {
						s.Pending.SessionID = id
					}
					_ = d.Store.Put(*s)
				},
			})
			if err != nil {
				return pruneLost(d, s.Name, err)
			}
			defer func() { _ = kernel.Close() }()

			fmt.Fprintf(d.Stderr, "[colab] Resuming execution '%s' in session '%s'...\n", p.MsgID, s.Name)
			var renderErr error
			res, err := kernel.Resume(ctx, p.MsgID, runtime.ExecOptions{
				IdleTimeout: timeoutOption(timeout),
				OnOutput: func(o runtime.Output) {
					if e := renderOne(d, o, ""); e != nil && renderErr == nil {
						renderErr = e
					}
				},
			})
			switch {
			case err != nil:
				if e := pruneLost(d, s.Name, err); e != nil {
					return e
				}
				return err
			case renderErr != nil:
				return renderErr
			}
			if ctx.Err() != nil {
				return exitf(130, "")
			}
			logExecution(d, s.Name, "<resumed>", res.Outputs, map[string]any{
				"via": "resume", "msg_id": p.MsgID,
			})
			// Terminal success: the recovery handle is no longer needed.
			clearPending(d, s)
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session holding the interrupted execution")
	cmd.Flags().Float64Var(&timeout, "timeout", 30, "Seconds without output before failing (0 for no limit)")
	return cmd
}

func registerResumeCommands(root *cobra.Command) { root.AddCommand(resumeCommand()) }
