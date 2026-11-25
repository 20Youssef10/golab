package state

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
)

// §4.7 (state half): the rename in writeJSON must be followed by a fsync of
// the parent directory, otherwise a crash can lose the directory entry even
// though the data file itself was fsynced. The dir-sync function is injected so
// the durability path can be exercised without real crash injection.
func TestWriteJSONSyncsParentDirAfterRename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sessions.json")

	var synced []string
	err := writeJSONWithDirSync(p, map[string]Session{}, func(d string) error {
		// The parent must be synced only after the rename is in place.
		if _, err := os.Stat(p); err != nil {
			t.Errorf("parent dir synced before rename: %v", err)
		}
		synced = append(synced, d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(synced) != 1 || synced[0] != dir {
		t.Fatalf("parent dir syncs = %v, want [%s]", synced, dir)
	}
}

// §4.10 (session half): a symlinked sessions.json must be refused with a typed
// error rather than silently followed (which could point at an attacker-chosen
// file outside the state directory).
func TestReadSessionsRejectsSymlink(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlinks require privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "sessions.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("List: expected ErrUnsafeFile, got %v", err)
	}
	if _, err := s.Get("x"); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("Get: expected ErrUnsafeFile, got %v", err)
	}
	if err := s.Put(Session{Name: "x"}); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("Put: expected ErrUnsafeFile, got %v", err)
	}
}

// §4.10 (session half): a non-regular file at the session path (e.g. a
// directory) is just as unsafe as a symlink and must be refused.
func TestReadSessionsRejectsNonRegular(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("expected ErrUnsafeFile, got %v", err)
	}
}

// §4.10 (session half): an existing session file with group/other permission
// bits must be corrected to 0600 on read, not only on write.
func TestReadSessionsCorrectsLooseMode(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("chmod 0600 is a no-op on Windows")
	}
	p := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode after read = %04o, want 0600", got)
	}
}
