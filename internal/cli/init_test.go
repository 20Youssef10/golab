package cli

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/budget"
)

// Ports the Tier-5 `golab init` contract: a purely local scaffold with a valid
// strict-schema budget profile (including `enforce`) and a .golabignore.
func TestInitScaffoldsValidBudgetProfile(t *testing.T) {
	t.Chdir(t.TempDir())
	h := newHarness(t)

	if code := h.run(t, "", "init"); code != 0 {
		t.Fatalf("init exit=%d stderr=%q", code, h.stderr.String())
	}
	for _, name := range []string{"golab.yaml", ".golabignore"} {
		info, err := os.Lstat(name)
		if err != nil {
			t.Fatalf("%s not created: %v", name, err)
		}
		// Windows reports 0666 for writable files; NTFS ACLs are the real
		// owner-only control there. Keep the exact assertion on Unix.
		if goruntime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", name, info.Mode().Perm())
		}
	}
	policy, err := budget.LoadProfile(context.Background(), "golab.yaml", "default")
	if err != nil {
		t.Fatalf("generated golab.yaml is not a valid profile: %v", err)
	}
	if !policy.Enforce {
		t.Fatalf("scaffold profile does not set enforce: %+v", policy)
	}
	for _, want := range []string{"__pycache__", ".git/"} {
		b, err := os.ReadFile(".golabignore")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), want) {
			t.Fatalf(".golabignore lacks %q: %q", want, b)
		}
	}
}

func TestInitRefusesOverwriteUnlessForce(t *testing.T) {
	t.Chdir(t.TempDir())
	h := newHarness(t)
	const original = "version: 1\n# keep me\n"
	if err := os.WriteFile("golab.yaml", []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := h.run(t, "", "init"); code != 1 {
		t.Fatalf("init exit=%d, want 1; stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "golab.yaml") {
		t.Fatalf("stderr does not name the conflicting file: %q", h.stderr.String())
	}
	b, err := os.ReadFile("golab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != original {
		t.Fatalf("existing file was modified: %q", b)
	}

	if code := h.run(t, "", "init", "--force"); code != 0 {
		t.Fatalf("init --force exit=%d stderr=%q", code, h.stderr.String())
	}
	b, err = os.ReadFile("golab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == original {
		t.Fatalf("--force did not replace the file: %q", b)
	}
}

// A conflict in either target aborts the whole scaffold: no partial write.
func TestInitChecksBothTargetsBeforeWriting(t *testing.T) {
	t.Chdir(t.TempDir())
	h := newHarness(t)
	if err := os.WriteFile(".golabignore", []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run(t, "", "init"); code != 1 {
		t.Fatalf("init exit=%d, want 1; stderr=%q", code, h.stderr.String())
	}
	if _, err := os.Lstat(filepath.Join(".", "golab.yaml")); !os.IsNotExist(err) {
		t.Fatalf("golab.yaml was created despite the .golabignore conflict: %v", err)
	}
}
