package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from upstream tests/test_cli.py (PR #267):
//   test_cli_upload_directory_requires_recursive
//   test_cli_upload_recursive_directory
//   test_cli_upload_recursive_single_file
//   test_cli_download_recursive
//   test_cli_download_directory_without_recursive
//   test_cli_upload_recursive_missing_path

func writeTreeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestUploadDirectoryRequiresRecursive ports upstream
// test_cli_upload_directory_requires_recursive.
func TestUploadDirectoryRequiresRecursive(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	dir := filepath.Join(t.TempDir(), "mydir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	code := h.run(t, "", "upload", "-s", "s1", dir, "content/mydir")
	if code != 1 || !strings.Contains(h.stderr.String(), "-r/--recursive") {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if len(fc.puts) != 0 {
		t.Fatalf("unexpected uploads: %v", fc.puts)
	}
}

// TestUploadRecursiveDirectory ports upstream
// test_cli_upload_recursive_directory, including empty-directory preservation.
func TestUploadRecursiveDirectory(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	dir := filepath.Join(t.TempDir(), "mydir")
	writeTreeFile(t, filepath.Join(dir, "root.txt"), "root")
	writeTreeFile(t, filepath.Join(dir, "sub", "nested.txt"), "nested")
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}

	code := h.run(t, "", "upload", "-r", "-s", "s1", dir, "content/mydir")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "2 file(s)") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	if got := fc.file("content/mydir/root.txt"); got != "root" {
		t.Errorf("root.txt = %q", got)
	}
	if got := fc.file("content/mydir/sub/nested.txt"); got != "nested" {
		t.Errorf("nested.txt = %q", got)
	}
	for _, p := range []string{
		"content/mydir", "content/mydir/sub", "content/mydir/empty",
	} {
		found := false
		for _, put := range fc.puts {
			if put == p {
				found = true
			}
		}
		if !found {
			t.Errorf("missing directory PUT for %q in %v", p, fc.puts)
		}
	}
}

// TestUploadRecursiveSingleFile ports upstream
// test_cli_upload_recursive_single_file: -r on a regular file is still a
// single-file upload.
func TestUploadRecursiveSingleFile(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	f := filepath.Join(t.TempDir(), "a.txt")
	writeTreeFile(t, f, "hi")

	code := h.run(t, "", "upload", "-r", "-s", "s1", f, "content/a.txt")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if got := fc.file("content/a.txt"); got != "hi" {
		t.Fatalf("uploaded = %q", got)
	}
	if strings.Contains(h.stderr.String(), "file(s)") {
		t.Fatalf("single-file upload reported a recursive count: %q", h.stderr.String())
	}
}

// TestUploadRecursiveMissingPath ports upstream
// test_cli_upload_recursive_missing_path.
func TestUploadRecursiveMissingPath(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)

	code := h.run(t, "", "upload", "-r", "-s", "s1", filepath.Join(t.TempDir(), "nope"), "content/nope")
	if code != 1 || !strings.Contains(h.stderr.String(), "Local path") {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if len(fc.puts) != 0 {
		t.Fatal("no uploads expected for a missing path")
	}
}

// TestDownloadRecursive ports upstream test_cli_download_recursive.
func TestDownloadRecursive(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	fc.dirs["content/proj"] = []string{"root.txt", "sub/", "empty/"}
	fc.dirs["content/proj/sub"] = []string{"bin.dat"}
	fc.dirs["content/proj/empty"] = []string{}
	fc.addFile("content/proj/root.txt", "hello")
	fc.addFile("content/proj/sub/bin.dat", "binary")
	dest := filepath.Join(t.TempDir(), "out")

	code := h.run(t, "", "download", "-r", "-s", "s1", "content/proj", dest)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "2 file(s)") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(dest, "root.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("root.txt = %q, %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(dest, "sub", "bin.dat"))
	if err != nil || string(b) != "binary" {
		t.Fatalf("bin.dat = %q, %v", b, err)
	}
	info, err := os.Stat(filepath.Join(dest, "empty"))
	if err != nil || !info.IsDir() {
		t.Fatalf("empty directory not preserved: %v", err)
	}
}

// TestDownloadDirectoryWithoutRecursive ports upstream
// test_cli_download_directory_without_recursive.
func TestDownloadDirectoryWithoutRecursive(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	fc.dirs["content/mydir"] = []string{}

	code := h.run(t, "", "download", "-s", "s1", "content/mydir", filepath.Join(t.TempDir(), "out"))
	if code != 1 || !strings.Contains(h.stderr.String(), "-r/--recursive") {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
}

// TestDownloadRecursiveRejectsUnsafeEntryName exercises the PR #267 traversal
// guard through the CLI layer.
func TestDownloadRecursiveRejectsUnsafeEntryName(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	fc.dirs["content/dir"] = []string{"../escape"}
	fc.addFile("content/escape", "secret")

	code := h.run(t, "", "download", "-r", "-s", "s1", "content/dir", filepath.Join(t.TempDir(), "out"))
	if code != 1 || !strings.Contains(h.stderr.String(), "unsafe path") {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
}
