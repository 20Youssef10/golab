package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/compat"
)

// compatFetch is a test seam (production default: the real fetcher). Tests
// point it at an httptest server; there is no flag or env var that redirects a
// release build away from the official upstream host.
var compatFetch = func(ctx context.Context, cfg compat.FetchConfig) (compat.Snapshot, error) {
	return compat.Fetch(ctx, cfg)
}

// NewCompatCommand exposes the command-surface snapshot. `report` is local-only
// by default; `refresh` opts in to one explicit fetch of the official upstream
// CLI source and never writes unless --out is given.
func NewCompatCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compat",
		Short: "Compare the CLI surface against a local snapshot",
		Args:  noArgs,
	}
	var asJSON bool
	var snapshotPath string
	report := &cobra.Command{
		Use:   "report",
		Short: "Report added/missing commands and flags against a snapshot",
		Long: "Report deltas against the embedded self-snapshot, or against an explicit " +
			"--snapshot FILE (e.g. one produced by `compat refresh --out FILE`). " +
			"A snapshot is never live upstream truth.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snap, err := loadCompatSnapshot(snapshotPath)
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
			if rep.SnapshotKind == compat.KindSelf {
				fmt.Fprintf(d.Stdout, "[compat] snapshot: %s (%s; NOT a live upstream capture; NOT live truth)\n",
					rep.SnapshotKind, rep.SnapshotSource)
			} else {
				fmt.Fprintf(d.Stdout, "[compat] snapshot: %s (%s; NOT live truth; a captured snapshot, not a live probe)\n",
					rep.SnapshotKind, rep.SnapshotSource)
			}
			if rep.SourceURL != "" {
				fmt.Fprintf(d.Stdout, "[compat] source_url: %s\n", rep.SourceURL)
				fmt.Fprintf(d.Stdout, "[compat] ref: %s; fetched_at: %s; method: %s\n",
					rep.Ref, rep.FetchedAt, rep.Method)
				fmt.Fprintf(d.Stdout, "[compat] content_hash: %s\n", rep.ContentHash)
			}
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
	report.Flags().StringVar(&snapshotPath, "snapshot", "",
		"Compare against FILE instead of the embedded self-snapshot")

	var out string
	var ref string
	refresh := &cobra.Command{
		Use:   "refresh",
		Short: "Fetch the official upstream CLI surface into a provenance-tagged snapshot",
		Long: "Fetch the upstream Typer CLI source from the official GitHub raw host at a pinned " +
			"ref and write a conservatively parsed, provenance-tagged snapshot. " +
			"Dry-run by default: nothing is written unless --out is given. " +
			"--out - prints the snapshot JSON to stdout without writing a file.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snap, err := compatFetch(cmd.Context(), compat.FetchConfig{Ref: ref})
			if err != nil {
				return fmt.Errorf("compat refresh: %w", err)
			}
			d := depsOf(cmd)
			switch out {
			case "":
				fmt.Fprintf(d.Stdout, "[compat] dry-run: fetched %s at %s (ref %s, %s, %d commands). "+
					"Not live truth. No file written; pass --out FILE to save or --out - for JSON.\n",
					snap.Kind, snap.FetchedAt, snap.Ref, snap.ContentHash, len(snap.Commands))
			case "-":
				b, err := json.MarshalIndent(snap, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(d.Stdout, string(b))
			default:
				if err := compat.WriteSnapshot(out, snap); err != nil {
					return err
				}
				fmt.Fprintf(d.Stdout, "[compat] wrote %s snapshot to %s (ref %s, %s); not live truth\n",
					snap.Kind, out, snap.Ref, snap.ContentHash)
			}
			return nil
		},
	}
	refresh.Flags().StringVar(&out, "out", "", "Write the snapshot to FILE (default: dry-run; '-' prints JSON to stdout)")
	refresh.Flags().StringVar(&ref, "ref", compat.DefaultRef,
		"Git ref/tag to pin (the default is a compatibility label, NOT a resolvable git ref; "+
			"pass a real ref such as v0.8.0 or main)")

	cmd.AddCommand(report, refresh)
	return cmd
}

func loadCompatSnapshot(path string) (compat.Snapshot, error) {
	if path == "" {
		return compat.Embedded()
	}
	return compat.LoadSnapshot(path)
}

// RegisterCompatCommands adds `compat` to the root command tree.
func RegisterCompatCommands(root *cobra.Command) { root.AddCommand(NewCompatCommand()) }
