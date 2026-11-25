package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBudgetCommandAdvisoryOnly proves the budget command is a GET-only
// advisory check: a tripped threshold does not fail without --enforce.
func TestBudgetCommandAdvisoryOnly(t *testing.T) {
	h := newHarness(t)
	h.ctrl.SetConsumption(5, 12, 1)
	if code := runNewCommand(t, h, "budget", "--max-balance", "10", "--max-hourly", "10", "--json"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	var d struct {
		Allowed  bool     `json:"allowed"`
		Enforced bool     `json:"enforced"`
		Reasons  []string `json:"reasons"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &d); err != nil {
		t.Fatalf("stdout not JSON: %v (%s)", err, h.stdout.String())
	}
	if !d.Allowed || d.Enforced || len(d.Reasons) == 0 {
		t.Fatalf("decision = %+v", d)
	}
	for _, r := range h.ctrl.Requests() {
		if r.Method != "GET" {
			t.Fatalf("budget issued %s %s", r.Method, r.Path)
		}
	}
}

// TestBudgetCommandEnforceDenies turns a profile reserve floor into a refusal,
// again with no allocation request. The balance (5) is below the floor (10):
// a well-funded account must not be denied.
func TestBudgetCommandEnforceDenies(t *testing.T) {
	h := newHarness(t)
	h.ctrl.SetConsumption(5, 0, 0)
	path := filepath.Join(t.TempDir(), "golab.yaml")
	body := "version: 1\nprofiles:\n  ci:\n    budget:\n      max_balance: 10\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	code := runNewCommand(t, h, "budget", "--profile", path, "--profile-name", "ci", "--enforce")
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stdout = %s; stderr = %s", code, h.stdout.String(), h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "denied") {
		t.Fatalf("no denial message: %s", h.stderr.String())
	}
	for _, r := range h.ctrl.Requests() {
		if r.Method != "GET" {
			t.Fatalf("budget issued %s %s", r.Method, r.Path)
		}
	}
}
