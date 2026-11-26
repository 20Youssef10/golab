package cli

// TIER 3 ADDITION (wait). Bounded polling over the EXISTING readiness checks:
// kernel attach (RuntimeFor), exec-to-idle (Execute), and session status
// (control-plane List). It allocates nothing and leaves no process behind.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
)

// ErrWaitTimeout is the typed cause of a readiness wait that exhausted its
// bound. Callers use errors.Is, never message matching.
var ErrWaitTimeout = errors.New("wait: timeout")

type waitTimeoutError struct {
	mode    string
	timeout time.Duration
}

func (e *waitTimeoutError) Error() string {
	return fmt.Sprintf("wait: timed out after %s waiting for %s", e.timeout, e.mode)
}

func (e *waitTimeoutError) Unwrap() error { return ErrWaitTimeout }

const (
	waitModeKernel = "kernel"
	waitModeIdle   = "idle"
	waitModeReady  = "ready"
)

// waitProbeFn is a test seam. Production uses defaultWaitProbe.
var waitProbeFn = defaultWaitProbe

// defaultWaitProbe runs one readiness check. Each mode reuses an existing
// primitive and performs no allocation:
//   - ready:  control-plane assignment status (status check),
//   - kernel: data-plane kernel attach (attach check),
//   - idle:   exec-to-idle of a harmless probe (exec readiness check).
func defaultWaitProbe(ctx context.Context, d *app.Deps, s *state.Session, mode string) (bool, error) {
	switch mode {
	case waitModeReady:
		client, err := d.ColabClient(ctx)
		if err != nil {
			return false, err
		}
		assignments, err := client.List(ctx)
		if err != nil {
			return false, err
		}
		for _, a := range assignments {
			if state.MatchAssignment(*s, a.Endpoint, a.RuntimeName) {
				return true, nil
			}
		}
		return false, nil
	case waitModeKernel:
		k, err := d.RuntimeFor(ctx, s)
		if err != nil {
			return false, nil // not ready yet; bounded polling retries
		}
		_ = k.Close()
		return true, nil
	case waitModeIdle:
		k, err := d.RuntimeFor(ctx, s)
		if err != nil {
			return false, nil
		}
		defer k.Close()
		_, err = k.Execute(ctx, "pass\n", runtime.ExecOptions{IdleTimeout: 5 * time.Second})
		if err != nil {
			return false, nil
		}
		return true, nil
	default:
		return false, usagef("[colab] wait: --for must be kernel, idle or ready")
	}
}

// waitFor polls at most until timeout. Both the deadline and the context bound
// the loop; a timeout returns the typed ErrWaitTimeout cause.
func waitFor(ctx context.Context, d *app.Deps, s *state.Session, mode string, timeout, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := waitProbeFn(ctx, d, s, mode)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return &waitTimeoutError{mode: mode, timeout: timeout}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// waitCommand exposes the bounded poll as `golab wait -s NAME --for MODE`.
func waitCommand() *cobra.Command {
	var name, mode string
	var timeout float64
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Wait for a session to become kernel/idle/ready (bounded, no allocation)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			switch mode {
			case waitModeKernel, waitModeIdle, waitModeReady:
			default:
				return usagef("[colab] wait: --for must be kernel, idle or ready (got %q).", mode)
			}
			if timeout <= 0 {
				return usagef("[colab] wait: --timeout must be positive (seconds).")
			}
			s, err := d.ResolveSession(cmd.Context(), name)
			if err != nil {
				return sessionErr(name, err)
			}
			budget := time.Duration(timeout * float64(time.Second))
			if err := waitFor(cmd.Context(), d, s, mode, budget, 500*time.Millisecond); err != nil {
				return exitCausef(1, err, "[colab] %s", err)
			}
			fmt.Fprintf(d.Stdout, "%s ready (for=%s)\n", s.Name, mode)
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().StringVar(&mode, "for", waitModeReady, "Wait for: kernel, idle or ready")
	cmd.Flags().Float64Var(&timeout, "timeout", 300, "Maximum seconds to wait")
	return cmd
}
