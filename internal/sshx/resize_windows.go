//go:build windows

package sshx

import (
	"context"
	"time"
)

// resizeLoop polls the Windows console every 250 ms (doc 04 §4; there is no
// SIGWINCH on Windows).
func resizeLoop(ctx context.Context, fd uintptr, out chan<- windowSize) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	last := windowSize{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w, h := terminalSize(fd)
			next := windowSize{w, h}
			if next == last {
				continue
			}
			last = next
			select {
			case out <- next:
			default:
			}
		}
	}
}
