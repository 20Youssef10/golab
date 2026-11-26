package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/platform"
	"github.com/20Youssef10/golab/internal/state"
)

// Tier 1 item 1. reapNow is a test seam with a time.Now default; it is used
// only to decide whether a STALE LOCAL record is old enough to prune. Server
// assignments carry no creation timestamp, so every unmatched assignment is
// treated as an orphan regardless of --older-than.
var reapNow = time.Now

type reapTarget struct {
	Endpoint    string `json:"endpoint,omitempty"`
	RuntimeName string `json:"runtime_name,omitempty"`
}

type reapReport struct {
	DryRun     bool         `json:"dry_run"`
	Orphans    []reapTarget `json:"orphans"`
	Stale      []string     `json:"stale_local"`
	Unassigned []reapTarget `json:"unassigned"`
	Pruned     []string     `json:"pruned"`
	Failed     []reapTarget `json:"failed,omitempty"`
}

// reapRecordTime returns the best local "last seen" timestamp for a stale
// record: the last execution, then the TTL expiry, then the proxy-token expiry.
func reapRecordTime(s state.Session) (time.Time, bool) {
	if s.LastExecution != nil {
		const layout = "2006-01-02 15:04:05"
		// The CLI writes local time, but older records may carry UTC. Pick the
		// later interpretation: an ambiguous timestamp must never make us
		// prune a record that is actually recent.
		var best time.Time
		if t, err := time.ParseInLocation(layout, s.LastExecution.At, time.Local); err == nil && t.After(best) {
			best = t
		}
		if t, err := time.Parse(layout, s.LastExecution.At); err == nil && t.After(best) {
			best = t
		}
		if !best.IsZero() {
			return best, true
		}
	}
	if s.ExpiresAt != nil {
		return *s.ExpiresAt, true
	}
	if s.TokenExpiresAt != nil {
		return *s.TokenExpiresAt, true
	}
	return time.Time{}, false
}

func reapCommand() *cobra.Command {
	var olderThan time.Duration
	var dryRun bool
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "reap",
		Short: "Unassign server runtimes that have no local session record",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if olderThan < 0 {
				return usagef("[colab] --older-than must not be negative.")
			}
			ctx := cmd.Context()
			client, err := d.ColabClient(ctx)
			if err != nil {
				return err
			}
			assignments, err := client.List(ctx)
			if err != nil {
				return err
			}
			local, err := d.Store.List()
			if err != nil {
				return err
			}
			report := reapReport{
				DryRun: dryRun, Orphans: []reapTarget{}, Stale: []string{},
				Unassigned: []reapTarget{}, Pruned: []string{}, Failed: []reapTarget{},
			}
			matched := make(map[string]bool, len(assignments))
			seenLocal := make(map[string]bool, len(local))
			for _, a := range assignments {
				for name, s := range local {
					if !state.MatchAssignment(s, a.Endpoint, a.RuntimeName) {
						continue
					}
					key := a.Endpoint + "\x00" + a.RuntimeName
					matched[key] = true
					seenLocal[name] = true
				}
			}
			for _, a := range assignments {
				key := a.Endpoint + "\x00" + a.RuntimeName
				if !matched[key] {
					report.Orphans = append(report.Orphans, reapTarget{Endpoint: a.Endpoint, RuntimeName: a.RuntimeName})
				}
			}
			staleNames := make([]string, 0, len(local))
			for name := range local {
				if !seenLocal[name] {
					staleNames = append(staleNames, name)
				}
			}
			sort.Strings(staleNames)
			now := reapNow()
			eligible := make([]string, 0, len(staleNames))
			for _, name := range staleNames {
				report.Stale = append(report.Stale, name)
				t, known := reapRecordTime(local[name])
				if olderThan <= 0 || (known && now.Sub(t) >= olderThan) {
					eligible = append(eligible, name)
				}
			}

			actions := len(report.Orphans) + len(eligible)
			if actions == 0 {
				fmt.Fprintln(d.Stderr, "[colab] Nothing to reap.")
				return writeReapReport(d, asJSON, &report)
			}
			if !dryRun && !assumeYes(cmd) {
				if !platform.IsStdinTTY() {
					return usagef("[colab] Refusing to reap without confirmation: stdin is not a TTY. " +
						"Re-run with --yes (or set COLAB_ASSUME_YES=1) to confirm. Use --dry-run to preview.")
				}
				fmt.Fprintf(d.Stderr, "[colab] Unassign %d orphan(s) and prune %d stale local record(s)? [y/N] ",
					len(report.Orphans), len(eligible))
				var answer string
				_, _ = fmt.Fscanln(d.Stdin, &answer)
				answer = strings.ToLower(strings.TrimSpace(answer))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(d.Stderr, "[colab] Aborted.")
					return nil
				}
			}
			if dryRun {
				fmt.Fprintf(d.Stderr, "[colab] dry run: would unassign %d orphan(s), prune %d stale local record(s).\n",
					len(report.Orphans), len(eligible))
				return writeReapReport(d, asJSON, &report)
			}

			// Re-read local state immediately before each mutation: a session
			// that gained a local record in the meantime is never unassigned.
			for _, target := range report.Orphans {
				fresh, err := d.Store.List()
				if err == nil {
					var hasRecord bool
					for _, s := range fresh {
						if state.MatchAssignment(s, target.Endpoint, target.RuntimeName) {
							hasRecord = true
							break
						}
					}
					if hasRecord {
						continue
					}
				}
				if err := client.UnassignRuntime(ctx, target.Endpoint, target.RuntimeName); err != nil {
					report.Failed = append(report.Failed, target)
					fmt.Fprintf(d.Stderr, "[colab] Failed to unassign %s: %s\n",
						target.Endpoint, logx.Redact(err.Error()))
					continue
				}
				report.Unassigned = append(report.Unassigned, target)
			}
			for _, name := range eligible {
				if err := d.PruneSession(name); err != nil {
					fmt.Fprintf(d.Stderr, "[colab] Failed to prune local record %s: %v\n", name, err)
					continue
				}
				report.Pruned = append(report.Pruned, name)
			}
			fmt.Fprintf(d.Stderr, "[colab] Reaped %d orphan assignment(s); pruned %d stale local record(s).\n",
				len(report.Unassigned), len(report.Pruned))
			if err := writeReapReport(d, asJSON, &report); err != nil {
				return err
			}
			if len(report.Failed) > 0 {
				return exitf(1, "[colab] Failed to unassign %d orphan(s).", len(report.Failed))
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&olderThan, "older-than", 2*time.Hour, "Prune stale local records older than this duration")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview without any mutation")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the reap result as JSON")
	return cmd
}

// writeReapReport puts structured results on stdout and all diagnostics on
// stderr (docs/03 §8). JSON is always written, even for an empty/dry run.
func writeReapReport(d *app.Deps, asJSON bool, r *reapReport) error {
	if asJSON {
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(d.Stdout, string(b))
		return nil
	}
	for _, t := range r.Unassigned {
		fmt.Fprintf(d.Stdout, "unassigned %s\n", t.Endpoint)
	}
	for _, name := range r.Pruned {
		fmt.Fprintf(d.Stdout, "pruned %s\n", name)
	}
	return nil
}
