package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

// Upstream analogue: docs/05 `wait` parity + ADR-001 (zero allocation). These
// tests pin bounded polling over the existing readiness checks and the typed
// timeout error before the command is wired up.

func waitSession(t *testing.T, h *testHarness, name string) *state.Session {
	t.Helper()
	s, err := h.deps.Store.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWaitKernelModeUsesExistingAttachCheck(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	before := len(h.ctrl.Assignments())
	s := waitSession(t, h, "s1")

	if err := waitFor(context.Background(), h.deps, s, "kernel", 2*time.Second, 10*time.Millisecond); err != nil {
		t.Fatalf("wait kernel = %v", err)
	}
	if got := len(h.ctrl.Assignments()); got != before {
		t.Fatalf("wait allocated %d assignment(s), want 0", got-before)
	}
}

func TestWaitIdleModeUsesExistingExecCheck(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	before := len(h.ctrl.Assignments())
	s := waitSession(t, h, "s1")

	if err := waitFor(context.Background(), h.deps, s, "idle", 2*time.Second, 10*time.Millisecond); err != nil {
		t.Fatalf("wait idle = %v", err)
	}
	if len(h.kernel.Executions()) == 0 {
		t.Fatal("wait idle did not use the exec readiness check")
	}
	if got := len(h.ctrl.Assignments()); got != before {
		t.Fatalf("wait allocated %d assignment(s), want 0", got-before)
	}
}

func TestWaitReadyModeUsesExistingStatusCheck(t *testing.T) {
	h := newHarness(t)
	h.assignAndStore(t, "s1", colabapi.AssignRequest{Variant: colabapi.VariantDefault, Accelerator: colabapi.AccelNone, Shape: colabapi.ShapeStandard})
	before := len(h.ctrl.Assignments())
	s := waitSession(t, h, "s1")

	if err := waitFor(context.Background(), h.deps, s, "ready", 2*time.Second, 10*time.Millisecond); err != nil {
		t.Fatalf("wait ready = %v", err)
	}
	if got := len(h.ctrl.Assignments()); got != before {
		t.Fatalf("wait allocated %d assignment(s), want 0", got-before)
	}
}

func TestWaitPollsUntilReady(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	s := waitSession(t, h, "s1")
	orig := waitProbeFn
	calls := 0
	waitProbeFn = func(context.Context, *app.Deps, *state.Session, string) (bool, error) {
		calls++
		return calls >= 3, nil
	}
	t.Cleanup(func() { waitProbeFn = orig })

	if err := waitFor(context.Background(), h.deps, s, "kernel", 2*time.Second, 1*time.Millisecond); err != nil {
		t.Fatalf("waitFor = %v", err)
	}
	if calls != 3 {
		t.Fatalf("probe calls = %d, want 3", calls)
	}
}

func TestWaitTimeoutIsTypedAndBounded(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	s := waitSession(t, h, "s1")
	orig := waitProbeFn
	waitProbeFn = func(context.Context, *app.Deps, *state.Session, string) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() { waitProbeFn = orig })

	start := time.Now()
	err := waitFor(context.Background(), h.deps, s, "ready", 60*time.Millisecond, 5*time.Millisecond)
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("waitFor error = %v, want ErrWaitTimeout", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("wait exceeded its bound")
	}
}

func TestWaitContextCancelStopsImmediately(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	s := waitSession(t, h, "s1")
	orig := waitProbeFn
	waitProbeFn = func(context.Context, *app.Deps, *state.Session, string) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() { waitProbeFn = orig })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitFor(ctx, h.deps, s, "ready", time.Hour, time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitFor error = %v, want context.Canceled", err)
	}
}

func TestWaitCommandValidatesMode(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	if code := h.run(t, "", "wait", "-s", "s1", "--for", "bogus"); code != 2 {
		t.Fatalf("wait --for bogus exit = %d, want 2; stderr=%s", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "kernel") {
		t.Fatalf("usage error must list valid modes: %s", h.stderr.String())
	}
}
