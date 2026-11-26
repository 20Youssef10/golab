package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/history"
)

// The polling loop itself is context-cancellable, ticks on a timer (never a
// busy spin) and is bounded by an explicit tick budget.
func TestWatchLoopTicksUntilCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := 0
	err := watchLoop(ctx, time.Millisecond, 100, func(context.Context) error {
		ticks++
		if ticks == 2 {
			cancel()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 2 {
		t.Fatalf("ticks = %d, want 2", ticks)
	}
}

func TestWatchLoopHonoursTickBudget(t *testing.T) {
	ticks := 0
	err := watchLoop(context.Background(), time.Millisecond, 3, func(context.Context) error {
		ticks++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 3 {
		t.Fatalf("ticks = %d, want 3", ticks)
	}
}

func TestWatchLoopPropagatesTickError(t *testing.T) {
	sentinel := errors.New("tick failed")
	calls := 0
	err := watchLoop(context.Background(), time.Millisecond, 10, func(context.Context) error {
		calls++
		if calls == 2 {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// Command-level tests replace the loop with a bounded fake so no test waits
// on a real ticker.
func fakeWatchLoop(t *testing.T) (*int, *time.Duration) {
	t.Helper()
	orig := watchLoopFn
	calls, interval := 0, time.Duration(0)
	watchLoopFn = func(ctx context.Context, d time.Duration, _ int, tick func(context.Context) error) error {
		calls++
		interval = d
		return tick(ctx)
	}
	t.Cleanup(func() { watchLoopFn = orig })
	return &calls, &interval
}

func TestStatusWatchInvokesLoop(t *testing.T) {
	calls, interval := fakeWatchLoop(t)
	h := newHarness(t)
	h.assignAndStore(t, "s1", colabapi.AssignRequest{})

	if code := h.run(t, "", "status", "--watch", "--interval", "1ms", "-s", "s1"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if *calls != 1 {
		t.Fatalf("watch loop calls = %d, want 1", *calls)
	}
	if *interval != time.Millisecond {
		t.Fatalf("interval = %v, want 1ms", *interval)
	}
	if !strings.Contains(h.stdout.String(), "[s1]") {
		t.Fatalf("status was not printed: %q", h.stdout.String())
	}
}

func TestStatusWithoutWatchDoesNotLoop(t *testing.T) {
	calls, _ := fakeWatchLoop(t)
	h := newHarness(t)
	h.assignAndStore(t, "s1", colabapi.AssignRequest{})
	if code := h.run(t, "", "status", "-s", "s1"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if *calls != 0 {
		t.Fatalf("watch loop called without --watch: %d", *calls)
	}
}

func TestStatusWatchRejectsBadInterval(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"status", "--watch", "--interval", "not-a-duration", "-s", "s1"},
		{"status", "--watch", "--interval", "-1s", "-s", "s1"},
	} {
		if code := h.run(t, "", args...); code != 2 {
			t.Fatalf("%v exit=%d, want 2; stderr=%q", args, code, h.stderr.String())
		}
	}
}

func TestLogFollowInvokesLoop(t *testing.T) {
	calls, _ := fakeWatchLoop(t)
	h := newHarness(t)
	h.addHistory(t, "s1", history.Event{EventType: history.EventSessionCreated})

	if code := h.run(t, "", "log", "-s", "s1", "-f"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if *calls != 1 {
		t.Fatalf("follow loop calls = %d, want 1", *calls)
	}
	if !strings.Contains(h.stdout.String(), "session_created") {
		t.Fatalf("history event not printed: %q", h.stdout.String())
	}
}

func TestLogWithoutFollowDoesNotLoop(t *testing.T) {
	calls, _ := fakeWatchLoop(t)
	h := newHarness(t)
	h.addHistory(t, "s1", history.Event{EventType: history.EventSessionCreated})
	if code := h.run(t, "", "log", "-s", "s1"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if *calls != 0 {
		t.Fatalf("follow loop called without -f: %d", *calls)
	}
}

func TestLogFollowRejectsExport(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "log", "-s", "s1", "-f", "-o", "out.txt"); code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, h.stderr.String())
	}
}
