package sshx

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/20Youssef10/golab/internal/logx"
)

// Teardown wraps an idempotent teardown callback (--rm). It can be invoked
// concurrently from a signal handler and from the normal finally path; the
// callback runs at most once. Diagnostics go to stderr, never stdout, because
// in --proxy-mode stdout is the SSH byte stream.
type Teardown struct {
	once sync.Once
	fn   func(context.Context) error
	w    io.Writer
	err  error
}

// NewTeardown builds a Teardown. A nil fn is a no-op; a nil w writes to stderr.
func NewTeardown(fn func(context.Context) error, w io.Writer) *Teardown {
	if w == nil {
		w = os.Stderr
	}
	return &Teardown{fn: fn, w: w}
}

// Run invokes the callback at most once. Its error is reported as a stderr
// diagnostic and returned for callers that want it; it never masks the
// session's own exit code.
func (t *Teardown) Run(ctx context.Context) error {
	t.once.Do(func() {
		if t.fn == nil {
			return
		}
		t.err = t.fn(ctx)
		if t.err != nil {
			fmt.Fprintf(t.w, "[colab] --rm: failed to stop: %s\n",
				logx.Redact(t.err.Error()))
		}
	})
	return t.err
}

// InstallTerminationSignals routes SIGHUP/SIGTERM/SIGINT (and, on Windows,
// console-close events) to onSignal. OpenSSH ends a ProxyCommand with SIGHUP,
// so --rm must be driven through this path. The returned function stops the
// handlers and is safe to call repeatedly.
func InstallTerminationSignals(onSignal func(os.Signal)) func() {
	signals := make(chan os.Signal, 4)
	signalNotify(signals, terminationSignals()...)
	stopConsole := installConsoleClose(func() { onSignal(nil) })

	done := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			select {
			case s := <-signals:
				onSignal(s)
			case <-done:
				return
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(done)
			signalStop(signals)
			stopConsole()
		})
	}
}
