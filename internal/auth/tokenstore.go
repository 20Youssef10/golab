// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/platform"
)

// Real OS keyring adapter. The 5 s bound is applied by tieredStore, because
// go-keyring's D-Bus helpers do not accept a context.
type realKeyring struct{}

func (realKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}
func (realKeyring) Set(service, user, value string) error {
	return keyring.Set(service, user, value)
}
func (realKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

var errKeyringTimeout = errors.New("OS keyring probe timed out")

// ErrUnsafeTokenFile means the token file exists but is not a safe owner-only
// regular file (symlink, directory, FIFO, or a mode that exposes the blob).
var ErrUnsafeTokenFile = errors.New("unsafe token file")

// withStringTimeout bounds a blocking keyring read. A helper thread that is
// stuck in D-Bus may outlive the deadline; it reports to a buffered channel
// and cannot block process exit (same approach as upstream's daemon thread).
func withStringTimeout(timeout time.Duration, fn func() (string, error)) (string, error) {
	type result struct {
		v   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-t.C:
		return "", errKeyringTimeout
	}
}

func withErrorTimeout(timeout time.Duration, fn func() error) error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-ch:
		return err
	case <-t.C:
		return errKeyringTimeout
	}
}

// tieredStore implements ADR-006: OS keyring, then an owner-only file in the
// config dir. The availability probe happens once per store instance (one
// provider per CLI process) with a hard timeout.
type tieredStore struct {
	kr      KeyringBackend
	file    TokenStore
	path    string
	stderr  io.Writer
	logger  *slog.Logger
	timeout time.Duration

	probeOnce  sync.Once
	available  bool
	probeData  []byte
	noticeOnce sync.Once
}

func (s *tieredStore) probe() {
	s.probeOnce.Do(func() {
		v, err := withStringTimeout(s.timeout, func() (string, error) {
			return s.kr.Get(KeyringService, KeyringUser)
		})
		switch {
		case err == nil:
			s.available = true
			s.probeData = []byte(v)
		case errors.Is(err, keyring.ErrNotFound):
			// The backend answered: it is usable, just empty.
			s.available = true
		default:
			s.available = false
			s.notice(err)
		}
	})
}

// notice records the keyring tier fallback. A missing OS keyring is routine on
// hosts without a session bus, not an auth failure, so the human-facing hint is
// debug-only (COLAB_DEBUG=1); the debug log entry is always written so the
// diagnosis survives in colab.log. Genuine failures (corrupt/refresh/save) are
// reported by oauth2Provider.notice and stay visible by default.
func (s *tieredStore) notice(err error) {
	s.noticeOnce.Do(func() {
		if s.logger != nil {
			msg := ""
			if err != nil {
				msg = logx.Redact(err.Error())
			}
			s.logger.Debug("oauth2: keyring tier unavailable", "error", msg)
			if s.logger.Enabled(context.Background(), slog.LevelDebug) {
				fmt.Fprintf(s.stderr, "[colab] OS keyring unavailable; using owner-only token file %s. Use --auth=adc to avoid token storage.\n", s.path)
			}
		}
	})
}

func (s *tieredStore) Load() ([]byte, error) {
	s.probe()
	if s.available {
		if s.probeData != nil {
			data := s.probeData
			s.probeData = nil
			return data, nil
		}
		v, err := withStringTimeout(s.timeout, func() (string, error) {
			return s.kr.Get(KeyringService, KeyringUser)
		})
		switch {
		case err == nil:
			return []byte(v), nil
		case errors.Is(err, keyring.ErrNotFound):
			return s.file.Load()
		default:
			s.notice(err)
			return s.file.Load()
		}
	}
	return s.file.Load()
}

func (s *tieredStore) Save(data []byte) error {
	s.probe()
	if s.available {
		err := withErrorTimeout(s.timeout, func() error {
			return s.kr.Set(KeyringService, KeyringUser, string(data))
		})
		if err == nil {
			return nil
		}
		s.notice(err)
	}
	return s.file.Save(data)
}

func (s *tieredStore) Delete() error {
	s.probe()
	var firstErr error
	if s.available {
		firstErr = withErrorTimeout(s.timeout, func() error {
			return s.kr.Delete(KeyringService, KeyringUser)
		})
		if errors.Is(firstErr, keyring.ErrNotFound) {
			firstErr = nil
		}
	}
	if err := s.file.Delete(); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// fileStore keeps a single owner-only JSON blob with atomic replacement.
// All three operations take the same cross-process lock as state.Store so a
// refresh in another process cannot race a write or read of the token file.
type fileStore struct {
	path string
}

func newFileStore(path string) *fileStore { return &fileStore{path: path} }

// locked mirrors internal/state's flock pattern. The parent directory is
// created before taking the lock so the lock file itself can be created on a
// first-ever Save. The lock file carries no secret material.
func (f *fileStore) locked(ctx context.Context, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	lock := flock.New(f.path + ".lock")
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("timed out acquiring token file lock for %s", f.path)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

func (f *fileStore) Load() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out []byte
	err := f.locked(ctx, func() error {
		var err error
		out, err = f.loadLocked()
		return err
	})
	return out, err
}

// loadLocked refuses a token path that is not a regular owner-owned file.
// Reading through a symlink (or from a FIFO/device) would let a planted path
// supply arbitrary credential material. The mode is validated and repaired
// after a successful read so a loose file is self-healed rather than leaving
// the user signed out. No token bytes ever reach an error or log.
func (f *fileStore) loadLocked() ([]byte, error) {
	info, err := os.Lstat(f.path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrUnsafeTokenFile, f.path)
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		return nil, err
	}
	// Close the Lstat→ReadFile window: the file actually read must still be
	// the regular file we validated, not a symlink swapped in meanwhile.
	after, err := os.Lstat(f.path)
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(info, after) {
		return nil, fmt.Errorf("%w: %s changed while being read", ErrUnsafeTokenFile, f.path)
	}
	// Re-apply the owner-only mode after a successful read (a no-op on
	// Windows, where ACLs are the real control).
	if err := platform.MakePrivate(f.path); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		final, err := os.Lstat(f.path)
		if err != nil {
			return nil, err
		}
		if got := final.Mode().Perm(); got&0o077 != 0 {
			return nil, fmt.Errorf("%w: %s mode %o is not owner-only", ErrUnsafeTokenFile, f.path, got)
		}
	}
	return data, nil
}

func (f *fileStore) Save(data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return f.locked(ctx, func() error { return f.saveLocked(data) })
}

func (f *fileStore) saveLocked(data []byte) error {
	dir := filepath.Dir(f.path)
	tmp, err := os.CreateTemp(dir, ".oauth-token-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Windows cannot rename over an existing file.
	if runtime.GOOS == "windows" {
		_ = os.Remove(f.path)
	}
	if err := os.Rename(tmpName, f.path); err != nil {
		return err
	}
	// Defensive: some filesystems ignore the temp file's mode on rename.
	return os.Chmod(f.path, 0o600)
}

func (f *fileStore) Delete() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return f.locked(ctx, f.deleteLocked)
}

func (f *fileStore) deleteLocked() error {
	err := os.Remove(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
