package runtime

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any goroutine outlives the suite. The runtime
// drives websocket read/ping/hook loops (ws.go); a loop that survives its
// context would keep a fire-and-forget CLI process alive after its work is
// done (AUDIT-03 §4.2).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
