package cli

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/contents"
	"github.com/20Youssef10/golab/internal/logx"
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

// TestSyncApplyRejectsPathEscape is the §4.18 regression. cleanRemotePath
// protects collection-time paths, but a crafted manifest/state action must not
// rely on that: applySync asserts containment itself. No upstream Python test
// covers this — the escape is possible only through a tampered local manifest.
func TestSyncApplyRejectsPathEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fc := newFakeContents(t)
	client, err := contents.New(contents.Config{
		BaseURL: fc.URL, Token: logx.NewSecret("t"), HTTPClient: fc.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Absolute action paths are platform-specific: a POSIX "/x" becomes a
	// volumeless rooted "\x" on Windows, so test the native form as well as
	// the POSIX one (and a UNC volume on Windows).
	posixAbs := "/abs-escape.txt"
	nativeAbs := posixAbs
	if runtime.GOOS == "windows" {
		nativeAbs = `C:\abs-escape.txt`
	}

	// The remote side serves every payload so the pre-fix download/upload
	// branches actually reach the local filesystem instead of failing early.
	fc.addFile(path.Join("remote", "../outside-write.txt"), "pwned\n")
	for _, abs := range []string{posixAbs, nativeAbs} {
		fc.addFile(path.Join("remote", abs), "pwned\n")
	}
	uncAbs := `\\host\share\abs-escape.txt`
	if runtime.GOOS == "windows" {
		fc.addFile(path.Join("remote", uncAbs), "pwned\n")
	}

	outsideWrite := filepath.Join(parent, "outside-write.txt")
	outsideRead := filepath.Join(parent, "outside-read.txt")
	outsideDelete := filepath.Join(parent, "outside-delete.txt")
	if err := os.WriteFile(outsideRead, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideDelete, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type escapeCase struct {
		name   string
		action syncAction
	}
	cases := []escapeCase{
		{"download-traversal", syncAction{Kind: "download", Path: "../outside-write.txt"}},
		{"delete-traversal", syncAction{Kind: "delete-local", Path: "../outside-delete.txt"}},
		{"upload-traversal", syncAction{Kind: "upload", Path: "../outside-read.txt"}},
		{"download-absolute", syncAction{Kind: "download", Path: nativeAbs}},
		{"download-posix-absolute", syncAction{Kind: "download", Path: posixAbs}},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, escapeCase{"download-unc-absolute", syncAction{Kind: "download", Path: uncAbs}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := applySync(context.Background(), client, root, "remote",
				[]syncAction{tc.action}, nil, nil, map[string]string{})
			var escape *syncPathEscape
			if !errors.As(err, &escape) {
				t.Fatalf("applySync error = %v, want *syncPathEscape", err)
			}
		})
	}
	if _, err := os.Stat(outsideWrite); !os.IsNotExist(err) {
		t.Errorf("download wrote outside localRoot: %s (stat err %v)", outsideWrite, err)
	}
	if b, err := os.ReadFile(outsideDelete); err != nil || string(b) != "keep\n" {
		t.Errorf("delete-local mutated a file outside localRoot: %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		// On Windows filepath.Join nests a drive/UNC path under the root, so
		// the typed rejection above is the portable assertion there.
		absTarget := filepath.Join(root, filepath.FromSlash(posixAbs))
		if _, err := os.Stat(absTarget); !os.IsNotExist(err) {
			t.Errorf("absolute action path created %s (stat err %v)", absTarget, err)
		}
	}
	if len(fc.puts) != 0 {
		t.Errorf("upload-traversal read outside localRoot and wrote %d remote files", len(fc.puts))
	}
}
