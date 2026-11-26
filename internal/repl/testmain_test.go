package repl

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any goroutine outlives the suite. TTY mode
// starts line-editor and execution goroutines (tty.go); a survivor would keep
// the CLI alive after the REPL exits (AUDIT-03 §4.2).
func TestMain(m *testing.M) {
	// Chroma's regexp2 lexers use a shared fast clock: regexp2/v2 starts a
	// single runClock goroutine that periodically samples time while any
	// match has a deadline. That library-owned goroutine self-terminates once
	// clockEnd is reached (see regexp2/v2 fastclock.go), so it is intentional
	// and not owned by this package. Ignore ONLY that one function: its top
	// frame is time.Sleep, so IgnoreAnyFunction (rather than
	// IgnoreTopFunction) is required. Every other goroutine is still checked.
	goleak.VerifyTestMain(m, goleak.IgnoreAnyFunction("github.com/dlclark/regexp2/v2.runClock"))
}
