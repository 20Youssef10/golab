package budget

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/colabapi"
)

const yamlProfile = `version: 1
defaults:
  budget:
    max_balance: 100
    max_hourly: 20
profiles:
  ci:
    budget:
      max_balance: 50
  loose:
    budget:
      max_hourly: 40
`

// TestCheckIsAdvisoryUnlessEnforced is the core A6 safety contract: a tripped
// threshold never refuses an allocation unless the caller passes --enforce.
func TestCheckIsAdvisoryUnlessEnforced(t *testing.T) {
	usage := &colabapi.Consumption{PaidComputeUnitsBalance: 5, ConsumptionRateHourly: 12}
	advisory, err := Check(context.Background(), usage, Policy{MaxBalance: 10, MaxHourly: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !advisory.Allowed || advisory.Enforced || len(advisory.Reasons) == 0 {
		t.Fatalf("advisory decision = %+v, want allowed and non-enforced with reasons", advisory)
	}
	enforced, err := Check(context.Background(), usage, Policy{MaxBalance: 10, MaxHourly: 10, Enforce: true})
	if err != nil {
		t.Fatal(err)
	}
	if enforced.Allowed || !enforced.Enforced {
		t.Fatalf("enforced decision = %+v, want denied", enforced)
	}
}

// TestCheckNeverInventsPrices rejects any dollar estimate in the decision:
// thresholds are compute units (CU) and CU/hour only.
func TestCheckNeverInventsPrices(t *testing.T) {
	usage := &colabapi.Consumption{PaidComputeUnitsBalance: 5, ConsumptionRateHourly: 2, AssignmentsCount: 1}
	d, err := Check(context.Background(), usage, Policy{MaxBalance: 10, MaxHourly: 1, Enforce: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(b))
	for _, forbidden := range []string{"$", "usd", "dollar", "price", "estimated cost"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("decision contains a made-up price term %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "compute units") {
		t.Errorf("decision does not say the unit: %s", text)
	}
}

// TestCheckUnknownUsageFailOpenAdvisory handles a missing ccu-info result:
// advisory mode warns, enforce mode denies. It never reports "safe to spend".
func TestCheckUnknownUsageFailOpenAdvisory(t *testing.T) {
	advisory, err := Check(context.Background(), nil, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if !advisory.Unknown || !advisory.Allowed {
		t.Fatalf("advisory = %+v", advisory)
	}
	enforced, err := Check(context.Background(), nil, Policy{Enforce: true})
	if err != nil {
		t.Fatal(err)
	}
	if enforced.Allowed || !enforced.Unknown {
		t.Fatalf("enforced = %+v", enforced)
	}
}

// TestCheckRespectsContext ensures a cancelled caller aborts before deciding.
func TestCheckRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Check(ctx, &colabapi.Consumption{}, Policy{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want Canceled", err)
	}
}

// TestLoadProfileProfileInheritance checks the tiny strict YAML subset: a
// profile's budget overrides defaults, and unknown fields are rejected.
func TestLoadProfileProfileInheritance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "golab.yaml")
	if err := os.WriteFile(path, []byte(yamlProfile), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfile(context.Background(), path, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxBalance != 50 || p.MaxHourly != 20 {
		t.Fatalf("policy = %+v, want {50 20}", p)
	}
	p, err = LoadProfile(context.Background(), path, "loose")
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxBalance != 100 || p.MaxHourly != 40 {
		t.Fatalf("policy = %+v, want {100 40}", p)
	}
	if _, err := LoadProfile(context.Background(), path, "missing"); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("error = %v, want ErrProfileNotFound", err)
	}
}

// TestLoadProfileRejectsPriceTables refuses user-supplied rate tables instead
// of computing an estimate from unsourced numbers.
func TestLoadProfileRejectsPriceTables(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"rates", "version: 1\nprofiles:\n  ci:\n    budget:\n      rates:\n        T4: 0.50\n", ErrPriceTable},
		{"unknown key", "version: 1\nprofiles:\n  ci:\n    budget:\n      max_instances: 3\n", ErrUnknownKey},
		{"bad version", "version: 99\n", ErrUnsupportedVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfile(context.Background(), path, "ci"); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}
