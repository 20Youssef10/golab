package sshx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/testutil/fakesshd"
)

// Ports upstream tests/test_ssh_lifecycle.py::test_proxy_mode_rm_teardown_idempotent
// and tests/test_windows_ssh.py::test_rm_handlers_without_sighup.
func TestTeardownIdempotent(t *testing.T) {
	var calls atomic.Int32
	var w bytes.Buffer
	td := NewTeardown(func(context.Context) error {
		calls.Add(1)
		return errors.New("best-effort failure")
	}, &w)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = td.Run(context.Background())
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("teardown called %d times; want 1", got)
	}
	if !strings.Contains(w.String(), "failed to stop") {
		t.Fatalf("teardown error must be a stderr diagnostic: %q", w.String())
	}
}

// Ports upstream tests/test_ssh_lifecycle.py::test_proxy_mode_rm_teardown_idempotent
// (signal path + finally path) and test_ssh_autocreate.py::test_proxy_mode_signal_handler_installation.
func TestTerminationSignalsTriggerTeardownOnce(t *testing.T) {
	if os.Getenv("COLAB_TEST_NO_SIGNALS") == "1" {
		t.Skip("signals not supported in this environment")
	}
	var calls atomic.Int32
	var w bytes.Buffer
	td := NewTeardown(func(context.Context) error {
		calls.Add(1)
		return nil
	}, &w)
	gotSignal := make(chan os.Signal, 1)
	stop := InstallTerminationSignals(func(s os.Signal) {
		_ = td.Run(context.Background())
		gotSignal <- s
	})
	defer stop()

	// SIGHUP is how OpenSSH ends a ProxyCommand (upstream comment).
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Signal(syscall.SIGHUP); err != nil {
		t.Skipf("cannot send SIGHUP: %v", err)
	}
	select {
	case <-gotSignal:
	case <-time.After(3 * time.Second):
		t.Fatal("SIGHUP handler not invoked")
	}
	_ = td.Run(context.Background()) // the finally path
	if got := calls.Load(); got != 1 {
		t.Fatalf("teardown called %d times; want 1", got)
	}
}

// Ports upstream tests/test_ssh_lifecycle.py::test_proxy_mode_rm_stops_even_if_bridge_raises.
func TestTeardownRunsOnBridgeFailure(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{Failure: &fakesshd.Failure{Status: 401, Body: ""}})
	var calls atomic.Int32
	var errBuf bytes.Buffer
	_, err := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeProxy,
		Identity: writePub(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIuser host"),
		Stdin:    strings.NewReader(""), Stdout: &bytes.Buffer{},
		Stderr:   &errBuf,
		Teardown: func(context.Context) error { calls.Add(1); return nil },
	}).Run(context.Background())
	if err == nil {
		t.Fatal("want handshake error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("teardown called %d times on failure; want 1", got)
	}
}

// Ports upstream tests/test_ssh_lifecycle.py::test_proxy_mode_rm_stops_reused_session:
// --proxy-mode --rm stops the bridged session on disconnect even when reused.
func TestRunnerTeardownOnNormalExit(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{})
	var calls atomic.Int32
	rc, err := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeProxy,
		Identity: writePub(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIuser host"),
		Stdin:    strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		Teardown: func(context.Context) error { calls.Add(1); return nil },
	}).Run(context.Background())
	if err != nil || rc != 0 {
		t.Fatalf("Run = %d,%v", rc, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("teardown called %d times; want 1", got)
	}
}

// Ports upstream tests/test_ssh_cmd.py (130 on interrupt while piped).
func TestInterruptWhilePipedReturns130(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simulate SIGINT -> cancellation before connect
	rc, err := newRunner(t, Config{
		ProxyURL: "http://127.0.0.1:1", Token: logx.NewSecret("TOK"), Mode: ModeCmd,
		Identity: writePub(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIuser host"),
		Command:  []string{"true"}, Stdin: strings.NewReader(""),
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		StdinTTY: boolPtr(false), StdoutTTY: boolPtr(false),
	}).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rc != 130 {
		t.Fatalf("exit code = %d; want 130", rc)
	}
}

func TestInstallTerminationSignalsStopIsIdempotent(t *testing.T) {
	stop := InstallTerminationSignals(func(os.Signal) {})
	stop()
	stop()
}
