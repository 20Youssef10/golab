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
				return exitf(1, "[colab] Warning: failed to fetch compute-unit info: %s", err)
			}
			fmt.Fprintln(d.Stdout, result.String())
			return nil
		},
	}
}

func registerUsageCommands(root *cobra.Command) { root.AddCommand(usageCommand()) }
