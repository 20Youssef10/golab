//go:build !windows

package sshx

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// resizeLoop uses SIGWINCH on Unix (Linux, macOS, Termux).
func resizeLoop(ctx context.Context, fd uintptr, out chan<- windowSize) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			w, h := terminalSize(fd)
			select {
			case out <- windowSize{w, h}:
			default:
			}
		}
	}
}
