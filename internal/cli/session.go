package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/platform"
	"github.com/20Youssef10/golab/internal/state"
)

var gpuAccelerators = []string{"t4", "l4", "g4", "h100", "a100"}
var tpuAccelerators = []string{"v5e1", "v6e1"}

type runtimeOptions struct {
	variant     colabapi.Variant
	accelerator colabapi.Accelerator
	shape       colabapi.Shape
	// Tier 1 items 2/5: additive TTL metadata and the capacity wait bound.
	ExpiresAt *time.Time
	TTLAction string
}

// resolveRuntimeOptions validates GPU/TPU flags *before* any network call.
// --tpu silently wins when both flags are present (upstream behaviour).
func resolveRuntimeOptions(gpu, tpu string, highMem bool) (runtimeOptions, error) {
	var out runtimeOptions
	switch {
	case tpu != "":
		out.variant = colabapi.VariantTPU
		v, err := parseAccelerator(tpu, tpuAccelerators, "--tpu")
		if err != nil {
			return out, err
		}
		out.accelerator = v
	case gpu != "":
		out.variant = colabapi.VariantGPU
		v, err := parseAccelerator(gpu, gpuAccelerators, "--gpu")
		if err != nil {
			return out, err
		}
		out.accelerator = v
	default:
		out.variant = colabapi.VariantDefault
		out.accelerator = colabapi.AccelNone
	}
	out.shape = colabapi.ShapeStandard
	if highMem {
		if p := colabapi.ResolveAssignShape(out.accelerator, true); p != nil {
			out.shape = *p
		}
	}
	return out, nil
}

func parseAccelerator(value string, allowed []string, flag string) (colabapi.Accelerator, error) {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, name := range allowed {
		if lower == name {
			return colabapi.Accelerator(strings.ToUpper(name)), nil
		}
	}
	names := make([]string, 0, len(allowed))
	for _, name := range allowed {
		names = append(names, strings.ToUpper(name))
	}
	return "", usagef("[colab] Invalid value for %s: unsupported value '%s'. Supported: %s.",
		flag, value, strings.Join(names, ", "))
}

func highMemOnly(acc colabapi.Accelerator) bool {
	switch acc {
	case colabapi.AccelL4, colabapi.AccelV5E1, colabapi.AccelV6E1:
		return true
	default:
		return false
	}
}

func randomName(prefix string) string {
	id, err := uuid.NewRandom()
	if err != nil {
		return prefix + "000000"
	}
	hex := strings.ReplaceAll(id.String(), "-", "")
	return prefix + hex[:6]
}

// createSession allocates and persists a session. Callers translate the two
// friendly allocation failures into their command-specific messages.
func createSession(ctx context.Context, d *app.Deps, name string, opts runtimeOptions) (state.Session, error) {
	client, err := d.ColabClient(ctx)
	if err != nil {
		return state.Session{}, err
	}
	nbh, err := colabapi.RandomNotebookHash()
	if err != nil {
		return state.Session{}, err
	}
	res, err := client.Assign(ctx, colabapi.AssignRequest{
		NotebookHash: nbh,
		Variant:      opts.variant,
		Accelerator:  opts.accelerator,
		Shape:        opts.shape,
	})
	if err != nil {
		return state.Session{}, err
	}
	expiry := res.RuntimeProxyInfo.ExpiresAt(nowUTC())
	s := state.Session{
		Name: name, Endpoint: res.Endpoint, RuntimeName: res.RuntimeName,
		URL:   res.RuntimeProxyInfo.URL,
		Token: res.RuntimeProxyInfo.Token, TokenExpiresAt: &expiry,
		Variant: string(opts.variant), Accelerator: string(opts.accelerator),
		MachineShape: opts.shape.String(),
		// Tier 1 item 2: persisted before the record is written so an expiry
		// check can never race with a session that exists without a TTL.
		ExpiresAt: opts.ExpiresAt, TTLAction: opts.TTLAction,
	}
	if err := d.Store.Put(s); err != nil {
		// Assign already created a billable runtime. Release it before
		// propagating so a failed record write cannot orphan a running VM.
		if unassignErr := client.Unassign(ctx, res.Endpoint); unassignErr != nil {
			return state.Session{}, fmt.Errorf(
				"session record write failed: %w; compensating unassign of %s also failed: %w",
				err, res.Endpoint, unassignErr)
		}
		return state.Session{}, fmt.Errorf("session record write failed: %w", err)
	}
	writeEvent(d, name, "session_created", map[string]any{
		"endpoint": s.Endpoint, "variant": s.Variant,
		"accelerator": s.Accelerator, "machine_shape": s.MachineShape,
	})
	return s, nil
}

func mapAssignFailure(err error, opts runtimeOptions) error {
	switch {
	case errors.Is(err, colabapi.ErrTooManyAssignments):
		return exitf(1, "[colab] Allocation refused (precondition failed). This can mean "+
			"too many active sessions, or a temporary usage or capacity limit for "+
			"the requested runtime. Run `golab stop` to free up a session, wait "+
			"and retry, or try a different accelerator.")
	case errors.Is(err, colabapi.ErrNoQuota):
		return exitf(1, "[colab] Backend rejected accelerator '%s'. You may not have "+
			"quota or entitlement for this accelerator on your account. Try a "+
			"different one (e.g. --gpu T4) or omit --gpu/--tpu for a CPU runtime.",
			opts.accelerator)
	default:
		return err
	}
}

func newSessionCommand() *cobra.Command {
	var name, gpu, tpu string
	var highMem bool
	// Tier 1 items 2 and 5: TTL and bounded capacity wait.
	var ttl, waitCapacity time.Duration
	var ttlAction string
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Create a new session",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			opts, err := resolveRuntimeOptions(gpu, tpu, highMem)
			if err != nil {
				return err
			}
			// Tier 1 item 2: validate before any allocation; a bad TTL value
			// must never cost a runtime.
			if ttl < 0 || (cmd.Flags().Changed("ttl") && ttl == 0) {
				return usagef("[colab] --ttl must be a positive duration.")
			}
			opts.TTLAction, err = parseTTLAction(ttlAction)
			if err != nil {
				return err
			}
			if ttl > 0 {
				opts.ExpiresAt = ttlExpiry(ttl)
			}
			if waitCapacity < 0 {
				return usagef("[colab] --wait-capacity must not be negative.")
			}
			if name == "" {
				name = randomName("")
			}
			if highMem && highMemOnly(opts.accelerator) {
				fmt.Fprintln(d.Stderr, "[colab] --high-mem ignored: this accelerator only offers one machine shape.")
			}
			if explainRequested(cmd) {
				fmt.Fprintln(d.Stdout, ExplainPlan(cmd, PlanFields{
					Operation: "assign", Method: "POST", API: apiLabel(d),
					Variant: string(opts.variant), Accelerator: string(opts.accelerator),
					Shape: opts.shape.String(), Session: name,
					CreatesSession: true, Billable: true,
				}))
				return nil
			}
			if err := guardBudget(cmd.Context(), d, cmd); err != nil {
				return err
			}
			fmt.Fprintf(d.Stderr, "[colab] Creating session '%s'...\n", name)
			// Tier 1 item 5: bounded retry on capacity refusal only.
			if _, err := createSessionWithCapacityWait(cmd.Context(), d, name, opts, waitCapacity); err != nil {
				return mapAssignFailure(err, opts)
			}
			fmt.Fprintln(d.Stderr, "[colab] Session READY.")
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().StringVar(&gpu, "gpu", "", "GPU accelerator variant (T4, L4, G4, H100, A100)")
	cmd.Flags().StringVar(&tpu, "tpu", "", "TPU accelerator variant (V5E1, V6E1)")
	cmd.Flags().BoolVar(&highMem, "high-mem", false, "Request a high-RAM machine shape")
	// Tier 1 items 2 and 5 (surgical additions; keep this block together).
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "Local session lifetime (e.g. 2h); checked on every invocation")
	cmd.Flags().StringVar(&ttlAction, "ttl-action", ttlActionWarn, "On expiry: warn (default) or stop")
	cmd.Flags().DurationVar(&waitCapacity, "wait-capacity", 0, "Retry capacity refusals for at most this duration")
	return cmd
}

func sessionsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List all active sessions",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			local, assignments, err := d.SyncSessions(cmd.Context())
			if err != nil {
				return err
			}
			if len(assignments) == 0 {
				fmt.Fprintln(d.Stderr, "[colab] No active sessions found on server.")
				return nil
			}
			names := make(map[string]string, len(local))
			for name, s := range local {
				names[s.Endpoint] = name
			}
			for _, a := range assignments {
				name, ok := names[a.Endpoint]
				if !ok {
					name = "?"
				}
				fmt.Fprintln(d.Stdout, formatSessionLine(name, a.Endpoint,
					string(a.Accelerator), a.Variant.String(), nil, a.MachineShape.String()))
			}
			return nil
		},
	}
}

func statusCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show session status",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			local, assignments, err := d.SyncSessions(cmd.Context())
			if err != nil {
				return err
			}
			// `sessions` prints the server-assigned shape; mirror that in
			// `status` at print time. SyncSessions deliberately does not write
			// the shape to disk, so neither do we.
			byEndpoint, byRuntime := indexAssignments(assignments)
			if name != "" {
				s, err := d.Store.Get(name)
				if err != nil {
					if errors.Is(err, state.ErrNotFound) {
						fmt.Fprintf(d.Stderr, "[colab] Session '%s' not found.\n", name)
						return nil
					}
					return err
				}
				printStatus(d, withServerShape(*s, byEndpoint, byRuntime))
				return nil
			}
			if len(local) == 0 {
				fmt.Fprintln(d.Stderr, "[colab] No active sessions.")
				return nil
			}
			for _, s := range local {
				printStatus(d, withServerShape(s, byEndpoint, byRuntime))
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	return cmd
}

func printStatus(d *app.Deps, s state.Session) {
	status := "IDLE"
	if s.Running != nil {
		status = "BUSY (" + *s.Running + ")"
	}
	fmt.Fprintln(d.Stdout, formatSessionLine(s.Name, s.Endpoint, s.Accelerator,
		s.Variant, &status, s.MachineShape))
	if le := s.LastExecution; le != nil {
		cell := ""
		if le.Value != nil && *le.Value != "" {
			cell = " | Cell: " + *le.Value
		}
		fmt.Fprintf(d.Stdout, "  Last Execution: %s%s at %s\n", le.Kind, cell, le.At)
	}
}

// indexAssignments indexes server assignments the same way SyncSessions
// matches local sessions: by endpoint first, then by runtime name.
func indexAssignments(assignments []colabapi.ListedAssignment) (map[string]colabapi.ListedAssignment, map[string]colabapi.ListedAssignment) {
	byEndpoint := make(map[string]colabapi.ListedAssignment, len(assignments))
	byRuntime := make(map[string]colabapi.ListedAssignment, len(assignments))
	for _, a := range assignments {
		byEndpoint[a.Endpoint] = a
		if a.RuntimeName != "" {
			byRuntime[a.RuntimeName] = a
		}
	}
	return byEndpoint, byRuntime
}

// withServerShape returns a display-only copy of s carrying the shape the
// server actually assigned. When there is no matching assignment the local
// value is kept. It never writes to the store, so the on-disk schema and
// values are unchanged.
func withServerShape(s state.Session, byEndpoint, byRuntime map[string]colabapi.ListedAssignment) state.Session {
	a, ok := byEndpoint[s.Endpoint]
	if !ok && s.RuntimeName != "" {
		a, ok = byRuntime[s.RuntimeName]
	}
	if ok {
		s.MachineShape = a.MachineShape.String()
	}
	return s
}

// verifyStopped re-reads GET /tun/m/assignments once and reports any stopped
// session that is still present. A survivor maps to exit code 1.
func verifyStopped(ctx context.Context, d *app.Deps, stopped []state.Session) error {
	client, err := d.ColabClient(ctx)
	if err != nil {
		return err
	}
	assignments, err := client.List(ctx)
	if err != nil {
		return err
	}
	for _, a := range assignments {
		for _, s := range stopped {
			if state.MatchAssignment(s, a.Endpoint, a.RuntimeName) {
				return exitf(1, "[colab] Session '%s' still assigned at %s.", s.Name, a.Endpoint)
			}
		}
	}
	return nil
}

// stopOne terminates one session: kernel (best effort), backend, local state.
func stopOne(ctx context.Context, d *app.Deps, name string, s state.Session, quiet bool) error {
	if !quiet {
		fmt.Fprintf(d.Stderr, "[colab] Stopping session '%s'...\n", name)
	}
	if s.KernelID != "" {
		if kernel, err := d.RuntimeFor(ctx, &s); err == nil {
			_ = kernel.Shutdown(ctx)
			_ = kernel.Close()
		}
	}
	client, err := d.ColabClient(ctx)
	if err == nil {
		err = client.UnassignRuntime(ctx, s.Endpoint, s.RuntimeName)
	}
	// Local state is always removed, even when the backend is already gone.
	_ = d.Store.Delete(name)
	writeEvent(d, name, "session_terminated", map[string]any{"reason": "user_requested"})
	return err
}

func stopCommand() *cobra.Command {
	var name string
	var all bool
	var verify bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a session, or all sessions with --all",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			if all {
				local, _, err := d.SyncSessions(cmd.Context())
				if err != nil {
					return err
				}
				if len(local) == 0 {
					fmt.Fprintln(d.Stderr, "[colab] No active sessions.")
					return nil
				}
				names := make([]string, 0, len(local))
				for n := range local {
					names = append(names, n)
				}
				sort.Strings(names)
				if !assumeYes(cmd) {
					if !platform.IsStdinTTY() {
						return usagef("[colab] Refusing to stop all sessions: stdin is not a TTY. " +
							"Re-run with --yes (or set COLAB_ASSUME_YES=1) to confirm.")
					}
					fmt.Fprintf(d.Stderr, "[colab] Stop %d session(s)? [y/N] ", len(local))
					var answer string
					_, _ = fmt.Fscanln(d.Stdin, &answer)
					answer = strings.ToLower(strings.TrimSpace(answer))
					if answer != "y" && answer != "yes" {
						fmt.Fprintln(d.Stderr, "[colab] Aborted.")
						return nil
					}
				}
				var stoppedCount int
				var failed []string
				for _, n := range names {
					if err := stopOne(cmd.Context(), d, n, local[n], false); err != nil {
						fmt.Fprintf(d.Stderr, "[colab] Failed to stop session '%s': %s\n", n, logx.Redact(err.Error()))
						failed = append(failed, n)
						continue
					}
					stoppedCount++
					fmt.Fprintf(d.Stderr, "[colab] Stopped session '%s'.\n", n)
				}
				fmt.Fprintf(d.Stderr, "[colab] Stopped %d session(s).\n", stoppedCount)
				if len(failed) > 0 {
					return exitf(1, "[colab] Failed to stop session(s): %s.", strings.Join(failed, ", "))
				}
				if verify {
					stopped := make([]state.Session, 0, len(local))
					for _, n := range names {
						stopped = append(stopped, local[n])
					}
					if err := verifyStopped(cmd.Context(), d, stopped); err != nil {
						return err
					}
					fmt.Fprintf(d.Stderr, "[colab] Verified: all %d session(s) are no longer assigned.\n", len(local))
				}
				return nil
			}
			if name == "" {
				s, err := d.ResolveSession(cmd.Context(), "")
				if err != nil {
					return err
				}
				name = s.Name
			}
			s, err := d.Store.Get(name)
			if err != nil {
				if errors.Is(err, state.ErrNotFound) {
					fmt.Fprintf(d.Stderr, "[colab] Session '%s' not found.\n", name)
					return nil
				}
				return err
			}
			if err := stopOne(cmd.Context(), d, name, *s, false); err != nil {
				return err
			}
			fmt.Fprintln(d.Stderr, "[colab] Session terminated.")
			if verify {
				if err := verifyStopped(cmd.Context(), d, []state.Session{*s}); err != nil {
					return err
				}
				fmt.Fprintf(d.Stderr, "[colab] Verified: session '%s' is no longer assigned.\n", name)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().BoolVar(&all, "all", false, "Stop all active sessions")
	cmd.Flags().BoolVar(&verify, "verify", false, "Verify after stopping that no assignment remains")
	return cmd
}

func restartKernelCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "restart-kernel",
		Short: "Restart a session's kernel",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			s, err := d.ResolveSession(cmd.Context(), name)
			if err != nil {
				return sessionErr(name, err)
			}
			kernel, err := d.RuntimeFor(cmd.Context(), s)
			if err != nil {
				return pruneLost(d, s.Name, err)
			}
			defer func() { _ = kernel.Close() }()
			if err := kernel.Restart(cmd.Context()); err != nil {
				return pruneLost(d, s.Name, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	return cmd
}

func registerSessionCommands(root *cobra.Command) {
	root.AddCommand(newSessionCommand())
	root.AddCommand(sessionsCommand())
	root.AddCommand(restartKernelCommand())
	root.AddCommand(statusCommand())
	root.AddCommand(stopCommand())
}
