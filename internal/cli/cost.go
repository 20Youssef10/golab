package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/cost"
)

// Tier 1 item 3. ccuLedgerPath and costNow are test seams with production
// defaults; the real ledger lives in $XDG_CACHE_HOME/colab-cli/ccu.jsonl.
var (
	ccuLedgerPath = cost.DefaultPath
	costNow       = time.Now
)

// sampleCCU wraps every existing ccu-info fetch with a best-effort ledger
// append. A ledger write failure is advisory and never changes the caller's
// result: usage/budget/guard behave exactly as before.
func sampleCCU(ctx context.Context, d *app.Deps, client colabapi.Client, source string) (*colabapi.Consumption, error) {
	u, err := client.Usage(ctx)
	if err != nil {
		return nil, err
	}
	path, pathErr := ccuLedgerPath()
	if pathErr == nil {
		s := cost.Sample{
			At:          costNow().UTC(),
			Source:      source,
			Balance:     u.PaidComputeUnitsBalance,
			RateHourly:  u.ConsumptionRateHourly,
			Assignments: u.AssignmentsCount,
		}
		if appendErr := cost.New(path).Append(s); appendErr != nil && d != nil && d.Log != nil {
			d.Log.Debug("ccu sample ledger write failed", "error", appendErr)
		}
	}
	return u, nil
}

// costCommand reports observed compute units over a time window. It never
// converts CU into money and never invents a price.
func costCommand() *cobra.Command {
	var since time.Duration
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "cost",
		Short: "Report observed compute-unit balance and rate (CU only, no prices)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if since <= 0 {
				return usagef("[colab] --since must be a positive duration.")
			}
			path, err := ccuLedgerPath()
			if err != nil {
				return err
			}
			samples, err := cost.Read(path)
			if err != nil {
				return err
			}
			now := costNow().UTC()
			report := cost.ReportOf(samples, now.Add(-since), now)
			if asJSON {
				b, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(d.Stdout, string(b))
				return nil
			}
			fmt.Fprintf(d.Stdout, "[cost] since %s: %d sample(s), compute units only\n",
				since, report.SampleCount)
			fmt.Fprintf(d.Stdout, "[cost] balance %.2f compute units | rate %.2f compute units/hour\n",
				report.Balance, report.RateHourly)
			for _, w := range report.Windows {
				fmt.Fprintf(d.Stdout, "[cost] window %s: %d sample(s), balance %.2f CU, rate %.2f CU/hour\n",
					w.Start.Format(time.RFC3339), w.Samples, w.Balance, w.RateHourly)
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&since, "since", 24*time.Hour, "Report samples from this time window")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the CU report as JSON")
	return cmd
}
