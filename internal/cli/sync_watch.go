package cli

// TIER 3 ADDITION (sync --watch). A polling loop over the EXISTING sync engine
// (runSync via sync's RunE). No fsnotify dependency, no daemon: the loop is
// context-cancellable and iteration-bounded, and exits when done.

import (
	"context"
	"time"
)

// syncWatch runs step at most max times, waiting interval between iterations.
// It is a pure helper so cancellation and bounding are directly testable.
func syncWatch(ctx context.Context, interval time.Duration, max int, step func() error) error {
	if interval <= 0 {
		interval = time.Second
	}
	if max <= 0 {
		max = 1
	}
	for i := 0; i < max; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step(); err != nil {
			return err
		}
		if i == max-1 {
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
