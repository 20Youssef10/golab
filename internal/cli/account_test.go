package cli

// No upstream Python port: named accounts are a golab extension. These tests
// use the in-memory harness and injected seams so no OAuth consent, keyring or
// network access is involved.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/account"
	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/auth"
)

const accountTestToken = "SECRET-ACCOUNT-TOKEN-DO-NOT-PRINT"

func testAccountRegistry(t *testing.T, h *testHarness) *account.Registry {
	t.Helper()
	r, err := account.New(filepath.Join(h.dir, account.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	return r
}

func stubAccountSeams(t *testing.T, signIn func(name string) error, clear func(name string) error) {
	t.Helper()
	oldSignIn, oldClear := accountSignIn, accountClearToken
	accountSignIn = func(_ context.Context, _ *app.Deps, name string) error { return signIn(name) }
	accountClearToken = func(_ context.Context, _ *app.Deps, name string) error { return clear(name) }
	t.Cleanup(func() { accountSignIn, accountClearToken = oldSignIn, oldClear })
}

func TestAccountCommandsAreRegistered(t *testing.T) {
	root := NewRootCommand(strings.NewReader(""), &strings.Builder{}, &strings.Builder{})
	if root.PersistentFlags().Lookup("account") == nil {
		t.Error("root is missing the --account flag")
	}
	cmd, _, err := root.Find([]string{"account"})
	if err != nil || cmd == nil || cmd.Name() != "account" {
		t.Fatalf("account command not registered: cmd=%v err=%v", cmd, err)
	}
	for _, name := range []string{"add", "list", "use", "remove", "show"} {
		if sub, _, err := cmd.Find([]string{name}); err != nil || sub == nil || sub.Name() != name {
			t.Errorf("account %s not registered: %v", name, err)
		}
	}
}

func TestAccountListAndShowAreRedactedJSON(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.dir, "oauth_token.json"), []byte(accountTestToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run(t, "", "account", "list", "--json"); code != 0 {
		t.Fatalf("account list --json exit = %d, stderr=%q", code, h.stderr.String())
	}
	var list struct {
		Active   string            `json:"active"`
		Accounts []account.Profile `json:"accounts"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &list); err != nil {
		t.Fatalf("invalid JSON %q: %v", h.stdout.String(), err)
	}
	if list.Active != account.DefaultName || len(list.Accounts) != 1 || list.Accounts[0].Name != account.DefaultName {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(h.stdout.String(), accountTestToken) || strings.Contains(h.stderr.String(), accountTestToken) {
		t.Error("account list leaked the token")
	}

	if code := h.run(t, "", "account", "show", "--json"); code != 0 {
		t.Fatalf("account show --json exit = %d, stderr=%q", code, h.stderr.String())
	}
	var shown accountShowJSON
	if err := json.Unmarshal(h.stdout.Bytes(), &shown); err != nil {
		t.Fatalf("invalid show JSON %q: %v", h.stdout.String(), err)
	}
	if shown.Name != account.DefaultName || !shown.Active {
		t.Errorf("show JSON = %+v", shown)
	}
	if strings.Contains(h.stdout.String(), accountTestToken) || strings.Contains(h.stderr.String(), accountTestToken) {
		t.Error("account show leaked the token")
	}
}

func TestAccountAddUseAndRemove(t *testing.T) {
	h := newHarness(t)
	var signedIn, cleared []string
	stubAccountSeams(t,
		func(name string) error { signedIn = append(signedIn, name); return nil },
		func(name string) error { cleared = append(cleared, name); return nil },
	)
	r := testAccountRegistry(t, h)

	if code := h.run(t, "", "account", "add", "alice", "--email", "alice@example.com"); code != 0 {
		t.Fatalf("account add exit = %d, stderr=%q", code, h.stderr.String())
	}
	if len(signedIn) != 1 || signedIn[0] != "alice" {
		t.Errorf("sign-in seam calls = %v, want [alice]", signedIn)
	}
	if !strings.Contains(h.stderr.String(), "human-only") {
		t.Errorf("add did not report human-only OAuth: %q", h.stderr.String())
	}
	p, err := r.Get("alice")
	if err != nil || p.Email != "alice@example.com" {
		t.Fatalf("alice profile = %+v, %v", p, err)
	}
	// add does not silently switch the active account.
	if active, err := r.Active(); err != nil || active != account.DefaultName {
		t.Fatalf("active after add = %q, %v", active, err)
	}

	if code := h.run(t, "", "account", "use", "alice"); code != 0 {
		t.Fatalf("account use exit = %d, stderr=%q", code, h.stderr.String())
	}
	if active, err := r.Active(); err != nil || active != "alice" {
		t.Fatalf("active after use = %q, %v", active, err)
	}

	// The active account needs an explicit override.
	if code := h.run(t, "", "account", "remove", "alice"); code != 2 {
		t.Fatalf("remove active exit = %d, want 2; stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "--force") {
		t.Errorf("remove error lacks --force guidance: %q", h.stderr.String())
	}
	if code := h.run(t, "", "account", "remove", "alice", "--force"); code != 0 {
		t.Fatalf("remove --force exit = %d, stderr=%q", code, h.stderr.String())
	}
	if len(cleared) != 1 || cleared[0] != "alice" {
		t.Errorf("clear seam calls = %v, want [alice]", cleared)
	}
	if _, err := r.Get("alice"); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("alice still present: %v", err)
	}
	if active, err := r.Active(); err != nil || active != account.DefaultName {
		t.Fatalf("active after remove = %q, %v", active, err)
	}
}

func TestAccountRemoveNeverDeletesLast(t *testing.T) {
	h := newHarness(t)
	stubAccountSeams(t, func(string) error { return nil }, func(string) error { return nil })
	r := testAccountRegistry(t, h)
	if code := h.run(t, "", "account", "remove", account.DefaultName, "--force"); code != 2 {
		t.Fatalf("remove last exit = %d, want 2; stderr=%q", code, h.stderr.String())
	}
	if _, err := r.Get(account.DefaultName); err != nil {
		t.Fatalf("last account was removed: %v", err)
	}
}

func TestAccountAddReportsHumanOnlySignInFailure(t *testing.T) {
	h := newHarness(t)
	stubAccountSeams(t, func(string) error { return auth.ErrNoAuthCode }, func(string) error { return nil })
	r := testAccountRegistry(t, h)
	if code := h.run(t, "", "account", "add", "bob"); code != 1 {
		t.Fatalf("add with failed sign-in exit = %d, stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "human-only") || !strings.Contains(h.stderr.String(), "account use bob") {
		t.Errorf("missing sign-in guidance: %q", h.stderr.String())
	}
	if _, err := r.Get("bob"); err != nil {
		t.Fatalf("profile was lost after sign-in failure: %v", err)
	}
}

func TestAccountEnvOverrideSelectsProfile(t *testing.T) {
	h := newHarness(t)
	stubAccountSeams(t, func(string) error { return nil }, func(string) error { return nil })
	r := testAccountRegistry(t, h)
	if err := r.Add(account.Profile{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOLAB_ACCOUNT", "alice")
	if code := h.run(t, "", "account", "show"); code != 0 {
		t.Fatalf("show with env override exit = %d, stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "alice") {
		t.Errorf("env override not reflected: %q", h.stdout.String())
	}
}
