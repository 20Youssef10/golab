package cli

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/colabapi"
)

// runCUSampleInterval bounds in-run ccu-info polling. The loop is driven by a
// context and a stop channel, never by a daemon or a process-wide timer.
const runCUSampleInterval = 60 * time.Second

type cuSampler func(context.Context) (*colabapi.Consumption, error)

type cuMonitorConfig struct {
	Interval time.Duration
	MaxCU    float64
	Enforce  bool
	Sample   cuSampler
	Cancel   context.CancelFunc
	Stderr   io.Writer
	Now      func() time.Time
}

// runCUMonitorLoop samples ccu-info immediately and then at most once per
// interval until stopped or cancelled. A breach cancels the run context when
// enforcement is on; without --enforce it is advisory only.
//
// ccu-info is ACCOUNT-level, not per-session: the sampled delta belongs to the
// run window, so a balance at or below the ceiling is attributed to this run
// rather than to the ephemeral session's own metering.
func runCUMonitorLoop(ctx context.Context, stop <-chan struct{}, cfg cuMonitorConfig) <-chan struct{} {
	done := make(chan struct{})
	interval := cfg.Interval
	if interval <= 0 {
		interval = runCUSampleInterval
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			u, err := cfg.Sample(ctx)
			if err == nil && u != nil && cfg.MaxCU > 0 && u.PaidComputeUnitsBalance <= cfg.MaxCU {
				observedAt := ""
				if cfg.Now != nil {
					observedAt = cfg.Now().UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(cfg.Stderr,
					"[colab] compute unit ceiling exceeded at %s: balance %.2f compute units <= %.2f compute units (account-level during run window).\n",
					observedAt, u.PaidComputeUnitsBalance, cfg.MaxCU)
				if cfg.Enforce && cfg.Cancel != nil {
					cfg.Cancel()
				}
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

// runCUMonitor joins the sampling goroutine so a run can never leave one behind.
type runCUMonitor struct {
	stop     chan struct{}
	done     <-chan struct{}
	stopOnce sync.Once
}

func (m *runCUMonitor) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		close(m.stop)
		<-m.done
	})
}

// startRunCUMonitor wires the in-run guard. It returns a derived context used
// for execution; the sampler keeps the parent context so cancelling the run
// does not abort the in-flight ccu-info fetch. No flags means no monitor.
func startRunCUMonitor(ctx context.Context, d *app.Deps, cmd *cobra.Command, maxCU float64, enforce bool) (context.Context, *runCUMonitor, error) {
	if maxCU <= 0 || cmd != nil && explainRequested(cmd) {
		return ctx, nil, nil
	}
	client, err := d.ColabClient(ctx)
	if err != nil {
		return ctx, nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	stop := make(chan struct{})
	cfg := cuMonitorConfig{
		Interval: runCUSampleInterval,
		MaxCU:    maxCU,
		Enforce:  enforce,
		Sample: func(c context.Context) (*colabapi.Consumption, error) {
			return sampleCCU(c, d, client, "guard")
		},
		Cancel: cancel,
		Stderr: d.Stderr,
		Now:    time.Now,
	}
	done := runCUMonitorLoop(ctx, stop, cfg)
	return runCtx, &runCUMonitor{stop: stop, done: done}, nil
}
