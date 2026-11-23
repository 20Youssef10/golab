package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runNewCommand executes a command family owned by this work package without
// editing the shared register.go. The orchestrator adds the same Register*
// calls to registerCommands when integrating the new commands.
func runNewCommand(t *testing.T, h *testHarness, args ...string) int {
	t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	root := NewRootCommand(strings.NewReader(""), &h.stdout, &h.stderr)
	// Idempotent so the tests still work once the orchestrator registers the
	// same commands in the shared register.go.
	addIfMissing(root, "doctor", RegisterDoctorCommands)
	addIfMissing(root, "audit", RegisterAuditCommands)
	addIfMissing(root, "budget", RegisterBudgetCommands)
	addIfMissing(root, "bundle", RegisterBundleCommands)
	addIfMissing(root, "agentout", RegisterAgentOutCommands)
	addIfMissing(root, "sync", RegisterSyncCommands)
	root.SetArgs(args)
	err := root.ExecuteContext(WithDeps(context.Background(), h.deps))
	if err == nil {
		return 0
	}
	mapped := mapError(err)
	var ee *exitError
	if !asExitError(mapped, &ee) {
		fmt.Fprintln(&h.stderr, mapped)
		return 1
	}
	if ee.msg != "" {
		fmt.Fprintln(&h.stderr, ee.msg)
	}
	return ee.code
}

func addIfMissing(root *cobra.Command, name string, register func(*cobra.Command)) {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return
		}
	}
	register(root)
}
