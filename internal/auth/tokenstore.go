// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package auth

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/20Youssef10/golab/internal/logx"
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

// notice emits one concise, secret-free line on stderr and a debug log entry.
func (s *tieredStore) notice(err error) {
	s.noticeOnce.Do(func() {
		fmt.Fprintf(s.stderr, "[colab] OS keyring unavailable; using owner-only token file %s. Use --auth=adc to avoid token storage.\n", s.path)
		if s.logger != nil {
			msg := ""
			if err != nil {
				msg = logx.Redact(err.Error())
			}
			s.logger.Debug("oauth2: keyring tier unavailable", "error", msg)
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
type fileStore struct {
	path string
}

func newFileStore(path string) *fileStore { return &fileStore{path: path} }

func (f *fileStore) Load() ([]byte, error) {
	return os.ReadFile(f.path)
}

func (f *fileStore) Save(data []byte) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
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
	err := os.Remove(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
