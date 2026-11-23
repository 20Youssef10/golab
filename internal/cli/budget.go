package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/budget"
	"github.com/20Youssef10/golab/internal/colabapi"
)

// NewBudgetCommand reports an advisory compute-unit budget decision. It only
// calls GET /tun/m/ccu-info; it never allocates and never computes a price.
func NewBudgetCommand() *cobra.Command {
	var profile, profileName string
	var maxBalance, maxHourly float64
	var enforce, asJSON bool
	cmd := &cobra.Command{
		Use:   "budget",
		Short: "Show an advisory compute-unit budget decision (no allocation)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			policy := budget.Policy{
				MaxBalance: maxBalance, MaxHourly: maxHourly, Enforce: enforce,
			}
			if profile != "" {
				p, err := budget.LoadProfile(cmd.Context(), profile, profileName)
				if err != nil {
					return exitf(2, "[colab] budget: %s", err)
				}
				if !cmd.Flags().Changed("max-balance") {
					policy.MaxBalance = p.MaxBalance
				}
				if !cmd.Flags().Changed("max-hourly") {
					policy.MaxHourly = p.MaxHourly
				}
			}
			var usage *colabapi.Consumption
			client, err := d.ColabClient(cmd.Context())
			if err != nil {
				return err
			}
			consumption, err := client.Usage(cmd.Context())
			if err == nil {
				usage = consumption
			}
			decision, err := budget.Check(cmd.Context(), usage, policy)
			if err != nil {
				return err
			}
			if asJSON {
				b, err := json.MarshalIndent(decision, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(d.Stdout, string(b))
			} else {
				status := "allowed"
				if !decision.Allowed {
					status = "denied"
				}
				fmt.Fprintf(d.Stdout, "[budget] status=%s enforced=%t source=%s\n",
					status, decision.Enforced, decision.Source)
				for _, reason := range decision.Reasons {
					fmt.Fprintf(d.Stdout, "[budget] - %s\n", reason)
				}
			}
			if !decision.Allowed {
				return exitf(1, "[colab] budget: allocation denied by --enforce")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "", "Optional golab.yaml profile file")
	cmd.Flags().StringVar(&profileName, "profile-name", "", "Named profile inside golab.yaml")
	cmd.Flags().Float64Var(&maxBalance, "max-balance", 0, "Advisory maximum compute-unit balance")
	cmd.Flags().Float64Var(&maxHourly, "max-hourly", 0, "Advisory maximum compute units/hour")
	cmd.Flags().BoolVar(&enforce, "enforce", false, "Refuse an allocation when a threshold is tripped")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the decision as JSON")
	return cmd
}

// RegisterBudgetCommands adds `budget` to the root command tree.
func RegisterBudgetCommands(root *cobra.Command) { root.AddCommand(NewBudgetCommand()) }
