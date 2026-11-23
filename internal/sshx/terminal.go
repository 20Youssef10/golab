package sshx

import (
	"context"
	"os"

	"golang.org/x/term"
)

// rawState is an opaque handle to the saved terminal state. It is a struct so
// the hooks below can be swapped in tests without a real TTY (AGENTS.md #4).
type rawState struct {
	fd    int
	state *term.State
}

type rawHooks struct {
	makeRaw func(int) (*rawState, error)
	restore func(int, *rawState) error
	getSize func(int) (int, int, error)
}

// currentRawHooks is only mutated by tests (withRawHooks); production code
// always uses the x/term implementations.
var currentRawHooks = rawHooks{
	makeRaw: func(fd int) (*rawState, error) {
		st, err := term.MakeRaw(fd)
		if err != nil {
			return nil, err
		}
		return &rawState{fd: fd, state: st}, nil
	},
	restore: func(fd int, s *rawState) error {
		if s == nil || s.state == nil {
			return nil
		}
		return term.Restore(fd, s.state)
	},
	getSize: term.GetSize,
}

// enterRawMode puts fd into raw mode and returns a handle whose restore method
// must be called on every exit path (including panic and signals).
func enterRawMode(fd uintptr) (*rawState, error) {
	return currentRawHooks.makeRaw(int(fd))
}

func (s *rawState) restore() error {
	if s == nil {
		return nil
	}
	return currentRawHooks.restore(s.fd, s)
}

// terminalSize returns a sane default when the fd is not a terminal.
func terminalSize(fd uintptr) (width, height int) {
	w, h, err := currentRawHooks.getSize(int(fd))
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

type windowSize struct{ width, height int }

// startResizeWatcher forwards terminal size changes to out. On Unix it uses
// SIGWINCH; on Windows it polls the console every 250 ms (doc 04 §4).
func startResizeWatcher(ctx context.Context, fd uintptr, out chan<- windowSize) {
	resizeLoop(ctx, fd, out)
}

// rawFD returns the fd if stdin is a terminal that can be put into raw mode;
// otherwise it returns ok=false. x/term.MakeRaw works on Windows consoles too.
func rawFD(f *os.File) (uintptr, bool) {
	if f == nil {
		return 0, false
	}
	fd := f.Fd()
	if _, _, err := currentRawHooks.getSize(int(fd)); err != nil {
		return 0, false
	}
	return fd, true
}
