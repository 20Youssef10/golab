package cli

// TIER 3 ADDITION (registration). The only shared-file change is one line in
// register.go calling RegisterTier3Commands. Everything else lives in new
// files, so ownership of root.go/run.go/session.go/state.go stays untouched.

import (
	"time"

	"github.com/spf13/cobra"
)

// RegisterTier3Commands wires the Tier 3 feature family. It is idempotent so a
// test harness that registers the same family twice cannot double-add flags.
func RegisterTier3Commands(root *cobra.Command) {
	addDetachToRun(root)
	addWatchToSync(root)
	addIfMissingTier3(root, "jobs", jobsCommand)
	addIfMissingTier3(root, "logs", logsCommand)
	addIfMissingTier3(root, "wait", waitCommand)
}

func findCommandTier3(root *cobra.Command, name string) *cobra.Command {
	if root == nil {
		return nil
	}
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func addIfMissingTier3(root *cobra.Command, name string, build func() *cobra.Command) {
	if findCommandTier3(root, name) == nil {
		root.AddCommand(build())
	}
}

// addDetachToRun wraps the existing `run` RunE: without --detach it delegates
// unchanged; with --detach it calls runDetached. No edit to run.go is needed.
func addDetachToRun(root *cobra.Command) {
	run := findCommandTier3(root, "run")
	if run == nil || run.Flags().Lookup("detach") != nil || run.RunE == nil {
		return
	}
	var detach bool
	var ttl time.Duration
	run.Flags().BoolVar(&detach, "detach", false,
		"Start the script detached on the VM (requires --ttl; see ADR-001)")
	run.Flags().DurationVar(&ttl, "ttl", 0,
		"Required deadline for a detached run (e.g. 1h) so a forgotten job cannot bill forever")
	original := run.RunE
	run.RunE = func(cmd *cobra.Command, args []string) error {
		if !detach {
			return original(cmd, args)
		}
		// --max-cu/--enforce sample CU in the foreground run loop. A detached
		// CLI returns after submitting one cell, so they would be silently
		// inactive. Reject them before any allocation instead (ADR-001).
		if cmd.Flags().Changed("max-cu") {
			return usagef("[colab] --max-cu applies to foreground runs only; 'golab run --detach' does not sample compute units. Remove --max-cu or drop --detach.")
		}
		if cmd.Flags().Changed("enforce") {
			return usagef("[colab] --enforce applies to foreground runs only (it acts on --max-cu); it has no effect with --detach.")
		}
		return runDetached(cmd, args, ttl)
	}
}

// addWatchToSync wraps the existing `sync` RunE with bounded polling. The
// sync engine itself is untouched; only an outer loop is added.
func addWatchToSync(root *cobra.Command) {
	syncCmd := findCommandTier3(root, "sync")
	if syncCmd == nil || syncCmd.Flags().Lookup("watch") != nil || syncCmd.RunE == nil {
		return
	}
	var watch bool
	var interval time.Duration
	var iterations int
	syncCmd.Flags().BoolVar(&watch, "watch", false, "Poll and re-run sync (no fsnotify; bounded)")
	syncCmd.Flags().DurationVar(&interval, "interval", time.Second, "Poll interval for --watch")
	syncCmd.Flags().IntVar(&iterations, "iterations", 60, "Maximum iterations for --watch (bounded)")
	original := syncCmd.RunE
	syncCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !watch {
			return original(cmd, args)
		}
		return syncWatch(cmd.Context(), interval, iterations, func() error {
			return original(cmd, args)
		})
	}
}
