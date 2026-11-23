package sshx

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/testutil/fakesshd"
)

// Ports upstream tests/test_ssh_workdir.py::test_interactive_ssh_builds_args
// and doc 04 §4 (PTY + resize). The fake sshd records pty-req/window-change/env.
func TestInteractiveRequestsPTYAndWorkingDir(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{
		Banner:    []byte("SHELL-READY\n"),
		ShellHold: 30 * 1e6,
	})
	var out bytes.Buffer
	rc, err := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeInteractive,
		Identity: writeKey(t), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &bytes.Buffer{},
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if out.String() != "SHELL-READY\n" {
		t.Fatalf("stdout = %q; want banner byte-exactly", out.String())
	}
	var pty, shell bool
	var shellCmd string
	for _, ev := range srv.Events() {
		switch ev.Type {
		case "pty-req":
			pty = true
		case "shell":
			shell = true
		case "exec":
			shellCmd = ev.Command
		}
	}
	if !pty {
		t.Fatalf("no pty-req: %+v", srv.Events())
	}
	if !shell {
		t.Fatalf("no shell request: %+v", srv.Events())
	}
	if !strings.Contains(shellCmd, "cd /content 2>/dev/null; exec ${SHELL:-/bin/bash} -l") {
		t.Fatalf("remote shell command = %q", shellCmd)
	}
}

// Ports doc 04 §4 and R-07: window changes are forwarded to the remote PTY.
func TestWindowChangeForwarded(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{Banner: []byte("x"), ShellHold: 50 * 1e6})
	_, err := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeInteractive,
		Identity: writeKey(t), Stdin: strings.NewReader("exit\n"), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var changes []fakesshd.Event
	for _, ev := range srv.Events() {
		if ev.Type == "window-change" {
			changes = append(changes, ev)
		}
	}
	if len(changes) == 0 {
		t.Fatalf("no window-change requests recorded: %+v", srv.Events())
	}
	if changes[0].Width <= 0 || changes[0].Height <= 0 {
		t.Fatalf("invalid window size: %+v", changes[0])
	}
}

// Ports doc 04 §4: raw mode must be restored on every exit path, including panic.
func TestRawModeRestoredOnPanic(t *testing.T) {
	var entered, restored int
	withRawHooks(t, rawHooks{
		makeRaw: func(int) (*rawState, error) { entered++; return &rawState{}, nil },
		restore: func(int, *rawState) error { restored++; return nil },
		getSize: func(int) (int, int, error) { return 80, 24, nil },
	}, func() {
		func() {
			defer func() { _ = recover() }() // the deferred restore must still run
			s, err := enterRawMode(0)
			if err != nil {
				t.Fatal(err)
			}
			defer s.restore()
			panic("simulated panic")
		}()
	})
	if entered != 1 || restored != 1 {
		t.Fatalf("entered=%d restored=%d; want 1/1", entered, restored)
	}
}

// Ports doc 04 §4: Ctrl-C (SIGINT) restores console modes before exit. The
// termination signal manager invokes the registered restore callback.
func TestRawModeRestoredOnSignal(t *testing.T) {
	var restored atomic.Int32
	withRawHooks(t, rawHooks{
		makeRaw: func(int) (*rawState, error) { return &rawState{}, nil },
		restore: func(int, *rawState) error { restored.Add(1); return nil },
		getSize: func(int) (int, int, error) { return 80, 24, nil },
	}, func() {
		s, err := enterRawMode(0)
		if err != nil {
			t.Fatal(err)
		}
		stop := InstallTerminationSignals(func(os.Signal) { _ = s.restore() })
		defer stop()
		s.restore()
	})
	if restored.Load() < 1 {
		t.Fatal("console modes not restored")
	}
}
