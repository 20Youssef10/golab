package account

// No upstream Python port: named profiles are a golab extension. These tests
// lock down the migration contract that a legacy single-user install must be
// adopted as account "default" with zero data loss.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsureMigratesLegacySingleUserWithoutDataLoss(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, TokenFileName(DefaultName))
	sessionsPath := filepath.Join(dir, SessionsFileName(DefaultName))
	legacyToken := []byte(`{"refresh_token":"do-not-log-me"}`)
	legacySessions := []byte("{\n  \"schema_version\": 1,\n  \"s1\": {\"name\": \"s1\"}\n}\n")
	if err := os.WriteFile(tokenPath, legacyToken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionsPath, legacySessions, 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := New(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	file, err := r.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if file.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", file.Version, CurrentVersion)
	}
	if file.Active != DefaultName {
		t.Errorf("active = %q, want %q", file.Active, DefaultName)
	}
	if len(file.Profiles) != 1 || file.Profiles[0].Name != DefaultName {
		t.Fatalf("profiles = %+v, want one %q profile", file.Profiles, DefaultName)
	}
	if file.Profiles[0].CreatedAt == "" {
		t.Error("created_at was not stamped")
	}
	// Migration must not touch the existing token or session bytes.
	gotToken, err := os.ReadFile(tokenPath)
	if err != nil || string(gotToken) != string(legacyToken) {
		t.Errorf("legacy token changed: %q, %v", gotToken, err)
	}
	gotSessions, err := os.ReadFile(sessionsPath)
	if err != nil || string(gotSessions) != string(legacySessions) {
		t.Errorf("legacy sessions changed: %q, %v", gotSessions, err)
	}
	// A second Ensure is a no-op and must not duplicate the default profile.
	again, err := r.Ensure()
	if err != nil || len(again.Profiles) != 1 {
		t.Fatalf("second Ensure = %+v, %v", again, err)
	}
}

func TestEnsureIsIdempotentAfterNamedProfilesExist(t *testing.T) {
	dir := t.TempDir()
	r, err := New(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(Profile{Name: "alice", Email: "alice@example.com", Provider: "oauth2"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetActive("alice"); err != nil {
		t.Fatal(err)
	}
	file, err := r.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if file.Active != "alice" || len(file.Profiles) != 2 {
		t.Fatalf("Ensure reset named profiles: %+v", file)
	}
}

func TestRegistryRefusesCorruptNewerAndUnsafeFiles(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, FileName)
		if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := New(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Ensure(); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("corrupt file error = %v, want ErrCorrupt", err)
		}
	})
	t.Run("newer version", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, FileName)
		body := `{"version":99,"active":"default","profiles":[{"name":"default","created_at":"now"}]}`
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := New(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Ensure(); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("newer-version error = %v, want ErrCorrupt", err)
		}
	})
	t.Run("dangling active", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, FileName)
		body := `{"version":1,"active":"ghost","profiles":[{"name":"default","created_at":"now"}]}`
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := New(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Ensure(); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("dangling active error = %v, want ErrCorrupt", err)
		}
	})
}

func TestRegistryRejectsUnsafeNames(t *testing.T) {
	r, err := New(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../escape", "a/b", "..", "CON", "bad name", ""} {
		if err := r.Add(Profile{Name: name}); err == nil {
			t.Errorf("Add(%q) was accepted", name)
		}
	}
	profiles, err := r.List()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("unsafe names wrote profiles: %+v, %v", profiles, err)
	}
}

func TestRegistryAddUseAndRemoveRules(t *testing.T) {
	r, err := New(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(Profile{Name: "alice", Email: "alice@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(Profile{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(Profile{Name: "alice"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate Add error = %v, want ErrExists", err)
	}
	if err := r.SetActive("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetActive(ghost) = %v, want ErrNotFound", err)
	}
	if err := r.SetActive("alice"); err != nil {
		t.Fatal(err)
	}
	if active, err := r.Active(); err != nil || active != "alice" {
		t.Fatalf("active = %q, %v", active, err)
	}
	// Active account needs an explicit override.
	if err := r.Remove("alice", false); !errors.Is(err, ErrActive) {
		t.Fatalf("Remove(active,false) = %v, want ErrActive", err)
	}
	if err := r.Remove("alice", true); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove("bob", false); err != nil {
		t.Fatal(err)
	}
	// Never delete the last account.
	if err := r.Remove(DefaultName, true); !errors.Is(err, ErrLast) {
		t.Fatalf("Remove(last) = %v, want ErrLast", err)
	}
	profiles, err := r.List()
	if err != nil || len(profiles) != 1 || profiles[0].Name != DefaultName {
		t.Fatalf("final profiles = %+v, %v", profiles, err)
	}
	if active, err := r.Active(); err != nil || active != DefaultName {
		t.Fatalf("active after remove = %q, %v", active, err)
	}
}

func TestRegistryUpdateNeverLosesMetadata(t *testing.T) {
	r, err := New(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	created := "2026-01-01T00:00:00Z"
	if err := r.Add(Profile{Name: "alice", Email: "a@example.com", Provider: "oauth2", Scopes: []string{"scope-a"}, CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := r.Update("alice", func(p *Profile) { p.Email = "new@example.com" }); err != nil {
		t.Fatal(err)
	}
	p, err := r.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	if p.Email != "new@example.com" || p.Provider != "oauth2" || p.CreatedAt != created || len(p.Scopes) != 1 {
		t.Fatalf("metadata lost on update: %+v", p)
	}
}

func TestPerAccountStorageNamesAreSafeAndIsolated(t *testing.T) {
	if got := KeyringUser(DefaultName); got != "oauth_token" {
		t.Errorf("default keyring user = %q, want the legacy user", got)
	}
	if got := TokenFileName(DefaultName); got != "oauth_token.json" {
		t.Errorf("default token file = %q, want the legacy file", got)
	}
	if got := SessionsFileName(DefaultName); got != "sessions.json" {
		t.Errorf("default sessions file = %q, want the legacy file", got)
	}
	if got := KeyringUser("alice"); got != "oauth_token:alice" {
		t.Errorf("named keyring user = %q", got)
	}
	if got := TokenFileName("alice"); got != "oauth_token_alice.json" {
		t.Errorf("named token file = %q", got)
	}
	if got := SessionsFileName("alice"); got != "sessions_alice.json" {
		t.Errorf("named sessions file = %q", got)
	}
	for _, name := range []string{"../escape", "a/b", "CON", "bad name"} {
		if got := KeyringUser(name); strings.Contains(got, "/") || strings.Contains(got, "..") {
			t.Errorf("unsafe name %q produced keyring user %q", name, got)
		}
	}
}

func TestRegistryWriteIsPrivateAndAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	r, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("registry is not a regular file: %v", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("registry mode = %o, want owner-only", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".golab-tmp-") {
			t.Errorf("temp file was left behind: %s", e.Name())
		}
	}
}
