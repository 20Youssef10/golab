package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/budget"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/logx"
)

// explainRequested reports whether --explain was set on the root command.
func explainRequested(cmd *cobra.Command) bool {
	if cmd == nil || cmd.Root() == nil {
		return false
	}
	v, err := cmd.Root().PersistentFlags().GetBool("explain")
	return err == nil && v
}

// assumeYes reports whether --yes / COLAB_ASSUME_YES was set. It never bypasses
// a budget guard: it only suppresses non-destructive confirmations.
func assumeYes(cmd *cobra.Command) bool {
	if cmd == nil || cmd.Root() == nil {
		return false
	}
	v, err := cmd.Root().PersistentFlags().GetBool("yes")
	return (err == nil && v) || parseEnvBool(os.Getenv("COLAB_ASSUME_YES"))
}

func parseEnvBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// budgetCheckFn is the decision hook used by guardBudget. The policy is a
// pointer so a test can inject `--enforce` into the same value the guard uses
// to choose denial over an advisory. Production delegates to budget.Check.
var budgetCheckFn = func(ctx context.Context, usage *colabapi.Consumption, policy *budget.Policy) (budget.Decision, error) {
	return budget.Check(ctx, usage, *policy)
}

// guardBudget applies an OPTIONAL golab.yaml profile before a billable call.
// Without a profile it is a no-op; with one it is advisory unless the profile
// sets enforce: true. Thresholds are compute units only — no dollar prices are
// invented. Auth/network failures are reported by the real command, not here.
func guardBudget(ctx context.Context, d *app.Deps, cmd *cobra.Command) error {
	if cmd == nil || explainRequested(cmd) {
		return nil
	}
	path := os.Getenv("GOLAB_BUDGET_FILE")
	if path == "" {
		path = "golab.yaml"
	}
	policy, err := budget.LoadProfile(ctx, path, budgetProfileName(cmd))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, budget.ErrProfileNotFound) {
		return nil
	}
	if err != nil {
		return exitf(2, "[colab] invalid budget profile: %s", logx.Redact(err.Error()))
	}
	return guardBudgetWithPolicy(ctx, d, policy)
}

// budgetProfileName resolves the profile the guard loads. The persistent flag
// already defaults to GOLAB_BUDGET_PROFILE (or "default"), so an explicit flag
// wins over the environment. Commands invoked without the root flag (for
// example, unit calls with a bare cobra command) fall back to the environment
// and then to "default", preserving the historical behaviour.
func budgetProfileName(cmd *cobra.Command) string {
	if cmd != nil && cmd.Root() != nil {
		if v, err := cmd.Root().PersistentFlags().GetString("budget-profile"); err == nil && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return envOr("GOLAB_BUDGET_PROFILE", "default")
}

// guardBudgetWithPolicy applies a resolved policy before a billable call. The
// policy is passed in so the usage-unavailable behaviour can be tested with an
// injected Enforce value without depending on profile parsing.
func guardBudgetWithPolicy(ctx context.Context, d *app.Deps, policy budget.Policy) error {
	client, err := d.ColabClient(ctx)
	if err != nil {
		return budgetUsageUnavailable(policy, err)
	}
	// Tier 1 item 3: every ccu-info fetch is sampled into the private ledger.
	usage, err := sampleCCU(ctx, d, client, "guard")
	if err != nil {
		return budgetUsageUnavailable(policy, err)
	}
	decision, err := budgetCheckFn(ctx, usage, &policy)
	if err != nil {
		return exitf(1, "[colab] budget check: %s", logx.Redact(err.Error()))
	}
	if decision.Allowed {
		return nil
	}
	msg := strings.Join(decision.Reasons, "; ")
	if policy.Enforce {
		return exitf(1, "[colab] budget guard: %s", msg)
	}
	fmt.Fprintf(d.Stderr, "[colab] budget advisory: %s\n", msg)
	return nil
}

// budgetUsageUnavailable fails closed when an enforced guard cannot read
// ccu-info: an unknown balance is never permission to allocate. Advisory mode
// keeps the historical silent no-op.
func budgetUsageUnavailable(policy budget.Policy, err error) error {
	if !policy.Enforce {
		return nil
	}
	return exitf(1, "[colab] budget guard: usage unavailable: %s", logx.Redact(err.Error()))
}

// apiLabel names the control plane a plan will use.
func apiLabel(d *app.Deps) string {
	if d != nil && d.Config.UsePublicAPI != nil && *d.Config.UsePublicAPI {
		return "public"
	}
	return "tunnel"
}

// PlanFields is the redacted, machine-independent description of what a
// billable command would do. It deliberately has no token, URL-with-credentials
// or request-body field: an explain output must be safe to paste into a ticket.
type PlanFields struct {
	Operation      string
	Endpoint       string
	Method         string
	API            string // "tunnel" | "public"
	Variant        string
	Accelerator    string
	Shape          string
	Session        string
	Cleanup        string
	CostSource     string
	CreatesSession bool
	Billable       bool
}

// ExplainPlan returns a deterministic, JSON/YAML-free description of a plan.
// It is pure: the caller should print it with fmt.Fprint(d.Stdout, …) *before*
// any authentication, state mutation or network call. Root wiring: add a
// persistent --explain flag; in each billable command's RunE, build PlanFields
// from the same resolved config as the real request and return early after
// printing ExplainPlan(cmd, fields).
func ExplainPlan(cmd *cobra.Command, fields PlanFields) string {
	path := "golab"
	if cmd != nil && strings.TrimSpace(cmd.CommandPath()) != "" {
		path = cmd.CommandPath()
	}
	values := map[string]string{
		"command":         path,
		"operation":       fields.Operation,
		"endpoint":        fields.Endpoint,
		"method":          fields.Method,
		"api":             fields.API,
		"variant":         fields.Variant,
		"accelerator":     fields.Accelerator,
		"shape":           fields.Shape,
		"session":         fields.Session,
		"cleanup":         fields.Cleanup,
		"cost-source":     fields.CostSource,
		"creates-session": boolString(fields.CreatesSession),
		"billable":        boolString(fields.Billable),
	}
	keys := make([]string, 0, len(values))
	for k, v := range values {
		if v == "" || (k != "billable" && k != "creates-session" && v == "false") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("[golab] explain:")
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%s", k, logx.Redact(values[k]))
	}
	return b.String()
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
