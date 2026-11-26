package cli

// doctor --fix is dry-run by default. --apply may only narrow modes and rename
// stale locks / legacy tokens aside; it must never delete or call the network.

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestDoctorFixDryRunAndApplyBounds(t *testing.T) {
	h := newHarness(t)
	t.Setenv("COLAB_CONFIG_DIR", h.dir)
	sessions := h.deps.Store.Path()
	if err := os.WriteFile(filepath.Join(h.dir, "oauth_token.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(h.dir, "token.json")
	if err := os.WriteFile(legacy, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessions, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := sessions + ".lock"
	if err := os.WriteFile(lock, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(sessions)
	if err != nil {
		t.Fatal(err)
	}

	if code := runNewCommand(t, h, "doctor", "--fix"); code != 0 {
		t.Fatalf("dry-run exit = %d, stderr = %s", code, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.Contains(out, "chmod 600 "+sessions) {
		t.Fatalf("exact remediation missing: %s", out)
	}
	if !strings.Contains(out, "mv "+legacy+" "+legacy+".golab-backup") {
		t.Fatalf("legacy remediation missing: %s", out)
	}
	after, err := os.ReadFile(sessions)
	if err != nil || string(after) != string(before) {
		t.Fatalf("dry run changed sessions.json content: %q -> %q (%v)", before, after, err)
	}
	// Windows reports 0666 for every writable file, so chmod is not observable
	// there; keep the exact Unix assertion and check content on Windows.
	if fi, err := os.Stat(sessions); err != nil ||
		(goruntime.GOOS != "windows" && fi.Mode().Perm() != 0o644) {
		t.Fatalf("dry run changed mode: %v %v", fi, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("dry run removed legacy token: %v", err)
	}

	if code := runNewCommand(t, h, "doctor", "--fix", "--apply"); code != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", code, h.stderr.String())
	}
	if applied, err := os.ReadFile(sessions); err != nil || string(applied) != string(before) {
		t.Fatalf("apply changed sessions.json content: %q -> %q (%v)", before, applied, err)
	}
	if fi, err := os.Stat(sessions); err != nil ||
		(goruntime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("apply did not narrow mode: %v %v", fi, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy token was not renamed aside: %v", err)
	}
	if _, err := os.Stat(legacy + ".golab-backup"); err != nil {
		t.Fatalf("reversible backup missing: %v", err)
	}
	if n := len(h.ctrl.Requests()); n != 0 {
		t.Fatalf("doctor --fix made %d control-plane requests", n)
	}
}

func TestDoctorApplyRequiresFix(t *testing.T) {
	h := newHarness(t)
	if code := runNewCommand(t, h, "doctor", "--apply"); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", code, h.stderr.String())
	}
}
