package update

import (
	"testing"

	"go.uber.org/goleak"
)

// TestNoGoroutineLeaks keeps the update package under the goleak gate.
//
// Unlike runtime/sshx/repl, this package already has a TestMain in
// helpers_test.go (owned by another agent), so a second TestMain cannot be
// added here. A real VerifyNone check is used instead: it runs as part of
// `go test` and fails on any goroutine left behind by the tests in this
// package (AUDIT-03 §4.2). The update check spawns no background workers;
// its httptest servers are closed via t.Cleanup.
func TestNoGoroutineLeaks(t *testing.T) {
	goleak.VerifyNone(t)
}
