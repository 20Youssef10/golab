package contents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// treeFake is a stateful fake Contents API used by the recursive-transfer
// tests. It understands directory models, chunked writes and GET ?content=1.
type treeFake struct {
	*fakeContents

	mu              sync.Mutex
	files           map[string][]byte
	dirs            map[string]map[string]any
	failMkdir       map[string]int
	failMkdirStatus int            // HTTP status for an exhausted failMkdir entry
	always          map[string]any // returned for every GET (depth-limit fakes)
	putPaths        []string
	tokens          []string
	calls           int
}

func (tf *treeFake) callCount() int {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	return tf.calls
}

func newTreeFake(t *testing.T) *treeFake {
	t.Helper()
	tf := &treeFake{
		fakeContents:    &fakeContents{},
		files:           map[string][]byte{},
		dirs:            map[string]map[string]any{},
		failMkdir:       map[string]int{},
		failMkdirStatus: http.StatusInternalServerError,
	}
	tf.Server = newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		tf.serve(w, r)
	}).Server
	return tf
}

func (tf *treeFake) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/contents/"), "/")
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.calls++
	switch r.Method {
	case http.MethodGet:
		if tf.always != nil {
			writeJSON(w, http.StatusOK, tf.always)
			return
		}
		if m, ok := tf.dirs[p]; ok {
			writeJSON(w, http.StatusOK, m)
			return
		}
		if data, ok := tf.files[p]; ok {
			writeJSON(w, http.StatusOK, map[string]any{
				"name": filepath.Base(p), "path": p, "type": "file",
				"format": "base64", "content": base64.StdEncoding.EncodeToString(data),
				"size": len(data),
			})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "not found"})
	case http.MethodPut:
		var body struct {
			Name    string `json:"name"`
			Path    string `json:"path"`
			Type    string `json:"type"`
			Content string `json:"content"`
			Chunk   int    `json:"chunk"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "bad body"})
			return
		}
		tf.putPaths = append(tf.putPaths, p)
		tf.tokens = append(tf.tokens, r.URL.Query().Get("colab-runtime-proxy-token"))
		if body.Type == "directory" {
			if n := tf.failMkdir[p]; n > 0 {
				tf.failMkdir[p] = n - 1
				writeJSON(w, tf.failMkdirStatus, map[string]any{"message": "mkdir failed"})
				return
			}
			if _, ok := tf.dirs[p]; !ok {
				tf.dirs[p] = map[string]any{
					"name": filepath.Base(p), "path": p, "type": "directory",
					"content": []any{},
				}
			}
			writeJSON(w, http.StatusCreated, map[string]any{"path": p, "type": "directory"})
			return
		}
		data, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "bad base64"})
			return
		}
		if body.Chunk <= 1 || body.Chunk == 0 {
			tf.files[p] = data
		} else {
			tf.files[p] = append(tf.files[p], data...)
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": p, "type": "file", "size": len(tf.files[p])})
	case http.MethodDelete:
		delete(tf.files, p)
		delete(tf.dirs, p)
		writeJSON(w, http.StatusNoContent, map[string]any{})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{})
	}
}

func (tf *treeFake) setDir(remote string, entries ...map[string]any) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.dirs[remote] = map[string]any{
		"name": filepath.Base(remote), "path": remote, "type": "directory", "content": entries,
	}
}

func (tf *treeFake) snapshot() (map[string][]byte, map[string]bool, []string) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	files := make(map[string][]byte, len(tf.files))
	for k, v := range tf.files {
		files[k] = append([]byte(nil), v...)
	}
	dirs := make(map[string]bool, len(tf.dirs))
	for k := range tf.dirs {
		dirs[k] = true
	}
	return files, dirs, append([]string(nil), tf.putPaths...)
}

func writeLocal(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestMkdir ports upstream tests/test_contents.py::test_mkdir: a directory is
// created with a plain {"type":"directory"} model PUT (PR #267).
func TestMkdir(t *testing.T) {
	srv := newTreeFake(t)
	c := mustClient(t, testConfig(srv.URL))
	if err := c.Mkdir(context.Background(), "/content/new_dir/"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	_, dirs, puts := srv.snapshot()
	if !dirs["content/new_dir"] || len(puts) != 1 {
		t.Fatalf("dirs=%v puts=%v", dirs, puts)
	}
}

// TestUploadDir ports upstream tests/test_contents.py::test_upload_dir: files
// and empty subdirectories are both preserved, with POSIX remote paths.
func TestUploadDir(t *testing.T) {
	srv := newTreeFake(t)
	c := mustClient(t, testConfig(srv.URL))
	root := t.TempDir()
	writeLocal(t, filepath.Join(root, "root.txt"), "root")
	writeLocal(t, filepath.Join(root, "sub", "bin.dat"), "\x00\x01\xff")

	n, err := c.UploadDir(context.Background(), root, "content/proj")
	if err != nil {
		t.Fatalf("UploadDir: %v", err)
	}
	if n != 2 {
		t.Errorf("file count = %d, want 2", n)
	}
	files, dirs, _ := srv.snapshot()
	if !dirs["content/proj"] || !dirs["content/proj/sub"] {
		t.Fatalf("remote dirs = %v", dirs)
	}
	if got := string(files["content/proj/root.txt"]); got != "root" {
		t.Errorf("root.txt = %q", got)
	}
	if got := files["content/proj/sub/bin.dat"]; string(got) != "\x00\x01\xff" {
		t.Errorf("bin.dat = %v", got)
	}
}

// TestUploadDirPreservesEmptyDirectories covers the explicit PR #267
// requirement that empty subdirectories are created remotely too.
func TestUploadDirPreservesEmptyDirectories(t *testing.T) {
	srv := newTreeFake(t)
	c := mustClient(t, testConfig(srv.URL))
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UploadDir(context.Background(), root, "content/proj"); err != nil {
		t.Fatalf("UploadDir: %v", err)
	}
	_, dirs, _ := srv.snapshot()
	if !dirs["content/proj/empty"] {
		t.Fatalf("empty subdirectory missing from %v", dirs)
	}
}

// TestUploadDirCreatesMissingRemoteParents ports upstream
// tests/test_contents.py::test_upload_dir_creates_missing_remote_parents.
func TestUploadDirCreatesMissingRemoteParents(t *testing.T) {
	srv := newTreeFake(t)
	c := mustClient(t, testConfig(srv.URL))
	root := t.TempDir()
	writeLocal(t, filepath.Join(root, "a.txt"), "hi")
	srv.mu.Lock()
	srv.failMkdir["content/a/b/proj"] = 1
	srv.failMkdir["content/a/b"] = 1
	srv.mu.Unlock()

	n, err := c.UploadDir(context.Background(), root, "content/a/b/proj")
	if err != nil {
		t.Fatalf("UploadDir: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
	_, dirs, puts := srv.snapshot()
	for _, d := range []string{"content/a", "content/a/b", "content/a/b/proj"} {
		if !dirs[d] {
			t.Errorf("missing directory %q in %v", d, dirs)
		}
	}
	wantOrder := []string{
		"content/a/b/proj", "content/a/b", "content/a", "content/a/b",
		"content/a/b/proj", "content/a/b/proj/a.txt",
	}
	if fmt.Sprint(puts) != fmt.Sprint(wantOrder) {
		t.Errorf("PUT order = %v, want %v", puts, wantOrder)
	}
}

// TestUploadDirDoesNotRecurseOn403 ports upstream
// tests/test_contents.py::test_upload_dir_does_not_recurse_on_403.
func TestUploadDirDoesNotRecurseOn403(t *testing.T) {
	srv := newTreeFake(t)
	srv.mu.Lock()
	srv.failMkdir["content/a/proj"] = 1
	srv.failMkdirStatus = http.StatusForbidden
	srv.mu.Unlock()
	c := mustClient(t, testConfig(srv.URL))
	root := t.TempDir()
	writeLocal(t, filepath.Join(root, "a.txt"), "hi")

	_, err := c.UploadDir(context.Background(), root, "content/a/proj")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("err = %v (%T), want StatusError 403", err, err)
	}
	if n := srv.callCount(); n != 1 {
		t.Fatalf("requests = %d, want 1 (no recursion on 4xx)", n)
	}
}

// TestUploadDirSkipsSymlinks ports upstream
// tests/test_contents.py::test_upload_dir_skips_symlinked_files_and_broken_symlinks.
func TestUploadDirSkipsSymlinks(t *testing.T) {
	srv := newTreeFake(t)
	c := mustClient(t, testConfig(srv.URL))
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeLocal(t, outside, "secret")
	root := t.TempDir()
	writeLocal(t, filepath.Join(root, "real.txt"), "ok")
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "linked_dir")); err != nil {
		t.Fatal(err)
	}

	n, err := c.UploadDir(context.Background(), root, "content/proj")
	if err != nil {
		t.Fatalf("UploadDir: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1 (symlinks skipped)", n)
	}
	files, _, _ := srv.snapshot()
	if len(files) != 1 || string(files["content/proj/real.txt"]) != "ok" {
		t.Fatalf("unexpected uploaded files: %v", files)
	}
}

// TestUploadDirNormalizesRemotePath ports upstream
// tests/test_contents.py::test_upload_dir_normalizes_remote_dir.
func TestUploadDirNormalizesRemotePath(t *testing.T) {
	srv := newTreeFake(t)
	c := mustClient(t, testConfig(srv.URL))
	root := t.TempDir()
	writeLocal(t, filepath.Join(root, "a.txt"), "hi")

	if _, err := c.UploadDir(context.Background(), root, "./content//proj/"); err != nil {
		t.Fatalf("UploadDir: %v", err)
	}
	_, dirs, puts := srv.snapshot()
	if !dirs["content/proj"] || puts[len(puts)-1] != "content/proj/a.txt" {
		t.Fatalf("dirs=%v puts=%v", dirs, puts)
	}
	if _, err := c.UploadDir(context.Background(), root, "../escape"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v, want ErrUnsafePath", err)
	}
}

// TestUploadDirRejectsNonDirectory ports upstream
// tests/test_contents.py::test_upload_dir_raises_on_non_directory_or_walk_error.
func TestUploadDirRejectsNonDirectory(t *testing.T) {
	srv := newTreeFake(t)
	c := mustClient(t, testConfig(srv.URL))
	f := filepath.Join(t.TempDir(), "file.txt")
	writeLocal(t, f, "hi")
	if _, err := c.UploadDir(context.Background(), f, "content/proj"); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("err = %v, want ErrNotDirectory", err)
	}
	if len(srv.requests()) != 0 {
		t.Fatal("no requests expected for a non-directory source")
	}
}

// TestDownloadDir ports upstream tests/test_contents.py::test_download_dir.
func TestDownloadDir(t *testing.T) {
	srv := newTreeFake(t)
	srv.setDir("content/proj",
		map[string]any{"name": "root.txt", "type": "file"},
		map[string]any{"name": "nb.ipynb", "type": "notebook"},
		map[string]any{"name": "sub", "type": "directory"},
		map[string]any{"name": "empty", "type": "directory"},
	)
	srv.setDir("content/proj/sub", map[string]any{"name": "bin.dat", "type": "file"})
	srv.setDir("content/proj/empty")
	srv.mu.Lock()
	srv.files["content/proj/root.txt"] = []byte("hello")
	srv.files["content/proj/nb.ipynb"] = []byte(`{"cells":[],"nbformat":4}`)
	srv.files["content/proj/sub/bin.dat"] = []byte{0, 2}
	srv.mu.Unlock()
	c := mustClient(t, testConfig(srv.URL))

	dest := filepath.Join(t.TempDir(), "out")
	n, err := c.DownloadDir(context.Background(), "content/proj", dest)
	if err != nil {
		t.Fatalf("DownloadDir: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
	assertFileBytes(t, filepath.Join(dest, "root.txt"), []byte("hello"))
	assertFileBytes(t, filepath.Join(dest, "sub", "bin.dat"), []byte{0, 2})
	info, err := os.Stat(filepath.Join(dest, "empty"))
	if err != nil || !info.IsDir() {
		t.Fatalf("empty directory not preserved: %v", err)
	}
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
}

// TestDownloadDirSingleFile ports upstream
// tests/test_contents.py::test_download_dir_single_file.
func TestDownloadDirSingleFile(t *testing.T) {
	srv := newTreeFake(t)
	srv.mu.Lock()
	srv.files["content/solo.txt"] = []byte("solo")
	srv.mu.Unlock()
	c := mustClient(t, testConfig(srv.URL))
	dest := filepath.Join(t.TempDir(), "nested", "solo.txt")

	n, err := c.DownloadDir(context.Background(), "content/solo.txt", dest)
	if err != nil || n != 1 {
		t.Fatalf("DownloadDir = %d, %v", n, err)
	}
	assertFileBytes(t, dest, []byte("solo"))
}

// TestDownloadDirSingleFileIntoDirectory ports upstream
// tests/test_contents.py::test_download_dir_single_file_into_existing_or_trailing_slash_dir.
func TestDownloadDirSingleFileIntoDirectory(t *testing.T) {
	srv := newTreeFake(t)
	srv.mu.Lock()
	srv.files["content/solo.txt"] = []byte("solo")
	srv.mu.Unlock()
	c := mustClient(t, testConfig(srv.URL))
	root := t.TempDir()

	existing := filepath.Join(root, "existing")
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DownloadDir(context.Background(), "content/solo.txt", existing); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, filepath.Join(existing, "solo.txt"), []byte("solo"))

	trailing := filepath.Join(root, "new_dir") + string(filepath.Separator)
	if _, err := c.DownloadDir(context.Background(), "content/solo.txt", trailing); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, filepath.Join(root, "new_dir", "solo.txt"), []byte("solo"))
}

// TestDownloadDirRejectsUnsafeEntryNames ports upstream
// tests/test_contents.py::test_download_dir_rejects_unsafe_entry_variants.
func TestDownloadDirRejectsUnsafeEntryNames(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "bad\x00name"} {
		t.Run(fmt.Sprintf("%q", bad), func(t *testing.T) {
			srv := newTreeFake(t)
			srv.setDir("content/dir", map[string]any{"name": bad, "type": "file"})
			c := mustClient(t, testConfig(srv.URL))
			_, err := c.DownloadDir(context.Background(), "content/dir", filepath.Join(t.TempDir(), "out"))
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("err = %v, want ErrUnsafePath for name %q", err, bad)
			}
		})
	}
}

// TestDownloadDirRejectsLocalSymlinks ports upstream
// tests/test_contents.py::test_download_dir_rejects_local_symlink and
// test_download_dir_rejects_trailing_slash_local_symlink.
func TestDownloadDirRejectsLocalSymlinks(t *testing.T) {
	srv := newTreeFake(t)
	srv.setDir("content/dir", map[string]any{"name": "file.txt", "type": "file"})
	c := mustClient(t, testConfig(srv.URL))
	root := t.TempDir()
	outside := filepath.Join(root, "outside.txt")
	writeLocal(t, outside, "keep")
	dest := filepath.Join(root, "out")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "file.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := c.DownloadDir(context.Background(), "content/dir", dest); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
	assertFileBytes(t, outside, []byte("keep"))

	// A symlinked destination directory (even with a trailing separator) must
	// not be traversed.
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DownloadDir(context.Background(), "content/dir", link+string(filepath.Separator)); !errors.Is(err, ErrSymlink) {
		t.Fatalf("trailing-slash symlink err = %v, want ErrSymlink", err)
	}
	entries, err := os.ReadDir(real)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote through symlinked directory: %v, %v", entries, err)
	}
}

// TestDownloadDirMaxDepthGuard ports upstream
// tests/test_contents.py::test_download_dir_max_depth_guard.
func TestDownloadDirMaxDepthGuard(t *testing.T) {
	srv := newTreeFake(t)
	loopDir := map[string]any{
		"name": "loop", "type": "directory",
		"content": []any{map[string]any{"name": "loop", "type": "directory"}},
	}
	srv.mu.Lock()
	srv.always = loopDir
	srv.mu.Unlock()
	c := mustClient(t, testConfig(srv.URL)).(*client)
	c.maxDepth = 3

	_, err := c.DownloadDir(context.Background(), "content/loop", filepath.Join(t.TempDir(), "out"))
	if !errors.Is(err, ErrMaxDepth) {
		t.Fatalf("err = %v, want ErrMaxDepth", err)
	}
}

// TestDownloadDirIgnoresUntrustedItemPath ports upstream
// tests/test_contents.py::test_download_dir_ignores_untrusted_item_path: only
// the validated `name` is used; a hostile `path` field is ignored.
func TestDownloadDirIgnoresUntrustedItemPath(t *testing.T) {
	srv := newTreeFake(t)
	srv.setDir("content/proj", map[string]any{
		"name": "safe.txt", "path": "other/untrusted.txt", "type": "file",
	})
	srv.mu.Lock()
	srv.files["content/proj/safe.txt"] = []byte("trusted")
	srv.mu.Unlock()
	c := mustClient(t, testConfig(srv.URL))
	dest := filepath.Join(t.TempDir(), "out")

	if _, err := c.DownloadDir(context.Background(), "content/proj", dest); err != nil {
		t.Fatalf("DownloadDir: %v", err)
	}
	assertFileBytes(t, filepath.Join(dest, "safe.txt"), []byte("trusted"))
}

// TestDownloadRejectsMissingContent ports upstream
// tests/test_contents.py::test_download_none_content_raises.
func TestDownloadRejectsMissingContent(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"name": "f.txt", "type": "file", "format": "text", "content": nil,
		})
	})
	c := mustClient(t, testConfig(srv.URL))
	if _, err := c.DownloadDir(context.Background(), "content/f.txt", filepath.Join(t.TempDir(), "f.txt")); !errors.Is(err, ErrMissingContent) {
		t.Fatalf("err = %v, want ErrMissingContent", err)
	}
}
