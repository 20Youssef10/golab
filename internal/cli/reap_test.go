package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/state"
)

// Backlog Tier 1, item 1: reap diffs server assignments against local
// sessions, unassigns only orphans, and prunes stale local records. --dry-run
// makes zero mutations; a non-TTY invocation must pass --yes.

func reapFixture(t *testing.T) (*testHarness, state.Session, time.Time) {
	t.Helper()
	h := newHarness(t)
	setStreams(h)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, err := h.client.Assign(ctx, colabapi.AssignRequest{NotebookHash: mustNBH(t)})
		require.NoError(t, err)
	}
	assignments, err := h.client.List(ctx)
	require.NoError(t, err)
	require.Len(t, assignments, 2)

	// One server assignment has a local record; the other is an orphan.
	s := state.Session{
		Name: "kept", Endpoint: assignments[0].Endpoint,
		RuntimeName: assignments[0].RuntimeName,
		URL:         "https://runtime.example", Token: assignments[0].RuntimeProxyInfo.Token,
		TokenExpiresAt: func() *time.Time { v := time.Now().Add(time.Hour); return &v }(),
	}
	require.NoError(t, h.deps.Store.Put(s))

	// A stale local record points at nothing on the server.
	old := time.Now().Add(-3 * time.Hour)
	require.NoError(t, h.deps.Store.Put(state.Session{
		Name: "stale", Endpoint: "gone-endpoint",
		LastExecution: &state.LastExecution{Kind: "run", At: reapWallClock(t, 3*time.Hour)},
	}))
	return h, s, old
}

// reapWallClock formats a last_execution timestamp that reapRecordTime reads
// as exactly `ago` before now. Production intentionally takes the later of the
// local and UTC interpretations of a timezone-less wall clock; on runners with
// a negative UTC offset (observed on windows-11-arm) a naive UTC string is
// read as a future time, so a genuinely stale record is never pruned. This
// helper compensates for that offset, keeping the 2h threshold test identical
// on every platform without weakening the "never prune recent records" rule.
func reapWallClock(t *testing.T, ago time.Duration) string {
	t.Helper()
	now := time.Now()
	_, offset := now.In(time.Local).Zone()
	wall := now.UTC().Add(-ago)
	if offset < 0 {
		wall = wall.Add(time.Duration(offset) * time.Second)
	}
	return wall.Format("2006-01-02 15:04:05")
}

func mustNBH(t *testing.T) colabapi.NotebookHash {
	t.Helper()
	h, err := colabapi.RandomNotebookHash()
	require.NoError(t, err)
	return h
}

func countUnassigns(h *testHarness) int {
	n := 0
	for _, r := range h.ctrl.Requests() {
		if r.Method == http.MethodPost && strings.Contains(r.Path, "/unassign/") {
			n++
		}
	}
	return n
}

func TestReapDryRunMakesNoMutations(t *testing.T) {
	h, _, _ := reapFixture(t)
	code := h.run(t, "", "reap", "--older-than", "0", "--dry-run", "--yes", "--json")
	require.Equal(t, 0, code, h.stderr.String())
	require.Equal(t, 0, countUnassigns(h), "dry-run must issue zero unassigns")
	_, err := h.deps.Store.Get("stale")
	require.NoError(t, err, "dry-run must not prune local records")

	var out struct {
		Orphans []struct {
			Endpoint string `json:"endpoint"`
		} `json:"orphans"`
		DryRun bool `json:"dry_run"`
	}
	require.NoError(t, json.Unmarshal([]byte(h.stdout.String()), &out))
	require.True(t, out.DryRun)
	require.Len(t, out.Orphans, 1)
	require.Contains(t, h.stderr.String(), "dry run")
}

func TestReapUnassignsOnlyOrphans(t *testing.T) {
	h, kept, _ := reapFixture(t)
	code := h.run(t, "", "reap", "--older-than", "0", "--yes", "--json")
	require.Equal(t, 0, code, h.stderr.String())
	require.Equal(t, 1, countUnassigns(h), "exactly the orphan must be unassigned")

	// The session with a local record is never unassigned.
	assignments, err := h.client.List(context.Background())
	require.NoError(t, err)
	var keptFound bool
	for _, a := range assignments {
		if state.MatchAssignment(kept, a.Endpoint, a.RuntimeName) {
			keptFound = true
		}
	}
	require.True(t, keptFound, "a session with a local record must survive reap")

	_, err = h.deps.Store.Get("stale")
	require.ErrorIs(t, err, state.ErrNotFound, "stale local record should be pruned")
	require.Contains(t, h.stderr.String(), "Reaped")
}

func TestReapRequiresYesWithoutTTY(t *testing.T) {
	h, _, _ := reapFixture(t)
	code := h.run(t, "", "reap", "--older-than", "0")
	require.Equal(t, 2, code)
	require.Contains(t, h.stderr.String(), "--yes")
	require.Equal(t, 0, countUnassigns(h))
}

func TestReapOlderThanSkipsRecentLocalRecords(t *testing.T) {
	h, _, _ := reapFixture(t)
	// Default --older-than 2h: the 3h-old stale record is pruned. A 30m-old
	// stale record must be left alone even though it has no assignment.
	require.NoError(t, h.deps.Store.Put(state.Session{
		Name: "recent", Endpoint: "recent-gone",
		LastExecution: &state.LastExecution{Kind: "run", At: reapWallClock(t, 30*time.Minute)},
	}))

	code := h.run(t, "", "reap", "--yes")
	require.Equal(t, 0, code, h.stderr.String())
	_, err := h.deps.Store.Get("recent")
	require.NoError(t, err, "recent stale record must not be pruned")
	_, err = h.deps.Store.Get("stale")
	require.ErrorIs(t, err, state.ErrNotFound)
}
