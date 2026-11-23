//go:build !windows

package sshx

// enableVirtualTerminal is a no-op on Unix: x/term.MakeRaw is sufficient and
// terminals there already understand ANSI escape sequences.
func enableVirtualTerminal(uintptr) func() { return func() {} }
