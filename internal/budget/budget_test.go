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

// TestCheckBalanceReserveFloor proves the balance threshold is a MINIMUM
// RESERVE FLOOR: with --enforce, an allocation is refused when remaining
// credit is at or below the floor, and allowed when credit is well above it.
// Regression for the C6 balance-direction defect (1000 CU wrongly denied with
// max_balance: 100).
func TestCheckBalanceReserveFloor(t *testing.T) {
	const floor = 100.0
	for _, tc := range []struct {
		name    string
		balance float64
		want    bool // Decision.Allowed
	}{
		{"below floor", 20, false},
		{"at floor", floor, false},
		{"well above floor", 1000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Check(context.Background(),
				&colabapi.Consumption{PaidComputeUnitsBalance: tc.balance},
				Policy{MaxBalance: floor, Enforce: true})
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed != tc.want {
				t.Fatalf("balance %.2f with reserve floor %.2f: allowed=%t reasons=%v, want allowed=%t",
					tc.balance, floor, d.Allowed, d.Reasons, tc.want)
			}
			if !strings.Contains(strings.Join(d.Reasons, " "), "reserve floor") {
				t.Fatalf("reasons do not describe the reserve floor: %v", d.Reasons)
			}
		})
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

// TestLoadProfileEnforceFromDefaults proves `enforce` is actually read from
// defaults.budget. Without this, a profile that says enforce: true is only
// advisory and the fail-closed guard path is unreachable from configuration.
func TestLoadProfileEnforceFromDefaults(t *testing.T) {
	const body = `version: 1
defaults:
  budget:
    max_balance: 100
    max_hourly: 20
    enforce: true
`
	dir := t.TempDir()
	path := filepath.Join(dir, "golab.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfile(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enforce {
		t.Fatalf("default policy = %+v, want Enforce=true", p)
	}
	if p.MaxBalance != 100 || p.MaxHourly != 20 {
		t.Fatalf("default policy = %+v, want thresholds preserved {100 20}", p)
	}
}

// TestLoadProfileNamedEnforceOverride covers both directions of named-profile
// precedence: a profile may turn enforcement off, and a profile that omits the
// key inherits the defaults value.
func TestLoadProfileNamedEnforceOverride(t *testing.T) {
	const body = `version: 1
defaults:
  budget:
    max_balance: 100
    max_hourly: 20
    enforce: true
profiles:
  ci:
    budget:
      max_balance: 50
      enforce: false
  inherit:
    budget:
      max_hourly: 40
`
	dir := t.TempDir()
	path := filepath.Join(dir, "golab.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ci, err := LoadProfile(context.Background(), path, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if ci.Enforce {
		t.Fatalf("ci policy = %+v, want explicit enforce:false override", ci)
	}
	inherited, err := LoadProfile(context.Background(), path, "inherit")
	if err != nil {
		t.Fatal(err)
	}
	if !inherited.Enforce {
		t.Fatalf("inherit policy = %+v, want defaults enforce:true inherited", inherited)
	}
}

// TestLoadProfileRejectsNonBooleanEnforce requires a clear, typed error for a
// malformed enforce value instead of silently treating it as advisory.
func TestLoadProfileRejectsNonBooleanEnforce(t *testing.T) {
	const body = `version: 1
defaults:
  budget:
    max_balance: 100
    enforce: maybe
`
	dir := t.TempDir()
	path := filepath.Join(dir, "golab.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfile(context.Background(), path, "")
	if !errors.Is(err, ErrInvalidBoolean) {
		t.Fatalf("error = %v, want ErrInvalidBoolean", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "enforce") || !strings.Contains(msg, "boolean") {
		t.Fatalf("error = %q, want a clear non-boolean enforce diagnostic", msg)
	}
}

// TestLoadProfileEnforceDeniesTrippedReserveFloor is the end-to-end budget
// assertion: a named profile with enforce:true that inherits a reserve floor
// must produce a denied Check decision when the balance is at/below the floor.
func TestLoadProfileEnforceDeniesTrippedReserveFloor(t *testing.T) {
	const body = `version: 1
defaults:
  budget:
    max_balance: 100
profiles:
  ci:
    budget:
      enforce: true
`
	dir := t.TempDir()
	path := filepath.Join(dir, "golab.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfile(context.Background(), path, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxBalance != 100 || !p.Enforce {
		t.Fatalf("ci policy = %+v, want inherited floor 100 and enforce=true", p)
	}
	d, err := Check(context.Background(),
		&colabapi.Consumption{PaidComputeUnitsBalance: 20}, p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || !d.Enforced {
		t.Fatalf("decision = %+v, want denied and enforced at a tripped reserve floor", d)
	}
	if !strings.Contains(strings.Join(d.Reasons, " "), "reserve floor") {
		t.Fatalf("reasons do not describe the reserve floor: %v", d.Reasons)
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
