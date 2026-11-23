//go:build !windows

package sshx

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Ports docs/04 §4 / ADR-005: window resize must be forwarded via SIGWINCH on
// Unix. The size source is injected so no real TTY is needed in CI.
func TestResizeViaSIGWINCH(t *testing.T) {
	var w atomic.Int32
	w.Store(100)
	withRawHooks(t, rawHooks{
		makeRaw: func(int) (*rawState, error) { return &rawState{}, nil },
		restore: func(int, *rawState) error { return nil },
		getSize: func(int) (int, int, error) { return int(w.Load()), 24, nil },
	}, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sizes := make(chan windowSize, 1)
		go startResizeWatcher(ctx, 0, sizes)

		p, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		w.Store(132)
		// The watcher installs signal.Notify asynchronously. Repeat the signal
		// so the test cannot race with that setup under a loaded CI runner.
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err := p.Signal(syscall.SIGWINCH); err != nil {
				t.Skipf("cannot send SIGWINCH: %v", err)
			}
			select {
			case sz := <-sizes:
				if sz.width != 132 || sz.height != 24 {
					t.Fatalf("resize = %+v; want 132x24", sz)
				}
				return
			case <-ticker.C:
			case <-deadline.C:
				t.Fatal("SIGWINCH resize not delivered")
			}
		}
	})
}
