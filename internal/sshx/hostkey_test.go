package sshx

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
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
	if fi.Mode().Perm()&0o077 != 0 {
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
