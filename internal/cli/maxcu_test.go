package cli

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/state"
)

// Backlog Tier 1, item 4: extend the compute-unit guard from allocation time
// into a running `run`. ccu-info is ACCOUNT-level, so deltas are attributed to
// the run window, not to the individual session. Sampling is bounded (60s in
// production) and context-cancellable, and a breach interrupts + stops the run.

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestCUMonitorEnforcedBreachCancels(t *testing.T) {
	var canceled atomic.Bool
	var stderr syncBuffer
	stop := make(chan struct{})
	cfg := cuMonitorConfig{
		Interval: time.Hour, MaxCU: 20, Enforce: true,
		Sample: func(context.Context) (*colabapi.Consumption, error) {
			return &colabapi.Consumption{PaidComputeUnitsBalance: 10, ConsumptionRateHourly: 5}, nil
		},
		Cancel: func() { canceled.Store(true) }, Stderr: &stderr, Now: time.Now,
	}
	done := runCUMonitorLoop(context.Background(), stop, cfg)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not exit after an enforced breach")
	}
	require.True(t, canceled.Load())
	require.Contains(t, stderr.String(), "compute units")
	require.Contains(t, stderr.String(), "account-level")
}

func TestCUMonitorAdvisoryOnlyWarns(t *testing.T) {
	var canceled atomic.Bool
	var stderr syncBuffer
	stop := make(chan struct{})
	cfg := cuMonitorConfig{
		Interval: time.Hour, MaxCU: 20, Enforce: false,
		Sample: func(context.Context) (*colabapi.Consumption, error) {
			return &colabapi.Consumption{PaidComputeUnitsBalance: 10}, nil
		},
		Cancel: func() { canceled.Store(true) }, Stderr: &stderr, Now: time.Now,
	}
	done := runCUMonitorLoop(context.Background(), stop, cfg)
	waitForText(t, &stderr, "exceeded") // advisory warning is emitted once
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor ignored its stop channel")
	}
	require.False(t, canceled.Load(), "without --enforce the run must not be interrupted")
}

func TestCUMonitorNoBreachDoesNotCancel(t *testing.T) {
	var canceled atomic.Bool
	var stderr syncBuffer
	stop := make(chan struct{})
	cfg := cuMonitorConfig{
		Interval: time.Hour, MaxCU: 20, Enforce: true,
		Sample: func(context.Context) (*colabapi.Consumption, error) {
			return &colabapi.Consumption{PaidComputeUnitsBalance: 35}, nil
		},
		Cancel: func() { canceled.Store(true) }, Stderr: &stderr, Now: time.Now,
	}
	done := runCUMonitorLoop(context.Background(), stop, cfg)
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not exit on stop")
	}
	require.False(t, canceled.Load())
	require.Empty(t, stderr.String())
}

func TestRunMaxCUEnforcedAbortsAndStops(t *testing.T) {
	h := newHarness(t)
	h.ctrl.SetConsumption(5, 1, 1) // below the 20 CU floor at first sample
	script := filepath.Join(t.TempDir(), "cap.py")
	require.NoError(t, os.WriteFile(script, []byte("print('never')\n"), 0o600))

	code := h.run(t, "", "run", "--max-cu", "20", "--enforce", "-s", "capped", script)
	require.NotEqual(t, 0, code, "breach must abort the run")
	require.Contains(t, h.stderr.String(), "compute unit")
	var unassigned bool
	for _, r := range h.ctrl.Requests() {
		if r.Method == http.MethodPost && strings.Contains(r.Path, "/unassign/") {
			unassigned = true
		}
	}
	require.True(t, unassigned, "breach must stop the runtime")
	_, err := h.deps.Store.Get("capped")
	require.ErrorIs(t, err, state.ErrNotFound)
}

func waitForText(t *testing.T, b *syncBuffer, needle string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), needle) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("did not observe %q in %q", needle, b.String())
}
