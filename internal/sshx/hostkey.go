package sshx

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/crypto/ssh"

	"github.com/20Youssef10/golab/internal/platform"
)

// HostKeyStore is the plain-value persistence contract for trust-on-first-use
// host keys. It deliberately has no state/CLI dependency so sshx stays a
// protocol package (ADR-005 package boundary). Implementations must be safe for
// concurrent use.
type HostKeyStore interface {
	// KnownHost returns the pinned SHA256 fingerprint for host. ok is false
	// when nothing has been recorded yet (a legacy session or a new runtime).
	KnownHost(host string) (fingerprint string, ok bool, err error)
	// RecordHost pins fingerprint for host. It must refuse to overwrite an
	// existing, different pin.
	RecordHost(host, fingerprint string) error
}

// HostKeyMismatchError is the typed, loud failure for a changed host key.
// Only SHA256 fingerprints are carried — never raw key material.
type HostKeyMismatchError struct {
	Host     string
	Expected string
	Got      string
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("SSH host key changed for %s: expected %s, got %s. "+
		"The runtime may have been recreated, or the connection may be intercepted. "+
		"Verify the runtime, then remove its entry from %s to trust the new key.",
		e.Host, e.Expected, e.Got, knownHostsFileName)
}

const knownHostsFileName = "known_hosts.json"

const fpPrefix = "SHA256:"

// NormalizeHostKeyFingerprint converts a user-supplied host-key fingerprint to
// the canonical form produced by ssh.FingerprintSHA256. Both `SHA256:<base64>`
// and a bare `<base64>` fingerprint are accepted (prefix matching is
// case-insensitive); surrounding whitespace is trimmed. Only the digest shape
// is checked — this function never sees or returns raw key material, and it is
// deliberately not an environment-variable input.
func NormalizeHostKeyFingerprint(value string) (string, error) {
	s := strings.TrimSpace(value)
	if len(s) >= len(fpPrefix) && strings.EqualFold(s[:len(fpPrefix)], fpPrefix) {
		s = s[len(fpPrefix):]
	}
	if s == "" {
		return "", errors.New("host-key fingerprint is empty")
	}
	digest, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil || len(digest) != sha256.Size {
		return "", fmt.Errorf("invalid SHA256 host-key fingerprint: expected %d base64 characters after %q",
			base64.RawStdEncoding.EncodedLen(sha256.Size), fpPrefix)
	}
	return fpPrefix + s, nil
}

// HostKeyExpectationError is the typed, loud failure when a user-supplied
// expected fingerprint disagrees with either the live handshake key (first
// use) or the already-stored pin (later use). It carries fingerprints only.
// Stored distinguishes the two cases so the diagnostic can say whether a pin
// was consulted; either way nothing is written and the connection is refused.
type HostKeyExpectationError struct {
	Host     string
	Expected string
	Got      string
	Stored   bool
}

func (e *HostKeyExpectationError) Error() string {
	if e.Stored {
		return fmt.Sprintf("host-key pin for %s is %s, but --accept-host-key expects %s; "+
			"refusing to replace a stored pin. Verify the runtime before changing its pin in %s.",
			e.Host, e.Got, e.Expected, knownHostsFileName)
	}
	return fmt.Sprintf("--accept-host-key %s does not match the runtime's host key %s for %s; "+
		"no pin was recorded.", e.Expected, e.Got, e.Host)
}

// expectedHostKeyStore decorates the TOFU store with an explicit expectation.
// It is not a replacement for verification: the existing TOFU callback still
// receives the real handshake key, still owns persistence, and still rejects
// changed keys. The decorator only (1) refuses a stored pin that disagrees
// with the expectation and (2) refuses to record any fingerprint other than
// the expected one. Consequently an expected value is never persisted without
// the handshake first proving the live key matches it.
type expectedHostKeyStore struct {
	inner    HostKeyStore
	expected string
}

func (s *expectedHostKeyStore) KnownHost(host string) (string, bool, error) {
	fp, ok, err := s.inner.KnownHost(host)
	if err != nil || !ok {
		return fp, ok, err
	}
	if fp != s.expected {
		return "", false, &HostKeyExpectationError{Host: host, Expected: s.expected, Got: fp, Stored: true}
	}
	return fp, true, nil
}

func (s *expectedHostKeyStore) RecordHost(host, fp string) error {
	if fp != s.expected {
		return &HostKeyExpectationError{Host: host, Expected: s.expected, Got: fp}
	}
	return s.inner.RecordHost(host, fp)
}

// hostIdentity derives the TOFU identity from the proxy URL. Colab proxy URLs
// are unique per runtime, so a pin is scoped to one runtime rather than to the
// whole service; this also keeps a user-supplied ProxyURL in its own namespace.
func hostIdentity(proxyURL string) string {
	if u, err := url.Parse(proxyURL); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return "colab-runtime:22"
}

// tofuHostKeyCallback pins the first key seen for host and rejects any later
// change. A missing pin is trusted once and recorded (backward compatible with
// sessions that predate TOFU). All output is fingerprint-only.
func tofuHostKeyCallback(store HostKeyStore, host string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, pub ssh.PublicKey) error {
		if pub == nil {
			return errors.New("SSH host key missing from handshake")
		}
		fp := ssh.FingerprintSHA256(pub)
		known, ok, err := store.KnownHost(host)
		if err != nil {
			return fmt.Errorf("read host-key pin for %s: %w", host, err)
		}
		if !ok {
			if err := store.RecordHost(host, fp); err != nil {
				return fmt.Errorf("record host-key pin for %s: %w", host, err)
			}
			return nil
		}
		if known != fp {
			return &HostKeyMismatchError{Host: host, Expected: known, Got: fp}
		}
		return nil
	}
}

// FileHostKeyStore is a locked, atomically-written host -> fingerprint map in
// the config dir. A missing or empty file means "no pins yet"; no schema
// migration is involved and unrelated pins are preserved on every write.
type FileHostKeyStore struct{ path string }

// NewFileHostKeyStore resolves the store path. An explicit path wins (tests);
// otherwise the file lives at <config dir>/known_hosts.json. There is no
// env-controlled override: an attacker-supplied path could redirect the pins.
func NewFileHostKeyStore(path string) (*FileHostKeyStore, error) {
	if path == "" {
		d, err := platform.ConfigDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(d, knownHostsFileName)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &FileHostKeyStore{path: path}, nil
}

// Path returns the underlying file path.
func (s *FileHostKeyStore) Path() string { return s.path }

func (s *FileHostKeyStore) locked(ctx context.Context, fn func() error) error {
	lock := flock.New(s.path + ".lock")
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("timed out acquiring host-key store lock for %s", s.path)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

func (s *FileHostKeyStore) read() (map[string]string, error) {
	fi, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("unsafe host-key store %s: not a regular file", s.path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := platform.MakePrivate(s.path); err != nil {
			return nil, err
		}
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]string{}, nil
	}
	var out map[string]string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("corrupt host-key store %s: %w", s.path, err)
	}
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}

func (s *FileHostKeyStore) write(pins map[string]string) error {
	b, err := json.MarshalIndent(pins, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".golab-known-hosts-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
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
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	_ = platform.MakePrivate(s.path)
	// Best effort: some filesystems refuse to fsync a directory handle.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func (s *FileHostKeyStore) KnownHost(host string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var fp string
	var ok bool
	err := s.locked(ctx, func() error {
		pins, err := s.read()
		if err != nil {
			return err
		}
		fp, ok = pins[host]
		return nil
	})
	return fp, ok, err
}

func (s *FileHostKeyStore) RecordHost(host, fingerprint string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.locked(ctx, func() error {
		pins, err := s.read()
		if err != nil {
			return err
		}
		if prev, exists := pins[host]; exists {
			if prev != fingerprint {
				return &HostKeyMismatchError{Host: host, Expected: prev, Got: fingerprint}
			}
			return nil
		}
		pins[host] = fingerprint
		return s.write(pins)
	})
}
