//go:build !windows

package sshx

import (
	"os"
	"os/signal"
	"syscall"
)

func signalNotify(ch chan<- os.Signal, sigs ...os.Signal) { signal.Notify(ch, sigs...) }
func signalStop(ch chan<- os.Signal)                      { signal.Stop(ch) }

func terminationSignals() []os.Signal {
	return []os.Signal{syscall.SIGHUP, syscall.SIGTERM, os.Interrupt}
}

// There is no console-close event on Unix; SIGHUP covers terminal closure.
func installConsoleClose(func()) func() { return func() {} }
