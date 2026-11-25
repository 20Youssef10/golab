package state

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
)

// §4.10 (settings half): a symlinked settings.json must be refused with the
// same typed error as a symlinked sessions.json, rather than followed.
func TestSettingsLoadRejectsSymlink(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlinks require privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"enable_update_check":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	s, err := NewSettingsStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("Load: expected ErrUnsafeFile, got %v", err)
	}
}

// §4.10 (settings half): a non-regular file at the settings path is refused.
func TestSettingsLoadRejectsNonRegular(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := NewSettingsStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("expected ErrUnsafeFile, got %v", err)
	}
}

// §4.10 (settings half): an existing file with group/other bits is corrected
// to 0600 on load, not only on save.
func TestSettingsLoadCorrectsLooseMode(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("chmod 0600 is a no-op on Windows")
	}
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"enable_update_check":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSettingsStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode after load = %04o, want 0600", got)
	}
}

// The existing ErrCorrupt contract must survive the hardening: a malformed
// settings file still aborts instead of silently falling back to defaults.
func TestSettingsLoadCorruptStillAborts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSettingsStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt, got %v", err)
	}
}
