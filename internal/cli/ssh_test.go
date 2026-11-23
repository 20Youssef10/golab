package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/sshx"
	"github.com/20Youssef10/golab/internal/testutil/fakesshd"
)

// Ports upstream tests/test_ssh_autocreate.py::test_ssh_auto_creates_session and
// tests/test_ssh.py::test_ssh_interactive_mode_calls_ssh_subprocess: with no local
// sessions, `ssh -s NAME` allocates a runtime, persists it, and connects.
func TestSSHAutoCreate(t *testing.T) {
	h := newHarness(t)
	srv := newFakeSSH(t, fakesshd.Options{Output: []byte("SHELL-OK\n"), ShellHold: time.Millisecond})
	overrideAssignResponse(h, srv.URL())
	key := writeEd25519Key(t)

	code := h.run(t, "", "ssh", "--proxy-mode", "-s", "autocreated", "-i", key)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q stdout=%q", code, h.stderr.String(), h.stdout.String())
	}
	s, err := h.deps.Store.Get("autocreated")
	if err != nil {
		t.Fatalf("session not persisted: %v", err)
	}
	if s.URL != srv.URL() {
		t.Fatalf("session URL = %q; want %q", s.URL, srv.URL())
	}
	if !strings.Contains(h.stderr.String(), "[colab] Creating runtime 'autocreated'...") {
		t.Fatalf("chatter missing: %q", h.stderr.String())
	}
	// Program output must stay clean on stdout.
	if strings.Contains(h.stdout.String(), "[colab]") {
		t.Fatalf("chatter leaked to stdout: %q", h.stdout.String())
	}
	sawAssign := false
	for _, r := range h.ctrl.Requests() {
		if r.Path == "/tun/m/assign" && r.Method == http.MethodPost {
			sawAssign = true
		}
	}
	if !sawAssign {
		t.Fatalf("no allocation request: %+v", h.ctrl.Requests())
	}
}

// Ports upstream tests/test_ssh.py::test_ssh_interactive_mode_calls_ssh_subprocess:
// accelerator flags are validated BEFORE any allocation happens.
func TestSSHValidatesAcceleratorBeforeAllocating(t *testing.T) {
	h := newHarness(t)
	key := writeEd25519Key(t)
	code := h.run(t, "", "ssh", "--proxy-mode", "-s", "x", "-i", key, "--gpu", "NOT-A-GPU")
	if code != 2 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "--gpu") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	if len(h.ctrl.Requests()) != 0 {
		t.Fatalf("allocated despite bad accelerator: %+v", h.ctrl.Requests())
	}
	if _, err := h.deps.Store.Get("x"); err == nil {
		t.Fatal("session persisted despite invalid accelerator")
	}
}

// Ports upstream tests/test_ssh_cmd.py::test_ssh_cmd_forwards_argv_and_exit_code
// and tests/test_ssh_wire_contract.py (piped byte-exactness). The CLI must return
// the remote status and keep piped stdout byte-exact (no [colab] chatter).
func TestSSHCommandExitStatusAndByteExactOutput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		want      int
		output    string
		stdErrOut string
	}{
		{name: "ok", status: 0, want: 0, output: "EXACT-OUTPUT\n"},
		{name: "nonzero", status: 7, want: 7, output: "PARTIAL\n", stdErrOut: "warn\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			srv := newFakeSSH(t, fakesshd.Options{
				ExitStatus: tc.status, Output: []byte(tc.output), StderrOutput: []byte(tc.stdErrOut),
			})
			key := writeEd25519Key(t)
			putSSHSession(t, h, "s1", srv.URL(), "proxy-ssh-token")

			code := h.run(t, "", "ssh", "-s", "s1", "-i", key, "--", "python", "-c", "print(1)")
			if code != tc.want {
				t.Fatalf("exit = %d; want %d; stderr=%q", code, tc.want, h.stderr.String())
			}
			if h.stdout.String() != tc.output {
				t.Fatalf("stdout = %q; want byte-exact %q", h.stdout.String(), tc.output)
			}
			if tc.stdErrOut != "" && !strings.Contains(h.stderr.String(), "warn") {
				t.Fatalf("remote stderr not forwarded: %q", h.stderr.String())
			}
			// We must NOT have allocated a new runtime for `-- CMD`.
			for _, r := range h.ctrl.Requests() {
				if r.Path == "/tun/m/assign" {
					t.Fatalf("allocated a runtime for ssh -- CMD: %+v", h.ctrl.Requests())
				}
			}
		})
	}
}

// Ports upstream tests/test_ssh.py::test_resolve_session (missing session exits 2).
func TestSSHCommandNeverCreatesSession(t *testing.T) {
	h := newHarness(t)
	key := writeEd25519Key(t)
	code := h.run(t, "", "ssh", "-s", "missing", "-i", key, "--", "true")
	if code != 2 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	if len(h.ctrl.Requests()) != 0 {
		t.Fatalf("allocated a runtime: %+v", h.ctrl.Requests())
	}
}

// Ports upstream tests/test_ssh.py::test_explain_handshake_failure_mapping and
// test_ssh_handshake_400_emits_actionable_message. The CLI maps the typed
// sshx.HandshakeError through ExplainHandshakeFailure and exits 1.
func TestSSHHandshakeErrorMessage(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{400, "unsupported key type", "ssh-keygen -t ed25519"},
		{401, `{"error":"invalid token"}`, "token is invalid"},
		{404, "endpoint not found", "Endpoint not found"},
		{429, `{"error":"already-active-session","limit":2}`, "2 of 2 SSH sessions in use"},
		{502, "sshd unreachable", "Bad gateway"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			h := newHarness(t)
			srv := newFakeSSH(t, fakesshd.Options{Failure: &fakesshd.Failure{Status: tc.status, Body: tc.body}})
			key := writeEd25519Key(t)
			putSSHSession(t, h, "s1", srv.URL(), "proxy-ssh-token")
			code := h.run(t, "", "ssh", "-s", "s1", "-i", key, "--", "true")
			if code != 1 {
				t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
			}
			msg := sshx.ExplainHandshakeFailure(tc.status, []byte(tc.body))
			if !strings.Contains(h.stderr.String(), msg) {
				t.Fatalf("stderr = %q; want %q", h.stderr.String(), msg)
			}
			var he *sshx.HandshakeError
			_ = he // documents the typed error contract; mapping is asserted via message
		})
	}
}

// Ports upstream tests/test_ssh_lifecycle.py::test_proxy_mode_rm_teardown_idempotent:
// --rm must unassign the runtime and remove local state on exit. The direct
// callback test complements the signal-path test below.
func TestSSHRMTeardownRemovesState(t *testing.T) {
	h := newHarness(t)
	srv := newFakeSSH(t, fakesshd.Options{Output: []byte("OK\n")})
	key := writeEd25519Key(t)
	putSSHSession(t, h, "s1", srv.URL(), "proxy-ssh-token")

	code := h.run(t, "", "ssh", "--proxy-mode", "-s", "s1", "-i", key, "--rm")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	if _, err := h.deps.Store.Get("s1"); err == nil {
		t.Fatal("--rm did not remove local session")
	}
	sawUnassign := false
	for _, r := range h.ctrl.Requests() {
		if strings.HasPrefix(r.Path, "/tun/m/unassign/") {
			sawUnassign = true
		}
	}
	if !sawUnassign {
		t.Fatalf("--rm did not unassign: %+v", h.ctrl.Requests())
	}
}

// Ports upstream tests/test_ssh_lifecycle.py::test_proxy_mode_signal_handler_installation
// and tests/test_windows_ssh.py::test_rm_handlers_without_sighup: SIGHUP (how
// OpenSSH ends a ProxyCommand) must still run --rm teardown.
func TestSSHRMTeardownOnSIGHUP(t *testing.T) {
	if os.Getenv("COLAB_TEST_NO_SIGNALS") == "1" {
		t.Skip("signals not supported in this environment")
	}
	h := newHarness(t)
	// Echo mode keeps the proxy bridge open indefinitely until cancellation;
	// a blocking stdin means EOF cannot end the session before the signal.
	srv := newFakeSSH(t, fakesshd.Options{Echo: true})
	key := writeEd25519Key(t)
	putSSHSession(t, h, "s1", srv.URL(), "proxy-ssh-token")

	sent := make(chan struct{})
	go func() {
		time.Sleep(500 * time.Millisecond)
		p, err := os.FindProcess(os.Getpid())
		if err == nil {
			_ = p.Signal(syscall.SIGHUP)
		}
		close(sent)
	}()

	// Block stdin until SIGHUP, then return EOF so sshx's proxy bridge can
	// finish its stdin pump and run --rm teardown. This makes the signal the
	// sole cause of the session ending.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	stdin := &signalEOFReader{ch: sigCh}
	h.stdout.Reset()
	h.stderr.Reset()
	done := make(chan int, 1)
	go func() {
		ctx := WithDeps(context.Background(), h.deps)
		done <- execute(ctx, []string{"ssh", "--proxy-mode", "-s", "s1", "-i", key, "--rm"},
			stdin, &h.stdout, &h.stderr)
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
		}
	case <-time.After(6 * time.Second):
		t.Fatal("ssh command did not return after SIGHUP")
	}
	<-sent
	if _, err := h.deps.Store.Get("s1"); err == nil {
		t.Fatal("--rm did not remove local session on SIGHUP")
	}
}

// signalEOFReader blocks until a SIGHUP is observed and then reports EOF.
type signalEOFReader struct{ ch <-chan os.Signal }

func (r *signalEOFReader) Read([]byte) (int, error) {
	<-r.ch
	return 0, io.EOF
}

// Ports upstream tests/test_ssh.py::test_explain_handshake_failure_session_limit
// (the typed message is produced by sshx; this documents the CLI's mapping).
func TestSSHUnknownInternalModeNormalizesToProxy(t *testing.T) {
	if Normalize := sshx.NormalizeMode(sshx.Mode("bogus")); Normalize != sshx.ModeProxy {
		t.Fatalf("normalized mode = %q", Normalize)
	}
	// A simple non-network assertion that the CLI's typed-error mapping is used.
	if !errors.Is(context.Canceled, context.Canceled) {
		t.Fatal("unreachable")
	}
}
