//go:build windows

package sshx

import (
	"golang.org/x/sys/windows"
)

const (
	enableVirtualTerminalInput  = 0x0200
	enableVirtualTerminalOutput = 0x0004
)

// enableVirtualTerminal turns on VT input/output for a Windows console and
// returns a function that restores the original modes on every exit path
// (doc 04 §4). On a non-console handle it is a no-op.
func enableVirtualTerminal(fd uintptr) func() {
	h := windows.Handle(fd)
	var inMode, outMode uint32
	if err := windows.GetConsoleMode(h, &inMode); err != nil {
		return func() {}
	}
	if err := windows.GetConsoleMode(windows.Handle(windows.Stdout), &outMode); err != nil {
		return func() {}
	}
	_ = windows.SetConsoleOutputCP(65001)
	_ = windows.SetConsoleMode(h, inMode|enableVirtualTerminalInput)
	_ = windows.SetConsoleMode(windows.Handle(windows.Stdout), outMode|enableVirtualTerminalOutput)
	return func() {
		_ = windows.SetConsoleMode(h, inMode)
		_ = windows.SetConsoleMode(windows.Handle(windows.Stdout), outMode)
	}
}
