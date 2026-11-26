package cli

import (
	"context"
	"time"
)

// Polling defaults for read-only status/log views [tier5].
const (
	defaultWatchInterval  = 5 * time.Second
	defaultFollowInterval = 1 * time.Second
	// maxWatchTicks is a hard safety bound: even a forgotten cancellation
	// cannot keep a CLI process polling forever.
	maxWatchTicks = 10000
)

// watchLoop calls tick immediately, then once per interval until ctx is done
// or maxTicks calls have been made. Cancellation is a normal exit; a tick
// error is propagated. The timer keeps the loop from ever spinning. [tier5]
func watchLoop(ctx context.Context, interval time.Duration, maxTicks int, tick func(context.Context) error) error {
	if interval <= 0 {
		interval = defaultWatchInterval
	}
	if maxTicks <= 0 {
		maxTicks = maxWatchTicks
	}
	for i := 0; i < maxTicks; i++ {
		if err := ctx.Err(); err != nil {
			return nil // cancellation/deadline ends a read-only poll cleanly
		}
		if err := tick(ctx); err != nil {
			return err
		}
		if i == maxTicks-1 {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
	}
	return nil
}

// watchLoopFn is the seam the status/log commands call; tests replace it with
// a bounded fake so no command test waits on a real ticker. [tier5]
var watchLoopFn = watchLoop
