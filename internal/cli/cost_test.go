package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/20Youssef10/golab/internal/budget"
	"github.com/20Youssef10/golab/internal/cost"
)

// Backlog Tier 1, item 3: every ccu-info fetch (usage, budget, guard) is
// appended to the private ledger, and `cost` reports observed CU only.

func withCostLedger(t *testing.T) string {
	t.Helper()
	orig := ccuLedgerPath
	path := filepath.Join(t.TempDir(), "ccu.jsonl")
	ccuLedgerPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { ccuLedgerPath = orig })
	return path
}

func TestSampleCCUAppendsLedger(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	path := withCostLedger(t)
	h.ctrl.SetConsumption(42, 3.5, 2)

	client, err := h.deps.ColabClient(context.Background())
	require.NoError(t, err)
	u, err := sampleCCU(context.Background(), h.deps, client, "usage")
	require.NoError(t, err)
	require.Equal(t, 42.0, u.PaidComputeUnitsBalance)

	samples, err := cost.Read(path)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	require.Equal(t, "usage", samples[0].Source)
	require.Equal(t, 42.0, samples[0].Balance)
	require.Equal(t, 3.5, samples[0].RateHourly)
}

func TestGuardBudgetSamplesCCU(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	path := withCostLedger(t)
	h.ctrl.SetConsumption(50, 1, 0)

	require.NoError(t, guardBudgetWithPolicy(context.Background(), h.deps, budget.Policy{MaxBalance: 10}))
	samples, err := cost.Read(path)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	require.Equal(t, "guard", samples[0].Source)
}

func TestCostCommandReportsCUOnly(t *testing.T) {
	h := newHarness(t)
	path := withCostLedger(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	origCostNow := costNow
	costNow = func() time.Time { return now }
	t.Cleanup(func() { costNow = origCostNow })

	l := cost.New(path)
	l.SetClock(func() time.Time { return now })
	require.NoError(t, l.Append(cost.Sample{At: now.Add(-2 * time.Hour), Source: "usage", Balance: 90, RateHourly: 1}))
	require.NoError(t, l.Append(cost.Sample{At: now, Source: "budget", Balance: 70, RateHourly: 2}))

	code := h.run(t, "", "cost", "--since", "24h", "--json")
	require.Equal(t, 0, code, h.stderr.String())
	var report cost.Report
	require.NoError(t, json.Unmarshal([]byte(h.stdout.String()), &report))
	require.Equal(t, 2, report.SampleCount)
	require.Equal(t, 70.0, report.Balance)
	require.Equal(t, "compute_units", report.Unit)

	lower := strings.ToLower(h.stdout.String())
	for _, forbidden := range []string{"$", "usd", "dollar", "price"} {
		require.NotContains(t, lower, forbidden, "cost output must never invent dollars")
	}
}

func TestCostCommandTextHasNoCurrency(t *testing.T) {
	h := newHarness(t)
	path := withCostLedger(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	origCostNow := costNow
	costNow = func() time.Time { return now }
	t.Cleanup(func() { costNow = origCostNow })

	l := cost.New(path)
	l.SetClock(func() time.Time { return now })
	require.NoError(t, l.Append(cost.Sample{At: now, Source: "guard", Balance: 55, RateHourly: 4}))

	code := h.run(t, "", "cost", "--since", "24h")
	require.Equal(t, 0, code, h.stderr.String())
	require.Contains(t, h.stdout.String(), "compute units")
	lower := strings.ToLower(h.stdout.String())
	for _, forbidden := range []string{"$", "usd", "dollar", "price"} {
		require.NotContains(t, lower, forbidden)
	}
}
