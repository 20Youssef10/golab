package cost

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Backlog Tier 1, item 3: a bounded, private JSONL ledger of ccu-info samples.
// These tests pin the 0600 mode, the size bound and the strictly CU-only
// report contract (no invented currency).

func TestLedgerAppendIsPrivateJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccu.jsonl")
	l := New(path)
	clock := time.Unix(1_700_000_000, 0).UTC()
	l.SetClock(func() time.Time { return clock })

	for i := 0; i < 3; i++ {
		require.NoError(t, l.Append(Sample{
			Source: "usage", Balance: float64(100 - i), RateHourly: 0.5, Assignments: i,
		}))
	}

	fi, err := os.Stat(path)
	require.NoError(t, err)
	// Windows reports 0666 for writable files; ACLs are the real control.
	if goruntime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}

	samples, err := Read(path)
	require.NoError(t, err)
	require.Len(t, samples, 3)
	require.Equal(t, clock, samples[0].At)
	require.Equal(t, 100.0, samples[0].Balance)
	require.Equal(t, 98.0, samples[2].Balance)
	require.Equal(t, "usage", samples[2].Source)
}

func TestLedgerIsSizeBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccu.jsonl")
	l := New(path)
	l.SetMaxBytes(512)
	clock := time.Unix(1_700_000_000, 0).UTC()
	l.SetClock(func() time.Time { return clock })

	for i := 0; i < 40; i++ {
		clock = clock.Add(time.Second)
		require.NoError(t, l.Append(Sample{Source: "guard", Balance: float64(i), RateHourly: 1}))
	}

	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.LessOrEqual(t, fi.Size(), int64(512), "ledger must be bounded")
	samples, err := Read(path)
	require.NoError(t, err)
	require.NotEmpty(t, samples)
	require.Equal(t, "guard", samples[len(samples)-1].Source)
	require.Equal(t, 39.0, samples[len(samples)-1].Balance, "newest sample must survive the trim")
}

func TestReportIsCUOnly(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	samples := []Sample{
		{At: base.Add(-3 * time.Hour), Source: "guard", Balance: 100, RateHourly: 1},
		{At: base.Add(-30 * time.Minute), Source: "usage", Balance: 80, RateHourly: 2},
		{At: base, Source: "budget", Balance: 60, RateHourly: 3},
	}
	r := ReportOf(samples, base.Add(-24*time.Hour), base)
	require.Equal(t, 3, r.SampleCount)
	require.Equal(t, 60.0, r.Balance)
	require.InDelta(t, 2.0, r.RateHourly, 0.0001)
	require.Equal(t, "compute_units", r.Unit)
	require.Len(t, r.Windows, 3, "each sample falls in its own one-hour window")

	b, err := json.Marshal(r)
	require.NoError(t, err)
	text := strings.ToLower(string(b))
	for _, forbidden := range []string{"$", "usd", "dollar", "price"} {
		require.NotContains(t, text, forbidden, "report must never invent a currency")
	}
}

func TestReportSinceFiltersOldSamples(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	samples := []Sample{
		{At: base.Add(-48 * time.Hour), Balance: 10, RateHourly: 9},
		{At: base.Add(-2 * time.Hour), Balance: 20, RateHourly: 1},
	}
	r := ReportOf(samples, base.Add(-24*time.Hour), base)
	require.Equal(t, 1, r.SampleCount)
	require.Equal(t, 20.0, r.Balance)
}

func TestDefaultPathUsesXDGCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	p, err := DefaultPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "colab-cli", "ccu.jsonl"), p)
}

// Regression: cost.New(path) is called fresh by the budget guard, `usage` and
// the --max-cu monitor, so each call gets its own mutex. Concurrent appends to
// the same ccu.jsonl must still be serialized or the read-modify-rename cycle
// loses samples. Upstream analogue: the guard/usage/monitor fan-out that the
// race agent flagged; there is no single upstream Python test for it.
//
// Every writer deliberately builds its OWN Ledger instance and appends a
// distinct sample. A barrier releases them together, and repeated rounds make
// a lost-update interleaving reliably observable.
func TestConcurrentAppendFromDistinctLedgerInstances(t *testing.T) {
	const (
		rounds  = 3
		writers = 24
	)
	for round := 0; round < rounds; round++ {
		path := filepath.Join(t.TempDir(), "ccu.jsonl")

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make(chan error, writers)
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// Mirrors sampleCCU's cost.New(path).Append(...) call site.
				l := New(path)
				<-start
				errs <- l.Append(Sample{
					At:         time.Unix(1_700_000_000+int64(i), 0).UTC(),
					Source:     "usage",
					Balance:    float64(i),
					RateHourly: 1,
				})
			}(i)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}

		// Raw JSONL check: exactly one valid, distinct line per writer. Read()
		// skips malformed lines, so it alone cannot prove there was no corruption.
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		lines := bytes.Split(bytes.TrimSpace(b), []byte{'\n'})
		require.Len(t, lines, writers, "round %d: lost or merged samples", round)
		seen := make(map[float64]bool, writers)
		for _, line := range lines {
			var s Sample
			require.NoError(t, json.Unmarshal(line, &s), "round %d: corrupt JSONL line %q", round, line)
			require.False(t, seen[s.Balance], "round %d: duplicate sample %v", round, s.Balance)
			seen[s.Balance] = true
		}
		for i := 0; i < writers; i++ {
			require.True(t, seen[float64(i)], "round %d: sample %d missing", round, i)
		}

		// This is exactly what `golab cost --json` consumes: Read -> ReportOf.
		samples, err := Read(path)
		require.NoError(t, err)
		report := ReportOf(samples, time.Unix(1_699_000_000, 0).UTC(), time.Unix(1_701_000_000, 0).UTC())
		require.Equal(t, writers, report.SampleCount, "round %d: cost --json must see every sample", round)
	}
}
