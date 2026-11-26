package cli

import "github.com/spf13/cobra"

// RegisterTier1Commands is the single additive registration hook for the
// Tier 1 spend-safety commands. It keeps register.go to a one-line change so
// concurrent work on that shared file stays conflict-free.
func RegisterTier1Commands(root *cobra.Command) {
	root.AddCommand(costCommand())
	root.AddCommand(reapCommand())
}
