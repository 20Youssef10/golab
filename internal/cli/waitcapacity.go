package cli

import (
	"context"
	"errors"
	"time"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/state"
)

// Tier 1 item 5: `new --wait-capacity DURATION`. These are test seams with
// production defaults, mirroring the CLI command/kill hooks in this package:
// tests advance a fake clock and skip the real backoff sleep.
var (
	capacityWaitNow     = time.Now
	capacityWaitBackoff = colabapi.DefaultBackoff
	capacityWaitSleep   = func(ctx context.Context, d time.Duration) error {
		if d <= 0 {
			return ctx.Err()
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
)

// isCapacityUnavailable matches the typed capacity failure only. Both the
// 503 capacity sentinel (which carries ReasonCapacityUnavailable) and the
// 412 precondition refusal are capacity signals; every other error returns
// immediately with zero further attempts.
func isCapacityUnavailable(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, colabapi.ErrCapacity) ||
		errors.Is(err, colabapi.ErrTooManyAssignments) ||
		colabapi.ReasonOf(err) == colabapi.ReasonCapacityUnavailable
}

// createSessionWithCapacityWait wraps createSession with a bounded,
// context-cancellable retry loop for capacity refusals. It reuses the control
// plane's own GET-then-POST XSRF flow on every attempt (a refusal never
// mutates state), and it never retries a non-capacity error.
func createSessionWithCapacityWait(ctx context.Context, d *app.Deps, name string, opts runtimeOptions, wait time.Duration) (state.Session, error) {
	deadline := capacityWaitNow().Add(wait)
	lastErr := error(nil)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := ctx.Err(); err != nil {
				return state.Session{}, err
			}
			now := capacityWaitNow()
			if !now.Before(deadline) {
				return state.Session{}, lastErr
			}
			remaining := deadline.Sub(now)
			delay := capacityWaitBackoff
			for i := 0; i < attempt && delay > 0 && delay < remaining/2; i++ {
				delay *= 2
			}
			if delay > remaining {
				delay = remaining
			}
			if err := capacityWaitSleep(ctx, delay); err != nil {
				return state.Session{}, err
			}
		}
		s, err := createSession(ctx, d, name, opts)
		if err == nil || wait <= 0 || !isCapacityUnavailable(err) {
			return s, err
		}
		lastErr = err
	}
}
