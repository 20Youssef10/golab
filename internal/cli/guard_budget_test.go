package cli

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/budget"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

// TestGuardBudgetEnforceFailsClosedOnUsageError pins defect 2.5: with
// enforcement enabled, one transient ccu-info 5xx must not disable the spend
// guard and allow an allocation. (The upstream Python CLI has no profile
// guard; this is a golab-specific safety contract.)
func TestGuardBudgetEnforceFailsClosedOnUsageError(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	h.ctrl.SetFault(fakecolab.Fault{
		Method:       http.MethodGet,
		PathContains: "/ccu-info",
		Status:       http.StatusInternalServerError,
		Body:         `{"error":"transient upstream failure"}`,
	})

	err := guardBudgetWithPolicy(context.Background(), h.deps,
		budget.Policy{MaxBalance: 10, Enforce: true})
	if err == nil {
		t.Fatal("enforced guard returned nil on usage error, want fail-closed exit")
	}
	mapped := mapError(err)
	var ee *exitError
	if !errors.As(mapped, &ee) || ee.code != 1 {
		t.Fatalf("mapped error = %#v, want exit code 1", mapped)
	}
	if !strings.Contains(ee.msg, "budget guard: usage unavailable") {
		t.Fatalf("message = %q, want a clear budget-guard diagnostic", ee.msg)
	}
	if !strings.Contains(ee.msg, "HTTP 500") || !strings.Contains(ee.msg, "ccu-info") {
		t.Fatalf("message = %q, want the redacted underlying usage error", ee.msg)
	}
	for _, r := range h.ctrl.Requests() {
		if r.Method != http.MethodGet {
			t.Fatalf("guard budget issued %s %s, want no allocation", r.Method, r.Path)
		}
	}
}

// TestGuardBudgetAdvisoryStaysSilentOnUsageError keeps advisory mode a no-op
// when usage is unavailable: no error, no diagnostic, no allocation.
func TestGuardBudgetAdvisoryStaysSilentOnUsageError(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	h.ctrl.SetFault(fakecolab.Fault{
		Method:       http.MethodGet,
		PathContains: "/ccu-info",
		Status:       http.StatusInternalServerError,
		Body:         `{"error":"transient upstream failure"}`,
	})

	err := guardBudgetWithPolicy(context.Background(), h.deps,
		budget.Policy{MaxBalance: 10}) // Enforce is false.
	if err != nil {
		t.Fatalf("advisory guard returned %v, want silent no-op", err)
	}
	if got := h.stderr.String(); got != "" {
		t.Fatalf("advisory guard wrote to stderr: %q", got)
	}
	for _, r := range h.ctrl.Requests() {
		if r.Method != http.MethodGet {
			t.Fatalf("guard budget issued %s %s, want no allocation", r.Method, r.Path)
		}
	}
}
