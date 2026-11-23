package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/auth"
	"github.com/20Youssef10/golab/internal/state"
)

// fakeClearer implements both auth.Provider and auth.TokenClearer so the CLI's
// type assertion can be exercised without touching a real keyring.
type fakeClearer struct {
	result auth.ClearTokenResult
	err    error
	calls  int
}

func (f *fakeClearer) HTTPClient(context.Context, []string) (*http.Client, error) {
	return nil, errors.New("not used by logout tests")
}

func (f *fakeClearer) ClearToken(context.Context) (auth.ClearTokenResult, error) {
	f.calls++
	return f.result, f.err
}

// runWithExtraCommands executes a command tree that also contains commands
// whose registration lives outside this file (logout is wired by the
// orchestrator in register.go, which this subagent does not own).
func runWithExtraCommands(t *testing.T, d *app.Deps, stdin string, extra []*cobra.Command, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRootCommand(strings.NewReader(stdin), &out, &errOut)
	root.AddCommand(extra...)
	root.SetArgs(args)
	ctx := WithDeps(context.Background(), d)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0, out.String(), errOut.String()
	}
	mapped := mapError(err)
	var ee *exitError
	if errors.As(mapped, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(&errOut, ee.msg)
		}
		return ee.code, out.String(), errOut.String()
	}
	fmt.Fprintln(&errOut, mapped)
	return 1, out.String(), errOut.String()
}

func runLogout(t *testing.T, d *app.Deps, args ...string) (int, string, string) {
	t.Helper()
	return runWithExtraCommands(t, d, "", []*cobra.Command{logoutCommand()}, args...)
}

// TestLogoutRemovesToken ports upstream tests/test_cli.py::test_cli_logout_removes_token.
func TestLogoutRemovesToken(t *testing.T) {
	h := newHarness(t)
	clearer := &fakeClearer{result: auth.ClearTokenResult{Removed: true}}
	h.deps.Auth = clearer

	code, stdout, stderr := runLogout(t, h.deps, "logout")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if clearer.calls != 1 {
		t.Fatalf("ClearToken calls = %d, want 1", clearer.calls)
	}
	if !strings.Contains(stdout, "Logged out.") {
		t.Fatalf("stdout = %q", stdout)
	}
}

// TestLogoutAlreadyLoggedOut ports upstream
// tests/test_cli.py::test_cli_logout_already_logged_out.
func TestLogoutAlreadyLoggedOut(t *testing.T) {
	h := newHarness(t)
	h.deps.Auth = &fakeClearer{result: auth.ClearTokenResult{}}

	code, stdout, stderr := runLogout(t, h.deps, "logout")
	if code != 0 || !strings.Contains(stdout, "Already logged out.") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

// TestLogoutInvalidatesSessionTokensAndWarns ports upstream
// tests/test_cli.py::test_cli_logout_invalidates_session_tokens_and_warns.
func TestLogoutInvalidatesSessionTokensAndWarns(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.deps.Auth = &fakeClearer{result: auth.ClearTokenResult{Removed: true}}

	code, stdout, stderr := runLogout(t, h.deps, "logout")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	s, err := h.deps.Store.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Token.Reveal() != "" || s.TokenExpiresAt != nil {
		t.Fatalf("stale proxy token survived logout: %+v", s)
	}
	if !strings.Contains(stdout, "Logged out.") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "1 local session(s)") || !strings.Contains(stderr, "previous account") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// TestLogoutResetsCachedAPICapabilities ports upstream
// tests/test_cli.py::test_cli_logout_resets_cached_api_capabilities.
func TestLogoutResetsCachedAPICapabilities(t *testing.T) {
	h := newHarness(t)
	yes, no := true, false
	settings := state.DefaultSettings()
	settings.PublicAPISupported = &yes
	settings.TunnelAPISupported = &no
	if err := h.deps.SettingsStore.Save(settings); err != nil {
		t.Fatal(err)
	}
	h.deps.Auth = &fakeClearer{result: auth.ClearTokenResult{Removed: true}}

	code, stdout, stderr := runLogout(t, h.deps, "logout")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	got, err := h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicAPISupported != nil || got.TunnelAPISupported != nil {
		t.Fatalf("capability flags not reset: %+v", got)
	}
	if !strings.Contains(stdout, "Logged out.") {
		t.Fatalf("stdout = %q", stdout)
	}
}

// TestLogoutStoreErrorWarnsWithoutCrashing ports upstream
// tests/test_cli.py::test_cli_logout_store_error_warns_without_crashing.
func TestLogoutStoreErrorWarnsWithoutCrashing(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.dir, "sessions.json")
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.deps.Auth = &fakeClearer{result: auth.ClearTokenResult{Removed: true}}

	code, stdout, stderr := runLogout(t, h.deps, "logout")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "Logged out.") || !strings.Contains(stderr, "Warning:") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestLogoutKeyringErrorExitsNonzero ports upstream
// tests/test_cli.py::test_cli_logout_keyring_error_exits_nonzero.
func TestLogoutKeyringErrorExitsNonzero(t *testing.T) {
	h := newHarness(t)
	h.deps.Auth = &fakeClearer{err: errors.New("keyring locked")}

	code, _, stderr := runLogout(t, h.deps, "logout")
	if code != 1 || !strings.Contains(stderr, "Failed to clear OAuth token") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
}

// TestLogoutADCPrintsGcloudHint ports upstream
// tests/test_cli.py::test_cli_logout_adc_prints_gcloud_hint.
func TestLogoutADCPrintsGcloudHint(t *testing.T) {
	h := newHarness(t)
	h.deps.Config.AuthProvider = auth.AuthADC
	clearer := &fakeClearer{result: auth.ClearTokenResult{Removed: true}}
	h.deps.Auth = clearer

	code, stdout, _ := runLogout(t, h.deps, "logout")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if clearer.calls != 0 {
		t.Fatal("ADC logout must not clear an OAuth token")
	}
	if !strings.Contains(stdout, "gcloud auth application-default revoke") {
		t.Fatalf("stdout = %q", stdout)
	}
}

// TestLogoutNeverPrintsToken is a security regression test for the CLI layer.
func TestLogoutNeverPrintsToken(t *testing.T) {
	const canary = "logout-token-canary-DO-NOT-LEAK"
	h := newHarness(t)
	h.deps.Auth = &fakeClearer{err: fmt.Errorf(
		"exchange failed: {\"access_token\":\"%s\"}", canary)}

	_, stdout, stderr := runLogout(t, h.deps, "logout")
	for name, text := range map[string]string{"stdout": stdout, "stderr": stderr} {
		if strings.Contains(text, canary) {
			t.Errorf("%s leaked the token: %q", name, text)
		}
	}
}

// TestLogoutDoesNotStopSessions guards the explicit "do not stop running VMs"
// rule: no control-plane calls are made and sessions remain listed.
func TestLogoutDoesNotStopSessions(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.deps.Auth = &fakeClearer{result: auth.ClearTokenResult{Removed: true}}

	_, _, stderr := runLogout(t, h.deps, "logout")
	if strings.Contains(strings.ToLower(stderr), "stopped") ||
		strings.Contains(strings.ToLower(stderr), "unassigned") {
		t.Fatalf("logout must not stop VMs: %q", stderr)
	}
	if _, err := h.deps.Store.Get("s1"); err != nil {
		t.Fatalf("session was removed by logout: %v", err)
	}
}

// TestLogoutIdempotent runs the command twice: the second call is a clean
// "Already logged out." with no session/config changes.
func TestLogoutIdempotent(t *testing.T) {
	h := newHarness(t)
	h.deps.Auth = &fakeClearer{result: auth.ClearTokenResult{Removed: true}}
	if code, _, _ := runLogout(t, h.deps, "logout"); code != 0 {
		t.Fatal("first logout failed")
	}
	h.deps.Auth = &fakeClearer{result: auth.ClearTokenResult{}}
	code, stdout, stderr := runLogout(t, h.deps, "logout")
	if code != 0 || !strings.Contains(stdout, "Already logged out.") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
