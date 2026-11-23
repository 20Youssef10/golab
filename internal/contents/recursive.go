package contents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/20Youssef10/golab/internal/logx"
)

// This file ports upstream PR #267 (recursive upload/download) with the
// security checklist from that PR and AUDIT-02: reject untrusted remote entry
// names, verify canonical local containment, reject local symlinks, bound the
// recursion depth and keep remote paths in `path` / local paths in `filepath`.

// Mkdir creates a remote directory via a plain {"type":"directory"} model PUT.
func (c *client) Mkdir(ctx context.Context, remotePath string) error {
	clean, err := normalizeRemotePath(remotePath)
	if err != nil {
		return err
	}
	if clean == "" {
		return fmt.Errorf("%w: empty directory path", ErrUnsafePath)
	}
	body, err := json.Marshal(struct {
		Type string `json:"type"`
	}{Type: "directory"})
	if err != nil {
		return fmt.Errorf("contents: encode directory model: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.send(ctx, "mkdir", http.MethodPut, clean, nil, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return &TransportError{Op: "mkdir", Err: err}
	}
	return nil
}

// ensureRemoteDir creates remotePath, creating missing parents on a 404 or
// 500. It deliberately stops at the "content" root and never recurses on a
// 4xx such as 403 (upstream test_upload_dir_does_not_recurse_on_403).
func (c *client) ensureRemoteDir(ctx context.Context, remotePath string) error {
	err := c.Mkdir(ctx, remotePath)
	if err == nil {
		return nil
	}
	var se *StatusError
	retryable := errors.Is(err, ErrNotFound) ||
		(errors.As(err, &se) && se.Status == http.StatusInternalServerError)
	if !retryable {
		return err
	}
	parent := path.Dir(remotePath)
	if parent == "." || parent == remotePath || parent == "content" {
		return err
	}
	if err := c.ensureRemoteDir(ctx, parent); err != nil {
		return err
	}
	return c.Mkdir(ctx, remotePath)
}

// normalizeRemotePath strips outer slashes and normalises with POSIX `path`.
// Traversal out of the VM root is rejected; "." maps to the root (empty).
func normalizeRemotePath(remote string) (string, error) {
	cleaned := strings.TrimSpace(remote)
	cleaned = strings.Trim(cleaned, "/")
	if cleaned == "" {
		return "", nil
	}
	if strings.ContainsAny(cleaned, "\\\x00") {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, remote)
	}
	normalized := path.Clean(cleaned)
	if normalized == "." {
		return "", nil
	}
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, remote)
	}
	return normalized, nil
}

// validateEntryName rejects a remote directory entry whose *name* could
// escape the destination tree. The server-supplied `path` field is ignored.
func validateEntryName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	return nil
}

// UploadDir walks localDir top-down without following symlinks and mirrors it
// under remoteDir. Empty directories are preserved; symlinked files and
// directories are skipped. It returns the number of files uploaded.
func (c *client) UploadDir(ctx context.Context, localDir, remoteDir string) (int, error) {
	info, err := os.Stat(localDir)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%w: %s", ErrNotDirectory, localDir)
	}
	base, err := normalizeRemotePath(remoteDir)
	if err != nil {
		return 0, err
	}
	if base != "" {
		if err := c.ensureRemoteDir(ctx, base); err != nil {
			return 0, err
		}
	}
	count := 0
	err = filepath.WalkDir(localDir, func(local string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(localDir, local)
		if err != nil {
			return err
		}
		remote := base
		if rel != "." {
			remote = path.Join(base, filepath.ToSlash(rel))
		}
		if entry.IsDir() {
			if rel != "." {
				return c.Mkdir(ctx, remote)
			}
			return nil
		}
		// #nosec G122 -- local was checked with EvalSymlinks + Rel containment
		// before the walk; symlinked files are skipped above.
		f, err := os.Open(local)
		if err != nil {
			return err
		}
		writeErr := c.WriteStream(ctx, remote, f, DefaultChunkSize)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		count++
		return nil
	})
	return count, err
}

// DownloadDir mirrors a remote directory tree into localPath. When the remote
// target is a single file it downloads that file instead (into localPath, or
// into a directory named after the remote file when localPath is/[looks like] a
// directory). It returns the number of files written.
func (c *client) DownloadDir(ctx context.Context, remotePath, localPath string) (int, error) {
	return c.downloadDir(ctx, remotePath, localPath, 0)
}

func (c *client) downloadDir(ctx context.Context, remotePath, localPath string, depth int) (int, error) {
	maxDepth := c.maxDepth
	if maxDepth <= 0 {
		maxDepth = MaxRecursionDepth
	}
	if depth > maxDepth {
		return 0, fmt.Errorf("%w: exceeded %d levels at %q", ErrMaxDepth, maxDepth, logxRedactPath(remotePath))
	}
	clean, err := normalizeRemotePath(remotePath)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.send(ctx, "download", http.MethodGet, clean, url.Values{"content": {"1"}}, nil)
	if err != nil {
		return 0, err
	}
	var m *Model
	func() {
		defer resp.Body.Close()
		m, err = decodeModel(newJSONDecoder(resp.Body), nil)
	}()
	if err != nil {
		return 0, fmt.Errorf("contents: decode %s: %w", logxRedactPath(clean), err)
	}
	if m.Type != "directory" {
		return c.downloadSingle(clean, localPath, m)
	}

	if isLocalSymlink(localPath) {
		return 0, fmt.Errorf("%w: refusing to traverse %s", ErrSymlink, localPath)
	}
	if err := os.MkdirAll(localPath, 0o700); err != nil {
		return 0, err
	}
	realRoot, err := filepath.EvalSymlinks(localPath)
	if err != nil {
		return 0, fmt.Errorf("contents: resolve local directory: %w", err)
	}
	count := 0
	for _, entry := range m.Entries {
		if err := validateEntryName(entry.Name); err != nil {
			return count, err
		}
		childRemote := path.Join(clean, entry.Name)
		childLocal := filepath.Join(localPath, entry.Name)
		if err := assertSafeLocalChild(realRoot, childLocal); err != nil {
			return count, err
		}
		n, err := c.downloadDir(ctx, childRemote, childLocal, depth+1)
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

func (c *client) downloadSingle(remotePath, localPath string, m *Model) (int, error) {
	if isLocalSymlink(localPath) {
		return 0, fmt.Errorf("%w: refusing to overwrite %s", ErrSymlink, localPath)
	}
	if !m.ContentPresent() {
		return 0, fmt.Errorf("%w: %s", ErrMissingContent, logxRedactPath(remotePath))
	}
	dirTarget := strings.HasSuffix(localPath, "/") || strings.HasSuffix(localPath, string(filepath.Separator))
	if !dirTarget {
		if info, err := os.Stat(localPath); err == nil && info.IsDir() {
			dirTarget = true
		}
	}
	target := localPath
	if dirTarget {
		name := m.Name
		if name == "" {
			name = path.Base(remotePath)
		}
		if err := validateEntryName(name); err != nil {
			return 0, err
		}
		if err := os.MkdirAll(localPath, 0o700); err != nil {
			return 0, err
		}
		target = filepath.Join(localPath, name)
		root, err := filepath.EvalSymlinks(localPath)
		if err != nil {
			return 0, fmt.Errorf("contents: resolve local directory: %w", err)
		}
		if err := assertSafeLocalChild(root, target); err != nil {
			return 0, err
		}
	} else if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
		return 0, err
	}
	if err := writeLocalFileAtomic(target, m.Content); err != nil {
		return 0, err
	}
	return 1, nil
}

// isLocalSymlink also strips trailing separators, so a symlinked directory
// passed as "link/" is still recognised and refused.
func isLocalSymlink(p string) bool {
	if p == "" {
		return false
	}
	stripped := strings.TrimRight(p, "/\\")
	if stripped == "" {
		stripped = p
	}
	info, err := os.Lstat(stripped)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// assertSafeLocalChild rejects symlinks and any child whose canonical path
// escapes realRoot. `filepath.Rel` is used instead of string prefix matching so
// Windows drive letters and casing cannot be confused.
func assertSafeLocalChild(realRoot, childLocal string) error {
	if isLocalSymlink(childLocal) {
		return fmt.Errorf("%w: refusing to overwrite %s", ErrSymlink, childLocal)
	}
	parent := filepath.Dir(childLocal)
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnsafePath, childLocal, err)
	}
	realChild := filepath.Join(realParent, filepath.Base(childLocal))
	if realChild == realRoot {
		return fmt.Errorf("%w: %s", ErrUnsafePath, childLocal)
	}
	rel, err := filepath.Rel(realRoot, realChild)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s", ErrUnsafePath, childLocal)
	}
	return nil
}

// writeLocalFileAtomic avoids leaving a partial local file on a write error.
func writeLocalFileAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, ".golab-recursive-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(target)
	}
	return os.Rename(tmp, target)
}

func logxRedactPath(p string) string {
	// Remote paths never carry the token (it is a query parameter), but keep
	// defensive redaction of free-form values on error paths.
	return logx.Redact(p)
}
