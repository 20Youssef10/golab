package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Upstream analogue: docs/05 sync parity + ADR-001 (no fsnotify, bounded
// polling). Test-first: the watch loop must stop on context cancel, honour the
// iteration bound, and propagate a step failure.

func TestSyncWatchStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	step := func() error {
		calls++
		if calls == 2 {
			cancel()
		}
		return nil
	}
	err := syncWatch(ctx, time.Millisecond, 100, step)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("syncWatch error = %v, want context.Canceled", err)
	}
	if calls != 2 {
		t.Fatalf("watch ran %d iterations, want 2", calls)
	}
}

func TestSyncWatchIsBounded(t *testing.T) {
	calls := 0
	step := func() error { calls++; return nil }
	if err := syncWatch(context.Background(), time.Millisecond, 3, step); err != nil {
		t.Fatalf("syncWatch = %v", err)
	}
	if calls != 3 {
		t.Fatalf("watch ran %d iterations, want 3 (bounded)", calls)
	}
}

func TestSyncWatchPropagatesStepError(t *testing.T) {
	boom := errors.New("sync failed")
	calls := 0
	step := func() error { calls++; return boom }
	if err := syncWatch(context.Background(), time.Millisecond, 10, step); !errors.Is(err, boom) {
		t.Fatalf("syncWatch error = %v, want the step error", err)
	}
	if calls != 1 {
		t.Fatalf("watch ran %d iterations after a failure, want 1", calls)
	}
}

func TestSyncWatchCommandRunsBounded(t *testing.T) {
	h := newHarness(t)
	fc := newFakeContents(t)
	h.putSession(t, "s1")
	h.attachContents(t, fc)
	fc.dirs["content/remote"] = nil
	local := filepath.Join(t.TempDir(), "local")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	code := h.run(t, "", "sync", "--watch", "--interval", "1ms", "--iterations", "3",
		"-s", "s1", local, "content/remote")
	if code != 0 {
		t.Fatalf("sync --watch exit = %d; stderr=%s", code, h.stderr.String())
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("sync --watch did not stop at its bound")
	}
}
