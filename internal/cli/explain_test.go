package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestExplainPlanIsDeterministicAndSecretFree pins --explain's contract: a
// stable, human-readable plan with no JSON/YAML framing and no token field.
func TestExplainPlanIsDeterministicAndSecretFree(t *testing.T) {
	cmd := &cobra.Command{Use: "new"}
	fields := PlanFields{
		Operation:      "create-session",
		Endpoint:       "GET /tun/m/assign",
		Method:         "POST",
		API:            "tunnel",
		Variant:        "GPU",
		Accelerator:    "T4",
		Shape:          "STANDARD",
		Session:        "ci-run",
		CreatesSession: true,
		Billable:       true,
		CostSource:     "GET /tun/m/ccu-info",
		Cleanup:        "unassign on stop",
	}
	a := ExplainPlan(cmd, fields)
	b := ExplainPlan(cmd, fields)
	if a != b {
		t.Fatalf("non-deterministic plan:\n%s\n%s", a, b)
	}
	for _, want := range []string{
		"accelerator=T4", "api=tunnel", "billable=true", "endpoint=GET /tun/m/assign",
		"shape=STANDARD", "variant=GPU", "session=ci-run", "creates-session=true",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("plan missing %q: %s", want, a)
		}
	}
	if strings.Contains(a, "token=") || strings.Contains(a, "{") || strings.Contains(a, "---\n") {
		t.Fatalf("plan leaked a secret or used a data format: %s", a)
	}
}

// TestExplainPlanOmitsEmptyFields keeps a minimal plan short and still useful.
func TestExplainPlanOmitsEmptyFields(t *testing.T) {
	plan := ExplainPlan(nil, PlanFields{Operation: "status"})
	if !strings.Contains(plan, "operation=status") {
		t.Fatalf("plan = %q", plan)
	}
	if strings.Contains(plan, "endpoint=") || strings.Contains(plan, "accelerator=") {
		t.Fatalf("empty fields were printed: %q", plan)
	}
	if !strings.Contains(plan, "billable=false") {
		t.Fatalf("missing explicit billable flag: %q", plan)
	}
}
