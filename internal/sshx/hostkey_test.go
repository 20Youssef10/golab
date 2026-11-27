package sshx

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/testutil/fakesshd"
)

// memHostKeyStore is a test-only, in-memory HostKeyStore. It keeps TOFU tests
// off the real config directory and makes every case independent.
type memHostKeyStore struct {
	mu   sync.Mutex
	pins map[string]string
}

func newMemHostKeyStore() *memHostKeyStore { return &memHostKeyStore{pins: map[string]string{}} }

func (m *memHostKeyStore) KnownHost(host string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fp, ok := m.pins[host]
	return fp, ok, nil
}

func (m *memHostKeyStore) RecordHost(host, fp string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pins[host] = fp
	return nil
}

// There is no upstream Python equivalent for these tests: upstream disables
// host-key checking entirely (ADR-005). They close confirmed defect §4.12,
// where the accept-any callback silently loses its TLS guarantee for a
// user-supplied plaintext ProxyURL.

// TestTOFUFirstConnectionRecordsKey: (a) the first connection records the
// host-key fingerprint rather than blindly trusting forever.
func TestTOFUFirstConnectionRecordsKey(t *testing.T) {
	store := newMemHostKeyStore()
	_, pub, err := ed25519KeyForTest()
	if err != nil {
		t.Fatal(err)
	}
	cb := tofuHostKeyCallback(store, "colab-runtime:22")
	if err := cb("colab-runtime:22", &net.TCPAddr{}, pub); err != nil {
		t.Fatalf("first connection rejected: %v", err)
	}
	got, ok, err := store.KnownHost("colab-runtime:22")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("first connection did not record a pin")
	}
	want := ssh.FingerprintSHA256(pub)
	if got != want {
		t.Fatalf("pin = %q; want %q", got, want)
	}
	// Never persist or expose raw key material.
	if strings.Contains(got, "ssh-ed25519") || strings.Contains(got, "AAAAC3Nza") {
		t.Fatalf("pin contains raw key material: %q", got)
	}
}

// TestTOFUChangedKeyIsRejected: (b) a different key on the same host is
// rejected with a typed error and does not overwrite the stored pin.
func TestTOFUChangedKeyIsRejected(t *testing.T) {
	store := newMemHostKeyStore()
	_, first, err := ed25519KeyForTest()
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := ed25519KeyForTest()
	if err != nil {
		t.Fatal(err)
	}
	cb := tofuHostKeyCallback(store, "colab-runtime:22")
	if err := cb("colab-runtime:22", &net.TCPAddr{}, first); err != nil {
		t.Fatalf("first connection rejected: %v", err)
	}
	original := ssh.FingerprintSHA256(first)

	err = cb("colab-runtime:22", &net.TCPAddr{}, second)
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("want *HostKeyMismatchError, got %T: %v", err, err)
	}
	if mismatch.Host != "colab-runtime:22" {
		t.Fatalf("mismatch host = %q", mismatch.Host)
	}
	if mismatch.Expected != original {
		t.Fatalf("mismatch expected = %q; want %q", mismatch.Expected, original)
	}
	if mismatch.Got != ssh.FingerprintSHA256(second) {
		t.Fatalf("mismatch got = %q", mismatch.Got)
	}
	// The first pin survives a rejected rotation.
	got, ok, _ := store.KnownHost("colab-runtime:22")
	if !ok || got != original {
		t.Fatalf("pin after rejected key = %q (present=%v); want %q", got, ok, original)
	}
	// The error names fingerprints only, never raw key bytes.
	if strings.Contains(err.Error(), "ssh-ed25519") || strings.Contains(err.Error(), "AAAAC3Nza") {
		t.Fatalf("mismatch error leaks raw key material: %q", err.Error())
	}
}

// TestTOFUNoPinStillConnects: (c) a host with no stored fingerprint (an
// existing session created before TOFU, or a brand-new runtime) still works —
// it is trusted on first use and a pin is recorded.
func TestTOFUNoPinStillConnects(t *testing.T) {
	store := newMemHostKeyStore()
	_, pub, err := ed25519KeyForTest()
	if err != nil {
		t.Fatal(err)
	}
	cb := tofuHostKeyCallback(store, "colab-runtime:22")
	if err := cb("colab-runtime:22", &net.TCPAddr{}, pub); err != nil {
		t.Fatalf("host without a pin was rejected: %v", err)
	}
	if _, ok, _ := store.KnownHost("colab-runtime:22"); !ok {
		t.Fatal("host without a pin was not recorded")
	}
}

func TestFileHostKeyStoreIsAdditiveAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	// A hand-written store with one pin and no entry for the new host must
	// remain readable: no schema migration, no loss of existing pins.
	if err := os.WriteFile(path, []byte(`{"legacy-host":"SHA256:legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileHostKeyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if fp, ok, err := store.KnownHost("legacy-host"); err != nil || !ok || fp != "SHA256:legacy" {
		t.Fatalf("legacy pin = %q, %v, %v", fp, ok, err)
	}
	if _, ok, err := store.KnownHost("new-host"); err != nil || ok {
		t.Fatalf("unexpected pin for new host: %v, %v", ok, err)
	}
	if err := store.RecordHost("new-host", "SHA256:new"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SHA256:legacy", "SHA256:new"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("store lost %q: %s", want, b)
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Unix mode bits are not meaningful on Windows: Go reports 0666 for a
		// regular file there regardless of chmod, and platform.MakePrivate is
		// a no-op because %APPDATA% is already user-ACL restricted. Assert
		// only that the store exists as a regular file on this platform.
		if !fi.Mode().IsRegular() {
			t.Fatalf("store is not a regular file: %v", fi.Mode())
		}
	} else if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("store is not owner-only: %v", fi.Mode().Perm())
	}
	// Overwriting an existing pin with a different value must not silently
	// happen through RecordHost.
	if err := store.RecordHost("new-host", "SHA256:attacker"); err == nil {
		t.Fatal("RecordHost silently overwrote an existing pin")
	}
}

// TestRunRejectsChangedHostKey proves the wiring: a real handshake against the
// fake runtime fails with the typed mismatch when a stale pin is present.
func TestRunRejectsChangedHostKey(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{})
	store := newMemHostKeyStore()
	if err := store.RecordHost(hostIdentity(srv.URL()), "SHA256:stale-pin"); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	rc, err := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeCmd,
		Identity: writeKey(t), Command: []string{"echo", "hi"},
		HostKeyStore: store,
		Stdin:        strings.NewReader(""), Stdout: &out, Stderr: &out,
		StdinTTY: boolPtr(false), StdoutTTY: boolPtr(false),
	}).Run(context.Background())
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Run rc=%d err=%T: %v; want *HostKeyMismatchError", rc, err, err)
	}
	if rc != 255 {
		t.Fatalf("rc = %d; want 255", rc)
	}
}

// There is no upstream Python equivalent: upstream disables host-key checking
// entirely (ADR-005). These tests close the flagged follow-up to defect §4.12
// by pinning an expected fingerprint BEFORE the handshake without weakening
// the existing TOFU pin-and-reject semantics.

// knownHostKeyForTest returns a fake SSH server host key together with its
// canonical SHA256 fingerprint. Computing it from the key under test keeps the
// expected value available BEFORE the handshake and avoids hard-coding key
// material (or fingerprint blobs) in the tree.
func knownHostKeyForTest(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	signer, err := ed25519SignerForTest()
	if err != nil {
		t.Fatal(err)
	}
	return signer, ssh.FingerprintSHA256(signer.PublicKey())
}

// handshakeWithKnownServer performs a real x/crypto/ssh handshake against a
// one-shot in-process SSH server whose host key is hostSigner. It exercises
// the exact callback shape the runner installs instead of calling the callback
// directly, and returns the client-side handshake error.
func handshakeWithKnownServer(t *testing.T, hostSigner ssh.Signer, cb ssh.HostKeyCallback) error {
	t.Helper()
	serverCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, nil // accept any client key; only the host key is under test
		},
	}
	serverCfg.AddHostKey(hostSigner)
	// A real loopback socket, not net.Pipe: both SSH peers write their version
	// banner before reading, which would deadlock on an unbuffered pipe.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		serverSide, err := ln.Accept()
		if err != nil {
			return
		}
		conn, _, _, err := ssh.NewServerConn(serverSide, serverCfg)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		<-stop // released once the client is done; no channels are opened
	}()
	clientSigner, err := ed25519SignerForTest()
	if err != nil {
		t.Fatal(err)
	}
	clientSide, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cc, _, _, err := ssh.NewClientConn(clientSide, "test-host:22",
		newSSHClientConfig([]ssh.Signer{clientSigner}, cb))
	if err == nil {
		_ = cc.Close()
	}
	_ = clientSide.Close()
	close(stop)
	<-done
	return err
}

// TestAcceptHostKeyPinsOnFirstUse: (a) the correct explicit fingerprint is
// accepted on first use and recorded. Both the canonical SHA256: form and a
// bare fingerprint must work.
func TestAcceptHostKeyPinsOnFirstUse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spell func(string) string
	}{
		{"prefixed", func(fp string) string { return fp }},
		{"bare", func(fp string) string { return strings.TrimPrefix(fp, "SHA256:") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hostSigner, fp := knownHostKeyForTest(t)
			store := newMemHostKeyStore()
			r, err := New(Config{ExpectedHostKey: tc.spell(fp), HostKeyStore: store})
			if err != nil {
				t.Fatal(err)
			}
			cb := tofuHostKeyCallback(r.cfg.HostKeyStore, "test-host:22")
			if err := handshakeWithKnownServer(t, hostSigner, cb); err != nil {
				t.Fatalf("handshake with correct expected key failed: %v", err)
			}
			got, ok, err := store.KnownHost("test-host:22")
			if err != nil || !ok {
				t.Fatalf("first use did not pin: %q ok=%v err=%v", got, ok, err)
			}
			if got != fp {
				t.Fatalf("pin = %q; want %q", got, fp)
			}
			if strings.Contains(got, "ssh-ed25519") || strings.Contains(got, "AAAAC3Nza") {
				t.Fatalf("pin leaked raw key material: %q", got)
			}
		})
	}
}

// TestAcceptHostKeyWrongExpectedWritesNoPin: (b) a wrong explicit fingerprint
// is a hard error and never persists an unverified value.
func TestAcceptHostKeyWrongExpectedWritesNoPin(t *testing.T) {
	hostSigner, _ := knownHostKeyForTest(t)
	other, err := ed25519SignerForTest()
	if err != nil {
		t.Fatal(err)
	}
	wrong := ssh.FingerprintSHA256(other.PublicKey())
	store := newMemHostKeyStore()
	r, err := New(Config{ExpectedHostKey: wrong, HostKeyStore: store})
	if err != nil {
		t.Fatal(err)
	}
	cb := tofuHostKeyCallback(r.cfg.HostKeyStore, "test-host:22")
	err = handshakeWithKnownServer(t, hostSigner, cb)
	var expectErr *HostKeyExpectationError
	if !errors.As(err, &expectErr) {
		t.Fatalf("want *HostKeyExpectationError, got %T: %v", err, err)
	}
	if expectErr.Expected != wrong {
		t.Fatalf("expected = %q; want %q", expectErr.Expected, wrong)
	}
	if want := ssh.FingerprintSHA256(hostSigner.PublicKey()); expectErr.Got != want {
		t.Fatalf("got = %q; want live fingerprint %q", expectErr.Got, want)
	}
	if expectErr.Stored {
		t.Fatal("first-use mismatch reported as a stored-pin mismatch")
	}
	if _, ok, _ := store.KnownHost("test-host:22"); ok {
		t.Fatal("wrong expected fingerprint wrote a pin")
	}
	if strings.Contains(err.Error(), "ssh-ed25519") || strings.Contains(err.Error(), "AAAAC3Nza") {
		t.Fatalf("error leaked raw key material: %q", err.Error())
	}
}

// TestAcceptHostKeyRejectsChangedServerKey: (c) a later connection using a
// different server key is rejected and the original pin survives.
func TestAcceptHostKeyRejectsChangedServerKey(t *testing.T) {
	hostSigner, fp := knownHostKeyForTest(t)
	rotated, err := ed25519SignerForTest()
	if err != nil {
		t.Fatal(err)
	}
	store := newMemHostKeyStore()
	r, err := New(Config{ExpectedHostKey: fp, HostKeyStore: store})
	if err != nil {
		t.Fatal(err)
	}
	cb := tofuHostKeyCallback(r.cfg.HostKeyStore, "test-host:22")
	if err := handshakeWithKnownServer(t, hostSigner, cb); err != nil {
		t.Fatalf("first handshake failed: %v", err)
	}
	err = handshakeWithKnownServer(t, rotated, cb)
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("want *HostKeyMismatchError for rotated key, got %T: %v", err, err)
	}
	if got, ok, _ := store.KnownHost("test-host:22"); !ok || got != fp {
		t.Fatalf("pin after rejected rotation = %q ok=%v; want %q", got, ok, fp)
	}
}

// TestAcceptHostKeyMustMatchStoredPin: a later --accept-host-key value must
// match the stored pin; a disagreement is a hard error even when the live key
// still matches the stored pin. The pin is never replaced.
func TestAcceptHostKeyMustMatchStoredPin(t *testing.T) {
	hostSigner, fp := knownHostKeyForTest(t)
	other, err := ed25519SignerForTest()
	if err != nil {
		t.Fatal(err)
	}
	store := newMemHostKeyStore()
	first, err := New(Config{ExpectedHostKey: fp, HostKeyStore: store})
	if err != nil {
		t.Fatal(err)
	}
	if err := handshakeWithKnownServer(t, hostSigner,
		tofuHostKeyCallback(first.cfg.HostKeyStore, "test-host:22")); err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{ExpectedHostKey: ssh.FingerprintSHA256(other.PublicKey()), HostKeyStore: store})
	if err != nil {
		t.Fatal(err)
	}
	err = handshakeWithKnownServer(t, hostSigner,
		tofuHostKeyCallback(second.cfg.HostKeyStore, "test-host:22"))
	var expectErr *HostKeyExpectationError
	if !errors.As(err, &expectErr) || !expectErr.Stored {
		t.Fatalf("want stored *HostKeyExpectationError, got %T: %v", err, err)
	}
	if got, ok, _ := store.KnownHost("test-host:22"); !ok || got != fp {
		t.Fatalf("stored pin changed: %q ok=%v; want %q", got, ok, fp)
	}
}

// TestNormalizeHostKeyFingerprint pins the accepted input forms: canonical
// SHA256:<base64>, the same fingerprint without the prefix, surrounding space
// and a case-insensitive prefix. Malformed values are rejected early.
func TestNormalizeHostKeyFingerprint(t *testing.T) {
	_, fp := knownHostKeyForTest(t)
	bare := strings.TrimPrefix(fp, "SHA256:")
	for _, in := range []string{fp, bare, "  " + fp + "  ", "sha256:" + bare} {
		got, err := NormalizeHostKeyFingerprint(in)
		if err != nil || got != fp {
			t.Fatalf("NormalizeHostKeyFingerprint(%q) = %q, %v; want %q", in, got, err, fp)
		}
	}
	for _, in := range []string{"", "SHA256:", "SHA256:not-base64!", "SSH256:" + bare, bare[:10]} {
		if _, err := NormalizeHostKeyFingerprint(in); err == nil {
			t.Fatalf("NormalizeHostKeyFingerprint accepted invalid value %q", in)
		}
	}
}
