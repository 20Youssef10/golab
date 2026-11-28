package app

// No upstream Python port: account-aware composition is a golab extension.
// These tests prove the composition root resolves the active profile before
// building the session store, without moving or rewriting legacy files.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/20Youssef10/golab/internal/account"
	"github.com/20Youssef10/golab/internal/state"
)

func TestAppNewAdoptsLegacyInstallAsDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLAB_CONFIG_DIR", dir)
	t.Setenv("COLAB_SESSIONS_FILE", "")
	t.Setenv("GOLAB_ACCOUNT", "")
	legacyToken := []byte(`{"refresh_token":"do-not-log-me"}`)
	legacySessions := []byte("{\n  \"schema_version\": 1,\n  \"s1\": {\"name\": \"s1\"}\n}\n")
	if err := os.WriteFile(filepath.Join(dir, account.TokenFileName(account.DefaultName)), legacyToken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, account.SessionsFileName(account.DefaultName)), legacySessions, 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Account != account.DefaultName {
		t.Errorf("resolved account = %q, want default", d.Account)
	}
	if got, want := d.Store.Path(), filepath.Join(dir, "sessions.json"); got != want {
		t.Errorf("store path = %q, want %q", got, want)
	}
	sessions, err := d.Store.List()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("legacy session not visible: %+v, %v", sessions, err)
	}
	reg, err := account.New(filepath.Join(dir, account.FileName))
	if err != nil {
		t.Fatal(err)
	}
	f, err := reg.Load()
	if err != nil || f.Active != account.DefaultName || len(f.Profiles) != 1 {
		t.Fatalf("registry = %+v, %v", f, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "oauth_token.json")); err != nil || string(b) != string(legacyToken) {
		t.Errorf("legacy token changed: %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "sessions.json")); err != nil || string(b) != string(legacySessions) {
		t.Errorf("legacy sessions changed: %q, %v", b, err)
	}
}

func TestAppNewIsolatesNamedAccountStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLAB_CONFIG_DIR", dir)
	t.Setenv("COLAB_SESSIONS_FILE", "")
	t.Setenv("GOLAB_ACCOUNT", "")
	legacySessions := []byte("{\n  \"schema_version\": 1,\n  \"s1\": {\"name\": \"s1\"}\n}\n")
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), legacySessions, 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := account.New(filepath.Join(dir, account.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(account.Profile{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetActive("alice"); err != nil {
		t.Fatal(err)
	}

	d, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Account != "alice" {
		t.Fatalf("resolved account = %q, want alice", d.Account)
	}
	if got, want := d.Store.Path(), filepath.Join(dir, "sessions_alice.json"); got != want {
		t.Errorf("store path = %q, want %q", got, want)
	}
	// The named account starts with an isolated store; legacy sessions remain.
	if sessions, err := d.Store.List(); err != nil || len(sessions) != 0 {
		t.Fatalf("named store is not isolated: %+v, %v", sessions, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "sessions.json")); err != nil || string(b) != string(legacySessions) {
		t.Errorf("legacy sessions changed: %q, %v", b, err)
	}
	if err := d.Store.Put(state.Session{Name: "alice-only"}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "sessions.json")); err != nil || string(b) != string(legacySessions) {
		t.Errorf("named write leaked into legacy sessions: %q, %v", b, err)
	}
}

func TestAppNewAccountOverridePrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLAB_CONFIG_DIR", dir)
	t.Setenv("COLAB_SESSIONS_FILE", "")
	t.Setenv("GOLAB_ACCOUNT", "alice")
	reg, err := account.New(filepath.Join(dir, account.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		if err := reg.Add(account.Profile{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	// GOLAB_ACCOUNT wins over the registry's active profile.
	d, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Store.Path(); got != filepath.Join(dir, "sessions_alice.json") {
		t.Errorf("env override store = %q", got)
	}
	// An explicit Config.Account (the --account flag) wins over the env.
	d, err = New(context.Background(), Config{Account: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Account != "bob" || d.Store.Path() != filepath.Join(dir, "sessions_bob.json") {
		t.Fatalf("flag override = %q at %q", d.Account, d.Store.Path())
	}
}

func TestAppNewUnknownAccountAborts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLAB_CONFIG_DIR", dir)
	t.Setenv("COLAB_SESSIONS_FILE", "")
	t.Setenv("GOLAB_ACCOUNT", "ghost")
	reg, err := account.New(filepath.Join(dir, account.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatal("unknown account was silently accepted")
	}
}

func TestAppNewRejectsUnsafeAccountName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLAB_CONFIG_DIR", dir)
	t.Setenv("COLAB_SESSIONS_FILE", "")
	if _, err := New(context.Background(), Config{Account: "../escape"}); err == nil {
		t.Fatal("traversal account name was accepted")
	}
}
