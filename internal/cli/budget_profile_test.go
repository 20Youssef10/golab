package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/budget"
	"github.com/20Youssef10/golab/internal/colabapi"
)

// guardProfileYAML gives each named profile a distinct reserve floor so the
// profile the guard actually loaded is observable through budgetCheckFn's
// policy without adding a test-only seam to production code.
const guardProfileYAML = `version: 1
defaults:
  budget:
    max_balance: 10
profiles:
  default:
    budget:
      max_balance: 11
  ci:
    budget:
      max_balance: 12
  prod:
    budget:
      max_balance: 13
`

// unsetEnvForTest removes an env var for the duration of a test.
func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// guardLoadedFloor runs the real guardBudget with the supplied environment and
// root flags and returns the reserve floor of the policy it loaded. It pins
// defect 3.7 (the hardcoded "default" profile) at the guard's public seam.
func guardLoadedFloor(t *testing.T, envProfile string, flags ...string) (float64, error) {
	t.Helper()
	if envProfile == "" {
		unsetEnvForTest(t, "GOLAB_BUDGET_PROFILE")
	} else {
		t.Setenv("GOLAB_BUDGET_PROFILE", envProfile)
	}
	path := filepath.Join(t.TempDir(), "golab.yaml")
	if err := os.WriteFile(path, []byte(guardProfileYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOLAB_BUDGET_FILE", path)

	h := newHarness(t)
	setStreams(h)
	h.ctrl.SetConsumption(5, 0, 0)

	root := NewRootCommand(strings.NewReader(""), &h.stdout, &h.stderr)
	if err := root.ParseFlags(flags); err != nil {
		t.Fatal(err)
	}

	orig := budgetCheckFn
	defer func() { budgetCheckFn = orig }()
	called := false
	var floor float64
	budgetCheckFn = func(_ context.Context, _ *colabapi.Consumption, policy *budget.Policy) (budget.Decision, error) {
		called = true
		floor = policy.MaxBalance
		return budget.Decision{Allowed: true}, nil
	}

	err := guardBudget(context.Background(), h.deps, root)
	if !called {
		t.Fatalf("guard did not reach the budget check (err=%v, stderr=%q)", err, h.stderr.String())
	}
	return floor, err
}

// Defect 3.7: GOLAB_BUDGET_PROFILE must select the guard's named profile.
func TestGuardBudgetUsesEnvProfile(t *testing.T) {
	floor, err := guardLoadedFloor(t, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if floor != 12 {
		t.Fatalf("loaded reserve floor = %v, want 12 (the ci profile)", floor)
	}
}

// An explicit --budget-profile wins over the environment default.
func TestGuardBudgetFlagOverridesEnvProfile(t *testing.T) {
	floor, err := guardLoadedFloor(t, "ci", "--budget-profile=prod")
	if err != nil {
		t.Fatal(err)
	}
	if floor != 13 {
		t.Fatalf("loaded reserve floor = %v, want 13 (the explicit prod profile)", floor)
	}
}

// With neither flag nor environment variable, behaviour stays unchanged: the
// guard loads the "default" profile.
func TestGuardBudgetDefaultsToDefaultProfile(t *testing.T) {
	floor, err := guardLoadedFloor(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if floor != 11 {
		t.Fatalf("loaded reserve floor = %v, want 11 (the default profile)", floor)
	}
}

// --budget-profile is a persistent flag and must coexist with --explain.
func TestBudgetProfileFlagCoexistsWithExplain(t *testing.T) {
	root := NewRootCommand(strings.NewReader(""), io.Discard, io.Discard)
	if err := root.ParseFlags([]string{"--budget-profile=ci", "--explain"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := root.PersistentFlags().GetString("budget-profile"); got != "ci" {
		t.Fatalf("--budget-profile = %q, want ci", got)
	}
	if !explainRequested(root) {
		t.Fatal("--explain was lost when --budget-profile was set")
	}
}

// Shell completion still runs with the new persistent flag present.
func TestBudgetProfileFlagDoesNotBreakCompletion(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "completion", "bash", "--budget-profile=ci"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if h.stdout.Len() == 0 {
		t.Fatal("no completion script produced")
	}
}

// The budget subcommand's --profile/--profile-name flags keep working when the
// persistent --budget-profile flag is also present.
func TestBudgetProfileFlagDoesNotShadowBudgetProfile(t *testing.T) {
	h := newHarness(t)
	h.ctrl.SetConsumption(5, 0, 0)
	path := filepath.Join(t.TempDir(), "golab.yaml")
	if err := os.WriteFile(path, []byte(guardProfileYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	code := h.run(t, "", "budget", "--profile", path, "--profile-name", "ci",
		"--budget-profile=prod", "--max-balance", "1", "--json")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, h.stdout.String(), h.stderr.String())
	}
}
