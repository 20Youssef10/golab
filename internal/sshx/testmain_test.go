package sshx

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any goroutine outlives the suite. SSH sessions
// start proxy-copy and resize-watcher goroutines (proxy.go, session.go); a
// survivor would keep the CLI alive after the session ends (AUDIT-03 §4.2).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
