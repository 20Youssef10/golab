package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// NewStateCommand exposes the additive sessions.json schema version. Reporting
// is the default; a write happens only with --apply. No destructive upgrade
// and no network access are involved.
func NewStateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Inspect and migrate local state schema",
		Args:  noArgs,
	}
	var dryRun, apply, asJSON bool
	migrate := &cobra.Command{
		Use:   "migrate",
		Short: "Report (or apply) the additive sessions.json schema migration",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := depsOf(cmd)
			if dryRun {
				apply = false // an explicit dry-run always wins
			}
			// Default is a report; --dry-run is an explicit spelling of that
			// default. Only --apply writes, and it still preserves every
			// session record verbatim.
			report, err := d.Store.Migrate(!apply)
			if err != nil {
				return exitf(1, "[colab] state migrate: %s", strings.ReplaceAll(err.Error(), "\n", " "))
			}
			if asJSON {
				b, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(d.Stdout, string(b))
				return nil
			}
			verb := "report"
			if apply {
				verb = "applied"
			}
			fmt.Fprintf(d.Stdout, "[state] migrate: %s (schema %d -> %d, %d session(s))\n",
				verb, report.From, report.To, report.Sessions)
			for _, note := range report.Notes {
				fmt.Fprintf(d.Stdout, "[state] %s\n", note)
			}
			if !apply {
				fmt.Fprintln(d.Stdout, "[state] dry-run: no changes made; use --apply to stamp schema_version")
			}
			return nil
		},
	}
	migrate.Flags().BoolVar(&dryRun, "dry-run", false, "Explicitly report only (the default)")
	migrate.Flags().BoolVar(&apply, "apply", false, "Stamp schema_version (preserves all session records)")
	migrate.Flags().BoolVar(&asJSON, "json", false, "Emit the migration report as JSON")
	cmd.AddCommand(migrate)
	return cmd
}

// RegisterStateCommands adds `state` to the root command tree.
func RegisterStateCommands(root *cobra.Command) { root.AddCommand(NewStateCommand()) }
