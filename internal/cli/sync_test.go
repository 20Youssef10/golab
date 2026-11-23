package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupSyncSession(t *testing.T) (*testHarness, *fakeContents, string) {
	t.Helper()
	h := newHarness(t)
	fc := newFakeContents(t)
	fc.dirs["remote"] = []string{}
	h.putSession(t, "s1")
	h.attachContents(t, fc)
	local := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return h, fc, local
}

func writeLocal(t *testing.T, root, name, content string) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSyncOneShotRoundTripAndIdempotence proves the one-shot sync moves files
// in both directions and that a second unchanged sync writes nothing.
func TestSyncOneShotRoundTripAndIdempotence(t *testing.T) {
	h, fc, local := setupSyncSession(t)
	writeLocal(t, local, "a.txt", "alpha\n")
	writeLocal(t, local, "sub/b.txt", "beta\n")
	fc.addFile("remote/c.txt", "gamma\n")
	fc.dirs["remote"] = []string{"c.txt"}

	code := runNewCommand(t, h, "sync", "--apply", "-s", "s1", local, "remote")
	if code != 0 {
		t.Fatalf("first sync exit = %d; stdout=%s stderr=%s", code, h.stdout.String(), h.stderr.String())
	}
	if got := fc.file("remote/a.txt"); got != "alpha\n" {
		t.Errorf("remote a.txt = %q", got)
	}
	if got := fc.file("remote/sub/b.txt"); got != "beta\n" {
		t.Errorf("remote sub/b.txt = %q", got)
	}
	localC, err := os.ReadFile(filepath.Join(local, "c.txt"))
	if err != nil || string(localC) != "gamma\n" {
		t.Errorf("local c.txt = %q, err = %v", localC, err)
	}
	puts := len(fc.puts)

	code = runNewCommand(t, h, "sync", "--apply", "-s", "s1", local, "remote")
	if code != 0 {
		t.Fatalf("second sync exit = %d; stderr=%s", code, h.stderr.String())
	}
	if len(fc.puts) != puts {
		t.Fatalf("unchanged sync wrote %d extra times", len(fc.puts)-puts)
	}
}

// TestSyncDeleteRequiresFlag ensures an unrequested deletion never happens.
func TestSyncDeleteRequiresFlag(t *testing.T) {
	h, fc, local := setupSyncSession(t)
	fc.addFile("remote/stale.txt", "old\n")
	fc.dirs["remote"] = []string{"stale.txt"}
	if code := runNewCommand(t, h, "sync", "--apply", "--direction", "up", "-s", "s1", local, "remote"); code != 0 {
		t.Fatalf("sync exit = %d; stderr=%s", code, h.stderr.String())
	}
	if !fc.hasFile("remote/stale.txt") {
		t.Fatal("remote file deleted without --delete")
	}
	if code := runNewCommand(t, h, "sync", "--apply", "--direction", "up", "--delete", "-s", "s1", local, "remote"); code != 0 {
		t.Fatalf("sync --delete exit = %d; stderr=%s", code, h.stderr.String())
	}
	if fc.hasFile("remote/stale.txt") {
		t.Fatalf("remote file survived --delete; stdout=%s stderr=%s", h.stdout.String(), h.stderr.String())
	}
}

// TestSyncConflictDoesNotClobber seeds both sides changing after a baseline
// sync. A conflict must fail without overwriting either side.
func TestSyncConflictDoesNotClobber(t *testing.T) {
	h, fc, local := setupSyncSession(t)
	writeLocal(t, local, "x.txt", "original\n")
	if code := runNewCommand(t, h, "sync", "--apply", "-s", "s1", local, "remote"); code != 0 {
		t.Fatalf("baseline sync exit = %d; stderr=%s", code, h.stderr.String())
	}
	writeLocal(t, local, "x.txt", "local-change\n")
	fc.addFile("remote/x.txt", "remote-change\n")
	fc.dirs["remote"] = []string{"x.txt"}
	puts := len(fc.puts)
	code := runNewCommand(t, h, "sync", "--apply", "-s", "s1", local, "remote")
	if code != 1 {
		t.Fatalf("conflict exit = %d, want 1; stdout=%s stderr=%s", code, h.stdout.String(), h.stderr.String())
	}
	if len(fc.puts) != puts {
		t.Fatal("conflict caused a remote write")
	}
	got, err := os.ReadFile(filepath.Join(local, "x.txt"))
	if err != nil || string(got) != "local-change\n" {
		t.Fatalf("local file was clobbered: %q, %v", got, err)
	}
	if fc.file("remote/x.txt") != "remote-change\n" {
		t.Fatal("remote file was clobbered")
	}
}

// TestSyncNotebookDiffIgnoresOutputs is the git-friendly notebook golden:
// outputs/execution_count differences produce no diff, a code change produces
// one hunk.
func TestSyncNotebookDiffIgnoresOutputs(t *testing.T) {
	h, fc, _ := setupSyncSession(t)
	base := `{"cells":[{"cell_type":"code","id":"c1","metadata":{},"source":["print(1)\n"],"execution_count":1,"outputs":[{"output_type":"stream","name":"stdout","text":"old\n"}]}],"metadata":{},"nbformat":4,"nbformat_minor":5}`
	remote := strings.Replace(base, `"old\n"`, `"new\n"`, 1)
	remote = strings.Replace(remote, `"execution_count":1`, `"execution_count":2`, 1)
	local := filepath.Join(t.TempDir(), "nb.ipynb")
	if err := os.WriteFile(local, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	fc.addFile("remote/nb.ipynb", remote)

	if code := runNewCommand(t, h, "sync", "--diff", "-s", "s1", local, "remote/nb.ipynb"); code != 0 {
		t.Fatalf("diff exit = %d; stderr=%s", code, h.stderr.String())
	}
	if strings.TrimSpace(h.stdout.String()) != "" {
		t.Fatalf("outputs-only change produced a diff: %s", h.stdout.String())
	}

	changed := strings.Replace(base, "print(1)", "print(2)", 1)
	if err := os.WriteFile(local, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runNewCommand(t, h, "sync", "--diff", "-s", "s1", local, "remote/nb.ipynb"); code != 0 {
		t.Fatalf("diff exit = %d; stderr=%s", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "@@") || !strings.Contains(h.stdout.String(), "print(2)") {
		t.Fatalf("code change diff = %s", h.stdout.String())
	}
}

// TestSyncRejectsTraversal keeps remote paths on the VM side of the path/filepath
// split and refuses to walk above the requested directory.
func TestSyncRejectsTraversal(t *testing.T) {
	h, _, local := setupSyncSession(t)
	code := runNewCommand(t, h, "sync", "--apply", "-s", "s1", local, "../escape")
	if code != 2 {
		t.Fatalf("traversal exit = %d, want 2; stderr=%s", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "remote path") {
		t.Fatalf("unexpected error: %s", h.stderr.String())
	}
}
