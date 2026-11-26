package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

// Backlog Tier 1, item 2: `new --ttl D [--ttl-action=warn|stop]`. The check is
// entirely local (no List call) and happens on every invocation; with `stop`
// the unassign is best effort. No daemon/timer is started.

func withTTLClock(t *testing.T) time.Time {
	t.Helper()
	orig := ttlNow
	now := time.Unix(1_700_000_000, 0).UTC()
	ttlNow = func() time.Time { return now }
	t.Cleanup(func() { ttlNow = orig })
	return now
}

func putExpiringSession(t *testing.T, h *testHarness, name string, expiresAt time.Time, action string) {
	t.Helper()
	s := state.Session{
		Name: name, Endpoint: "endpoint-test", URL: h.kernel.URL(),
		ExpiresAt: &expiresAt, TTLAction: action,
		TokenExpiresAt: func() *time.Time { v := time.Now().Add(time.Hour); return &v }(),
	}
	require.NoError(t, h.deps.Store.Put(s))
}

func TestTTLWarnsExpiredSessionLocally(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	now := withTTLClock(t)
	putExpiringSession(t, h, "train", now.Add(-12*time.Minute), "warn")

	checkExpiredSessions(context.Background(), h.deps)

	require.Contains(t, h.stderr.String(), "session train expired 12m ago")
	for _, r := range h.ctrl.Requests() {
		if r.Method == http.MethodPost && strings.Contains(r.Path, "/unassign") {
			t.Fatalf("warn action issued an unassign: %s %s", r.Method, r.Path)
		}
	}
	_, err := h.deps.Store.Get("train")
	require.NoError(t, err, "warn must keep the local record")
}

func TestTTLStopUnassignsBestEffort(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	now := withTTLClock(t)
	putExpiringSession(t, h, "train", now.Add(-12*time.Minute), "stop")

	checkExpiredSessions(context.Background(), h.deps)

	require.Contains(t, h.stderr.String(), "session train expired 12m ago")
	var unassigned bool
	for _, r := range h.ctrl.Requests() {
		if r.Method == http.MethodPost && strings.Contains(r.Path, "/unassign") {
			unassigned = true
		}
	}
	require.True(t, unassigned, "stop action must unassign the expired runtime")
	_, err := h.deps.Store.Get("train")
	require.ErrorIs(t, err, state.ErrNotFound, "stop must prune the local record")
}

func TestTTLStopIgnoresUnassignFailure(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	now := withTTLClock(t)
	putExpiringSession(t, h, "train", now.Add(-time.Hour), "stop")
	h.ctrl.SetFault(fakecolab.Fault{
		Method: http.MethodGet, PathContains: "/unassign/",
		Status: http.StatusInternalServerError, Body: `{"error":"unavailable"}`, Times: -1,
	})

	// Best effort: an unassign failure must not make the local check fatal.
	checkExpiredSessions(context.Background(), h.deps)

	_, err := h.deps.Store.Get("train")
	require.ErrorIs(t, err, state.ErrNotFound)
}

func TestNewTTLPersistsExpiryAndAction(t *testing.T) {
	h := newHarness(t)
	now := withTTLClock(t)

	code := h.run(t, "", "new", "--ttl", "2h", "--ttl-action=warn", "-s", "train")
	require.Equal(t, 0, code, h.stderr.String())

	s, err := h.deps.Store.Get("train")
	require.NoError(t, err)
	require.NotNil(t, s.ExpiresAt)
	require.Equal(t, now.Add(2*time.Hour), *s.ExpiresAt)
	require.Equal(t, "warn", s.TTLAction)
}

func TestNewRejectsBadTTL(t *testing.T) {
	h := newHarness(t)
	code := h.run(t, "", "new", "--ttl", "nonsense", "-s", "train")
	require.NotEqual(t, 0, code)
	code = h.run(t, "", "new", "--ttl", "1h", "--ttl-action=bogus", "-s", "train")
	require.NotEqual(t, 0, code)
}
