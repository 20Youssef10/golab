package cli

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

// Backlog Tier 1, item 5: `new --wait-capacity DURATION` retries ONLY the
// typed capacity_unavailable failure, with a bounded backoff. Any other error
// must return immediately with zero further attempts. These tests inject the
// clock/backoff seams so the bound is deterministic (no real sleeping).

func withCapacitySeams(t *testing.T, base time.Duration, sleep func(context.Context, time.Duration) error) *atomic.Int64 {
	t.Helper()
	origNow, origBackoff, origSleep := capacityWaitNow, capacityWaitBackoff, capacityWaitSleep
	current := time.Unix(1_700_000_000, 0).UTC()
	calls := &atomic.Int64{}
	capacityWaitNow = func() time.Time { return current }
	capacityWaitBackoff = base
	capacityWaitSleep = func(ctx context.Context, d time.Duration) error {
		calls.Add(1)
		current = current.Add(d)
		return sleep(ctx, d)
	}
	t.Cleanup(func() {
		capacityWaitNow, capacityWaitBackoff, capacityWaitSleep = origNow, origBackoff, origSleep
	})
	return calls
}

func TestWaitCapacityRetriesCapacityErrorThenSucceeds(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	calls := withCapacitySeams(t, 5*time.Millisecond, func(context.Context, time.Duration) error { return nil })

	// First POST is a capacity refusal; the second reaches the normal handler
	// and allocates. GET-then-POST XSRF stays intact on both attempts.
	h.ctrl.SetFault(fakecolab.Fault{
		Method: http.MethodPost, PathContains: "/assign",
		Status: http.StatusServiceUnavailable, Body: `{"error":"no capacity"}`, Times: 1,
	})

	s, err := createSessionWithCapacityWait(context.Background(), h.deps, "waited", runtimeOptions{}, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "waited", s.Name)
	require.EqualValues(t, 1, calls.Load(), "one retry means one backoff sleep")
}

func TestWaitCapacityZeroFurtherRetriesOnNonCapacityError(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	calls := withCapacitySeams(t, 5*time.Millisecond, func(context.Context, time.Duration) error { return nil })

	h.ctrl.SetFault(fakecolab.Fault{
		Method: http.MethodPost, PathContains: "/assign",
		Status: http.StatusInternalServerError, Body: `{"error":"boom"}`, Times: -1,
	})

	_, err := createSessionWithCapacityWait(context.Background(), h.deps, "failed", runtimeOptions{}, time.Minute)
	require.Error(t, err)
	require.EqualValues(t, 0, calls.Load(), "non-capacity errors must not back off")
	var posts int
	for _, r := range h.ctrl.Requests() {
		if r.Method == http.MethodPost && strings.Contains(r.Path, "/assign") {
			posts++
		}
	}
	require.Equal(t, 1, posts, "non-capacity errors must not be retried")
	require.False(t, errors.Is(err, colabapi.ErrCapacity))
}

func TestWaitCapacityIsBounded(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	calls := withCapacitySeams(t, 30*time.Millisecond, func(context.Context, time.Duration) error { return nil })

	h.ctrl.SetFault(fakecolab.Fault{
		Method: http.MethodPost, PathContains: "/assign",
		Status: http.StatusServiceUnavailable, Body: `{"error":"no capacity"}`, Times: -1,
	})

	_, err := createSessionWithCapacityWait(context.Background(), h.deps, "bounded", runtimeOptions{}, 100*time.Millisecond)
	require.Error(t, err)
	require.True(t, errors.Is(err, colabapi.ErrCapacity))
	require.Greater(t, calls.Load(), int64(1))
	require.LessOrEqual(t, calls.Load(), int64(5), "retry loop must stop at the wait deadline")
}

func TestWaitCapacityRespectsContextCancellation(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	calls := withCapacitySeams(t, time.Millisecond, func(ctx context.Context, d time.Duration) error {
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: no retry may be attempted

	h.ctrl.SetFault(fakecolab.Fault{
		Method: http.MethodPost, PathContains: "/assign",
		Status: http.StatusServiceUnavailable, Body: `{"error":"no capacity"}`, Times: -1,
	})

	_, err := createSessionWithCapacityWait(ctx, h.deps, "cancelled", runtimeOptions{}, time.Minute)
	require.Error(t, err)
	require.EqualValues(t, 0, calls.Load(), "a cancelled context must not back off")
}
