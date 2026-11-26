package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/compat"
)

// NewCompatCommand exposes the embedded command-surface snapshot. It is a
// local comparison only: no network call and no live upstream claim.
func NewCompatCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compat",
		Short: "Compare the CLI surface against a local snapshot",
		Args:  noArgs,
	}
	var asJSON bool
	report := &cobra.Command{
		Use:   "report",
		Short: "Report added/missing commands and flags against the embedded snapshot",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snap, err := compat.Embedded()
			if err != nil {
				return err
			}
			rep := compat.Compare(cmd.Root(), snap)
			d := depsOf(cmd)
			if asJSON {
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(d.Stdout, string(b))
				return nil
			}
			fmt.Fprintf(d.Stdout, "[compat] snapshot: %s (%s; NOT a live upstream capture)\n",
				rep.SnapshotKind, rep.SnapshotSource)
			if rep.SnapshotScope != "" {
				fmt.Fprintf(d.Stdout, "[compat] scope: %s; added commands outside the subset are not claims of novelty\n",
					rep.SnapshotScope)
			}
			fmt.Fprintf(d.Stdout, "[compat] snapshot commands: %d; current commands: %d\n",
				rep.SnapshotCommands, rep.CurrentCommands)
			fmt.Fprintf(d.Stdout, "[compat] added commands: %v\n", rep.AddedCommands)
			fmt.Fprintf(d.Stdout, "[compat] missing commands: %v\n", rep.MissingCommands)
			for name, flags := range rep.AddedFlags {
				fmt.Fprintf(d.Stdout, "[compat] added flags %s: %v\n", name, flags)
			}
			for name, flags := range rep.MissingFlags {
				fmt.Fprintf(d.Stdout, "[compat] missing flags %s: %v\n", name, flags)
			}
			return nil
		},
	}
	report.Flags().BoolVar(&asJSON, "json", false, "Emit the comparison as a single JSON document")
	cmd.AddCommand(report)
	return cmd
}

// RegisterCompatCommands adds `compat` to the root command tree.
func RegisterCompatCommands(root *cobra.Command) { root.AddCommand(NewCompatCommand()) }
