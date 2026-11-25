package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func usageCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "usage",
		Short: "Show account compute-unit usage rate and balance",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			client, err := d.ColabClient(cmd.Context())
			if err != nil {
				return err
			}
			result, err := client.Usage(cmd.Context())
			if err != nil {
				// A failed ccu-info fetch is a recoverable advisory, not a command
				// failure: keep the [colab] diagnostic on stderr but exit 0 so it
				// stays distinguishable from a real failure (defect 4.19).
				fmt.Fprintf(d.Stderr, "[colab] Warning: failed to fetch compute-unit info: %s\n", err)
				return nil
			}
			fmt.Fprintln(d.Stdout, result.String())
			return nil
		},
	}
}

func registerUsageCommands(root *cobra.Command) { root.AddCommand(usageCommand()) }
