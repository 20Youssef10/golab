package sshx

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any goroutine outlives the suite. SSH sessions
// start proxy-copy and resize-watcher goroutines (proxy.go, session.go); a
// survivor would keep the CLI alive after the session ends (AUDIT-03 §4.2).
func TestMain(m *testing.M) {
	// x/crypto/ssh's agent client starts a pipeline readLoop per connection;
	// its lifecycle belongs to x/crypto/ssh, not to golab, and it is observed
	// on macOS CI. Ignore ONLY that one library function; all other goroutines
	// are still checked.
	goleak.VerifyTestMain(m, goleak.IgnoreAnyFunction("golang.org/x/crypto/ssh/agent.(*pipeline).readLoop"))
}
