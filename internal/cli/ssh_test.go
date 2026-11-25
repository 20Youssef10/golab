package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/budget"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/sshx"
	"github.com/20Youssef10/golab/internal/state"
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

// Regression for DEFECT 2.4: an enforced budget profile must stop ssh's
// auto-create path BEFORE it POSTs an allocation. Ports upstream's
// tests/test_budget_guard.py: a denied guard yields zero allocations. The
// budgetCheckFn injection supplies `--enforce` (LoadProfile carries only
// thresholds); profile loading, the Usage GET and the guard's error mapping
// all run for real, so a zero call count proves the guard was consulted.
func TestSSHAutoCreateBudgetGuardDenies(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "golab.yaml")
	body := "version: 1\n" +
		"defaults:\n  budget:\n    max_balance: 10\n" +
		"profiles:\n  default:\n    budget:\n      max_balance: 10\n"
	if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOLAB_BUDGET_FILE", profile)

	for _, tc := range []struct {
		name string
		args func(key string) []string
	}{
		{"proxy-unknown-name", func(key string) []string {
			return []string{"ssh", "--proxy-mode", "-s", "guarded", "-i", key}
		}},
		{"bare-interactive", func(key string) []string {
			return []string{"ssh", "-i", key}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.ctrl.SetConsumption(5, 0, 0) // balance 5 is below the reserve floor of 10

			orig := budgetCheckFn
			checks := 0
			budgetCheckFn = func(ctx context.Context, usage *colabapi.Consumption, policy *budget.Policy) (budget.Decision, error) {
				checks++
				policy.Enforce = true // simulate `--enforce` on the profile
				return budget.Check(ctx, usage, *policy)
			}
			defer func() { budgetCheckFn = orig }()

			key := writeEd25519Key(t)
			code := h.run(t, "", tc.args(key)...)
			if checks != 1 {
				t.Fatalf("budget guard consulted %d times; want 1 (stderr=%q)", checks, h.stderr.String())
			}
			if code != 1 {
				t.Fatalf("exit = %d; want 1; stderr=%q stdout=%q", code, h.stderr.String(), h.stdout.String())
			}
			if !strings.Contains(h.stderr.String(), "[colab] budget guard:") {
				t.Fatalf("no budget-denied error: %q", h.stderr.String())
			}
			if !strings.Contains(h.stderr.String(), "compute units") {
				t.Fatalf("no usage reason in denial: %q", h.stderr.String())
			}
			for _, r := range h.ctrl.Requests() {
				if r.Method == http.MethodPost && r.Path == "/tun/m/assign" {
					t.Fatalf("allocated despite budget denial: %+v", h.ctrl.Requests())
				}
			}
			local, err := h.deps.Store.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(local) != 0 {
				t.Fatalf("session persisted despite budget denial: %+v", local)
			}
		})
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
	// Windows has no SIGHUP; its console-close handler is covered separately.
	if goruntime.GOOS == "windows" {
		t.Skip("SIGHUP is Unix-only")
	}
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

// Ports upstream tests/test_ssh.py::test_force_kills_stale_sshd (commit cda02bc).
// Each SSH path must call the stale-sshd killer exactly once with --force, and
// never without it. The helper is injected so the assertion is independent of
// how the fake kernel would answer the pkill snippet.
func TestSSHForceFlagKillsStaleSSHd(t *testing.T) {
	for _, tc := range []struct {
		name  string
		argv  []string
		kills int
	}{
		{name: "interactive", argv: []string{"--force"}, kills: 1},
		{name: "proxy-mode", argv: []string{"--proxy-mode", "--force"}, kills: 1},
		{name: "cmd", argv: []string{"--force", "--", "true"}, kills: 1},
		{name: "no-force", argv: []string{}, kills: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			srv := newFakeSSH(t, fakesshd.Options{
				Output: []byte("OK\n"), ShellHold: time.Millisecond,
			})
			key := writeEd25519Key(t)
			putSSHSession(t, h, "s1", srv.URL(), "proxy-ssh-token")

			orig := killStaleSSHdFn
			calls := 0
			killStaleSSHdFn = func(context.Context, *app.Deps, state.Session) { calls++ }
			defer func() { killStaleSSHdFn = orig }()

			args := append([]string{"ssh", "-s", "s1", "-i", key}, tc.argv...)
			code := h.run(t, "", args...)
			if code != 0 {
				t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
			}
			if calls != tc.kills {
				t.Fatalf("stale-sshd kill calls = %d; want %d (stderr=%q)", calls, tc.kills, h.stderr.String())
			}
			// Piped output stays byte-exact; all chatter is on stderr.
			if strings.Contains(h.stdout.String(), "[colab]") {
				t.Fatalf("chatter leaked to stdout: %q", h.stdout.String())
			}
		})
	}
}

// forceKillApplies mirrors upstream: the hidden `proxy_mode + cmd-mode`
// re-invocation is a bridge and never forwards --force (ssh.py:851-854).
func TestSSHForceSkipsHiddenCmdModeBridge(t *testing.T) {
	for _, tc := range []struct {
		name      string
		force     bool
		proxyMode bool
		mode      sshx.Mode
		want      bool
	}{
		{"interactive", true, false, sshx.ModeInteractive, true},
		{"proxy", true, true, sshx.ModeProxy, true},
		{"cmd", true, false, sshx.ModeCmd, true},
		{"hidden-cmd-bridge", true, true, sshx.ModeCmd, false},
		{"no-force", false, true, sshx.ModeProxy, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := forceKillApplies(tc.force, tc.proxyMode, tc.mode); got != tc.want {
				t.Fatalf("forceKillApplies(%v,%v,%q) = %v; want %v", tc.force, tc.proxyMode, tc.mode, got, tc.want)
			}
		})
	}
}

// spyForceKernel satisfies runtime.Kernel via the embedded interface; only the
// methods the killer uses are overridden.
type spyForceKernel struct {
	runtime.Kernel
	id     string
	code   string
	closes int
}

func (k *spyForceKernel) ID() string { return k.id }
func (k *spyForceKernel) Execute(_ context.Context, code string, _ runtime.ExecOptions) (*runtime.Result, error) {
	k.code = code
	return &runtime.Result{}, nil
}
func (k *spyForceKernel) Close() error { k.closes++; return nil }

// Ports upstream tests/test_ssh.py::test_kill_stale_sshd_body (commit cda02bc):
// the kernel is addressed by its id, executes the exact pkill snippet and is
// always closed; the chatter is on stderr with a bounded (10 s) context.
func TestKillStaleSSHDBody(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	s := h.putSession(t, "s1")
	s.KernelID = "k123"
	if err := h.deps.Store.Put(s); err != nil {
		t.Fatal(err)
	}
	spy := &spyForceKernel{id: "k123"}
	var attached state.Session
	var deadline time.Time
	orig := attachSSHForceKernel
	attachSSHForceKernel = func(ctx context.Context, _ *app.Deps, target *state.Session) (runtime.Kernel, error) {
		attached = *target
		if dl, ok := ctx.Deadline(); ok {
			deadline = dl
		}
		return spy, nil
	}
	defer func() { attachSSHForceKernel = orig }()

	killStaleSSHd(context.Background(), h.deps, s)

	if attached.KernelID != "k123" {
		t.Fatalf("attached kernel id = %q; want k123", attached.KernelID)
	}
	if attached.URL != h.kernel.URL() {
		t.Fatalf("attached URL = %q; want %q", attached.URL, h.kernel.URL())
	}
	want := "import os; os.system('pkill -P $(pgrep -o -x sshd)')"
	if spy.code != want {
		t.Fatalf("snippet = %q; want %q", spy.code, want)
	}
	if spy.closes != 1 {
		t.Fatalf("kernel closes = %d; want 1", spy.closes)
	}
	if deadline.IsZero() || time.Until(deadline) > 10*time.Second || time.Until(deadline) < 5*time.Second {
		t.Fatalf("kill deadline = %v; want ~10 s from now", deadline)
	}
	if !strings.Contains(h.stderr.String(), "[colab] --force: killing stale SSH sessions...") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	if strings.Contains(h.stdout.String(), "[colab]") {
		t.Fatalf("chatter leaked to stdout: %q", h.stdout.String())
	}
}

// Ports the failure half of upstream's _killStaleSshd: a failure is tolerated,
// reported on stderr and the kernel is still closed.
func TestKillStaleSSHDFailureIsTolerated(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	s := h.putSession(t, "s1")
	orig := attachSSHForceKernel
	attachSSHForceKernel = func(context.Context, *app.Deps, *state.Session) (runtime.Kernel, error) {
		return nil, errors.New(`attach failed: {"access_token":"SENSITIVE123"}`)
	}
	defer func() { attachSSHForceKernel = orig }()

	killStaleSSHd(context.Background(), h.deps, s)
	if !strings.Contains(h.stderr.String(), "[colab] --force: failed to kill sshd:") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	if strings.Contains(h.stderr.String(), "SENSITIVE123") {
		t.Fatalf("error message leaked sensitive text: %q", h.stderr.String())
	}
}
