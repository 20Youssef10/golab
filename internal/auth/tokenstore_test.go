// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
//
// Token-store tests port the storage half of upstream tests/test_auth.py and
// the file-permission intent of tests/test_local_credentials.py. They use the
// in-memory keyring and a temporary config directory only.
package auth

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func newTestTieredStore(t *testing.T, kr KeyringBackend, stderr io.Writer) (*tieredStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, OAuthTokenFileName)
	st := &tieredStore{
		kr:      kr,
		file:    newFileStore(path),
		path:    path,
		stderr:  stderr,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		timeout: 20 * time.Millisecond,
	}
	return st, path
}

// Ports upstream test_get_credentials_keyring_timeout_falls_back_to_remote_flow:
// a keyring call that hangs beyond the probe timeout must fall back, and the
// probe result is cached for the lifetime of the process (ADR-006). The fallback
// is routine, so an ordinary read-only command must stay silent on stderr.
func TestKeyringProbeTimeoutIsCached(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	kr := &memKeyring{data: map[string]string{}, blockGet: block}
	stderr := &bytes.Buffer{}
	st, _ := newTestTieredStore(t, kr, stderr)

	for i := 0; i < 2; i++ {
		if _, err := st.Load(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Load %d error = %v; want file-tier not-exist", i, err)
		}
	}
	kr.mu.Lock()
	gets := kr.gets
	kr.mu.Unlock()
	if gets != 1 {
		t.Fatalf("keyring probed %d times; want once (cached per process)", gets)
	}
	if stderr.String() != "" {
		t.Fatalf("file fallback must not emit a default stderr notice: %q", stderr.String())
	}
}

// Ports upstream test_files_are_private_to_the_user (fresh and was-0644): the
// file fallback is always created owner-only.
func TestFileStoreIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", OAuthTokenFileName)
	fs := newFileStore(path)

	if err := fs.Save([]byte("secret-blob")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0666 regardless of chmod; ACLs are the real control there.
	if got := info.Mode().Perm(); goruntime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	dinfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dinfo.Mode().Perm(); goruntime.GOOS != "windows" && got != 0o700 {
		t.Fatalf("dir mode = %o, want 700", got)
	}
	b, err := fs.Load()
	if err != nil || string(b) != "secret-blob" {
		t.Fatalf("Load = %q, %v", b, err)
	}
	// Re-saving over a 0644 file must restore owner-only permissions.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fs.Save([]byte("secret-blob-2")); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	if got := info.Mode().Perm(); goruntime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("mode after re-save = %o, want 600", got)
	}
	if err := fs.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still present after Delete: %v", err)
	}
}

// Ports upstream test_keyring_save_failure_warns_on_stderr: a failing keyring
// write still falls back to the 0600 file, but the tier hint is debug-only —
// the save itself succeeded, so there is no error to warn about by default.
func TestTieredStoreSaveFallbackQuietByDefault(t *testing.T) {
	kr := &memKeyring{data: map[string]string{}, setErr: fmt.Errorf("no backend")}
	stderr := &bytes.Buffer{}
	st, path := newTestTieredStore(t, kr, stderr)

	for i := 0; i < 2; i++ {
		if err := st.Save([]byte("blob")); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "blob" {
		t.Fatalf("file fallback did not receive the blob: %q, %v", b, err)
	}
	if stderr.String() != "" {
		t.Fatalf("failing keyring write must not emit a default stderr notice: %q", stderr.String())
	}
}

// The keyring fallback is routine (many hosts have no session bus), so the
// human-facing hint is gated behind debug logging (COLAB_DEBUG=1). The debug
// log entry is always recorded, so the diagnosis is never lost.
func TestKeyringFallbackNoticeOnlyInDebug(t *testing.T) {
	kr := &memKeyring{data: map[string]string{}, getErr: fmt.Errorf("no recommended backend")}

	// Default (info-level) logger: no stderr noise for an ordinary command.
	stderr := &bytes.Buffer{}
	st, _ := newTestTieredStore(t, kr, stderr)
	if _, err := st.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load error = %v; want file-tier not-exist", err)
	}
	if stderr.String() != "" {
		t.Fatalf("default mode emitted a keyring notice: %q", stderr.String())
	}

	// Debug-level logger (COLAB_DEBUG=1): show the hint once, keep the reason.
	debugLog := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(debugLog, &slog.HandlerOptions{Level: slog.LevelDebug}))
	debugStderr := &bytes.Buffer{}
	dbg, path := newTestTieredStore(t, kr, debugStderr)
	dbg.logger = logger
	if _, err := dbg.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("debug Load error = %v; want file-tier not-exist", err)
	}
	msg := debugStderr.String()
	if n := strings.Count(msg, "[colab]"); n != 1 {
		t.Fatalf("got %d notices: %q", n, msg)
	}
	if !strings.Contains(msg, "keyring") || !strings.Contains(msg, "--auth=adc") || !strings.Contains(msg, path) {
		t.Fatalf("debug notice lacks reason/advice/path: %q", msg)
	}
	if !strings.Contains(debugLog.String(), "keyring tier unavailable") ||
		!strings.Contains(debugLog.String(), "no recommended backend") {
		t.Fatalf("debug log lost the keyring reason: %q", debugLog.String())
	}
}

// The keyring tier uses the exact upstream service/user pair (doc 02 §1.1).
func TestKeyringTierUsesUpstreamIdentity(t *testing.T) {
	kr := &memKeyring{data: map[string]string{}}
	st, _ := newTestTieredStore(t, kr, io.Discard)
	if err := st.Save([]byte("blob")); err != nil {
		t.Fatal(err)
	}
	kr.mu.Lock()
	v, ok := kr.data[KeyringService+"\x00"+KeyringUser]
	kr.mu.Unlock()
	if !ok || v != "blob" {
		t.Fatalf("keyring entry = %q (found=%v); service=%q user=%q", v, ok, KeyringService, KeyringUser)
	}

	if err := st.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load after Delete = %v; want not-exist", err)
	}
}
