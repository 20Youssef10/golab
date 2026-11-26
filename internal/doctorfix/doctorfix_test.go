package doctorfix

// `doctor --fix` prints EXACT remediations and applies only safe, reversible
// local changes. These tests pin both halves: the printed command and the
// bounds of --apply (never delete, never touch an unrelated path, never force
// a legacy-token migration without proof of a Go token store).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordFS struct {
	chmods  []string
	renames [][2]string
	stat    func(string) (os.FileInfo, error)
	held    bool
}

func (r *recordFS) Stat(p string) (os.FileInfo, error) { return r.stat(p) }
func (r *recordFS) Chmod(p string, m os.FileMode) error {
	r.chmods = append(r.chmods, p)
	return nil
}
func (r *recordFS) Rename(from, to string) error {
	r.renames = append(r.renames, [2]string{from, to})
	return nil
}
func (r *recordFS) TryLock(string) (bool, error) { return r.held, nil }
func (r *recordFS) Now() time.Time               { return time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC) }

func setup(t *testing.T) (Config, FS, *recordFS) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		ConfigDir:       dir,
		SessionsPath:    filepath.Join(dir, "sessions.json"),
		SettingsPath:    filepath.Join(dir, "settings.json"),
		OAuthTokenPath:  filepath.Join(dir, "oauth_token.json"),
		LegacyTokenPath: filepath.Join(dir, "token.json"),
		KnownHostsPath:  filepath.Join(dir, "known_hosts.json"),
	}
	fs := &recordFS{stat: os.Stat}
	for _, p := range []string{cfg.SessionsPath, cfg.SettingsPath, cfg.OAuthTokenPath, cfg.LegacyTokenPath} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(cfg.SessionsPath, 0o644); err != nil {
		t.Fatal(err)
	}
	lock := cfg.SessionsPath + ".lock"
	if err := os.WriteFile(lock, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	inj := FS{
		Stat: fs.Stat, Chmod: fs.Chmod, Rename: fs.Rename,
		TryLock: fs.TryLock, Now: fs.Now,
	}
	return cfg, inj, fs
}

func find(fixes []Fix, kind string) (Fix, bool) {
	for _, f := range fixes {
		if f.Kind == kind {
			return f, true
		}
	}
	return Fix{}, false
}

func findByID(fixes []Fix, id string) (Fix, bool) {
	for _, f := range fixes {
		if f.ID == id {
			return f, true
		}
	}
	return Fix{}, false
}

func TestInspectPrintsExactRemediations(t *testing.T) {
	cfg, fs, rec := setup(t)
	fixes, err := Inspect(context.Background(), cfg, fs)
	if err != nil {
		t.Fatal(err)
	}
	mode, ok := findByID(fixes, "state.sessions.mode")
	if !ok || mode.Command != "chmod 600 "+cfg.SessionsPath {
		t.Fatalf("chmod remediation = %+v", mode)
	}
	dirMode, ok := findByID(fixes, "config.dir.mode")
	if !ok || dirMode.Command != "chmod 700 "+cfg.ConfigDir {
		t.Fatalf("directory remediation = %+v", dirMode)
	}
	lock, ok := find(fixes, "stale-lock")
	if !ok || lock.Command != "mv "+cfg.SessionsPath+".lock "+cfg.SessionsPath+".lock.stale" {
		t.Fatalf("stale-lock remediation = %+v", lock)
	}
	legacy, ok := find(fixes, "legacy-token")
	if !ok || legacy.Command != "mv "+cfg.LegacyTokenPath+" "+cfg.LegacyTokenPath+".golab-backup" {
		t.Fatalf("legacy-token remediation = %+v", legacy)
	}
	if len(rec.chmods) != 0 || len(rec.renames) != 0 {
		t.Fatalf("inspect mutated the filesystem: %+v", rec)
	}
}

func TestApplyOnlySafeReversibleOperations(t *testing.T) {
	cfg, fs, rec := setup(t)
	fixes, err := Inspect(context.Background(), cfg, fs)
	if err != nil {
		t.Fatal(err)
	}
	results, err := Apply(context.Background(), fixes, fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.chmods) != 2 || rec.chmods[0] != cfg.ConfigDir || rec.chmods[1] != cfg.SessionsPath {
		t.Fatalf("chmod targets = %v", rec.chmods)
	}
	if len(rec.renames) != 2 {
		t.Fatalf("renames = %v", rec.renames)
	}
	for _, r := range results {
		if r.Status != "applied" {
			t.Fatalf("unexpected result: %+v", r)
		}
	}
}

func TestApplyRefusesLegacyWithoutGoTokenStore(t *testing.T) {
	cfg, fs, rec := setup(t)
	if err := os.Remove(cfg.OAuthTokenPath); err != nil {
		t.Fatal(err)
	}
	fixes, err := Inspect(context.Background(), cfg, fs)
	if err != nil {
		t.Fatal(err)
	}
	legacy, ok := find(fixes, "legacy-token")
	if !ok {
		t.Fatal("legacy token was not reported")
	}
	if legacy.SafeApply {
		t.Fatalf("unsafe fix marked appliable: %+v", legacy)
	}
	if _, err := Apply(context.Background(), fixes, fs); err != nil {
		t.Fatal(err)
	}
	for _, r := range rec.renames {
		if strings.Contains(r[0], "token.json") {
			t.Fatalf("legacy token renamed without proof of Go token store: %v", r)
		}
	}
}

func TestApplySkipsLockHeldByAnotherProcess(t *testing.T) {
	cfg, fs, rec := setup(t)
	rec.held = true
	fixes, err := Inspect(context.Background(), cfg, fs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := find(fixes, "stale-lock"); ok {
		t.Fatal("a held lock was reported as stale")
	}
}
