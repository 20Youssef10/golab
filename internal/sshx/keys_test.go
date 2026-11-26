package sshx

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Ports upstream tests/test_ssh.py::test_resolve_pubkey_with_identity_reads_pub_file_first
// and ::test_resolve_pubkey_with_pub_only_identity_needs_no_private_key.
func TestResolveKeyIdentityReadsPubFileFirst(t *testing.T) {
	for _, withSuffix := range []bool{true, false} {
		dir := t.TempDir()
		pubLine := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDfake user@host"
		pub := filepath.Join(dir, "id_test.pub")
		priv := filepath.Join(dir, "id_test")
		if err := os.WriteFile(pub, []byte(pubLine+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(priv, []byte("(fake private key)"), 0o600); err != nil {
			t.Fatal(err)
		}
		arg := priv
		if withSuffix {
			arg = pub
		}
		got, err := ResolveKey(arg)
		if err != nil {
			t.Fatalf("ResolveKey(%q): %v", arg, err)
		}
		if got.PublicLine != pubLine {
			t.Fatalf("pubkey = %q; want %q", got.PublicLine, pubLine)
		}
		if got.Source != "identity" {
			t.Fatalf("source = %q; want identity", got.Source)
		}
	}
}

// Ports upstream tests/test_ssh.py::test_resolve_pubkey_with_pub_only_identity_needs_no_private_key.
func TestResolveKeyStandalonePub(t *testing.T) {
	dir := t.TempDir()
	pubLine := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAGENT user@host"
	pub := filepath.Join(dir, "agent_only.pub")
	if err := os.WriteFile(pub, []byte(pubLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveKey(pub)
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if got.PublicLine != pubLine {
		t.Fatalf("pubkey = %q", got.PublicLine)
	}
	if len(got.Signers) != 0 {
		t.Fatalf("pub-only key must not fabricate a signer: %d", len(got.Signers))
	}
}

// Ports upstream tests/test_ssh.py::test_resolve_pubkey_missing_identity_exits.
func TestResolveKeyMissingIdentity(t *testing.T) {
	_, err := ResolveKey(filepath.Join(t.TempDir(), "no-such-key"))
	if err == nil {
		t.Fatal("want error for missing identity")
	}
	var ke *KeyError
	if !errors.As(err, &ke) {
		t.Fatalf("want *KeyError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("message = %q", err.Error())
	}
}

// Ports upstream tests/test_ssh.py::test_resolve_pubkey_default_scan_key_order
// and ::test_resolve_pubkey_default_scan_rsa_not_selected.
func TestResolveKeyDefaultScanOrder(t *testing.T) {
	cases := []struct {
		name    string
		present []string
		want    string
		wantErr bool
	}{
		{"ed25519-first", []string{"id_ed25519.pub", "id_ecdsa.pub"}, "ssh-ed25519 ED", false},
		{"ecdsa-only", []string{"id_ecdsa.pub"}, "ecdsa-sha2-nistp256 EC", false},
		{"rsa-not-selected", []string{"id_rsa.pub"}, "", true},
		{"none", nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			sshDir := filepath.Join(home, ".ssh")
			if err := os.MkdirAll(sshDir, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.present {
				if err := os.WriteFile(filepath.Join(sshDir, name), []byte("ssh-rsa RSA\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(sshDir, name), []byte(map[string]string{
					"id_ed25519.pub": "ssh-ed25519 ED\n",
					"id_ecdsa.pub":   "ecdsa-sha2-nistp256 EC\n",
					"id_rsa.pub":     "ssh-rsa RSA\n",
				}[name]), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("COLAB_TEST_AGENT", "0")
			got, err := ResolveKey("")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error; got %+v", got)
				}
				if !errors.Is(err, ErrNoSSHKey) {
					t.Fatalf("want ErrNoSSHKey, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveKey: %v", err)
			}
			if got.PublicLine != tc.want {
				t.Fatalf("pubkey = %q; want %q", got.PublicLine, tc.want)
			}
		})
	}
}

// Ports upstream tests/test_ssh.py (RSA rejection is server-side upstream; we
// reject locally with guidance per doc 05/ssh and ADR-005).
func TestPublicKeyTypeRejectsRSA(t *testing.T) {
	if _, err := PublicKeyType("ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ user@host"); !errors.Is(err, ErrUnsupportedKeyType) {
		t.Fatalf("RSA: want ErrUnsupportedKeyType, got %v", err)
	}
	for _, typ := range []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIuser@host",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTY user@host",
		"ecdsa-sha2-nistp384 AAAAE2VjZHNhLXNoYTItbmlzdHAzODQ user@host",
		"ecdsa-sha2-nistp521 AAAAE2VjZHNhLXNoYTItbmlzdHA1MjE user@host",
	} {
		got, err := PublicKeyType(typ)
		if err != nil {
			t.Errorf("PublicKeyType(%q): %v", typ, err)
			continue
		}
		if !strings.HasPrefix(typ, got) {
			t.Errorf("type %q does not match line %q", got, typ)
		}
	}
}

// Ports ADR-005's auth order: agent keys are preferred over ~/.ssh files.
func TestResolveKeyAgentBeforeFiles(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte("ssh-ed25519 FILEKEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	signer, err := ed25519SignerForTest()
	if err != nil {
		t.Fatal(err)
	}
	old := agentSignersHook
	agentSignersHook = func() []ssh.Signer { return []ssh.Signer{signer} }
	defer func() { agentSignersHook = old }()
	got, err := ResolveKey("")
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if got.Source != "agent" {
		t.Fatalf("source = %q; want agent", got.Source)
	}
	if strings.Contains(got.PublicLine, "FILEKEY") || !strings.HasPrefix(got.PublicLine, "ssh-ed25519 ") {
		t.Fatalf("public line = %q; want the agent key", got.PublicLine)
	}
}

// Local RSA rejection must cover PKCS#8 too, not just PKCS#1/openssh.
func TestDerivePublicLineRejectsPKCS8RSA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = DerivePublicLine(path)
	var ke *KeyError
	if !errors.As(err, &ke) || !errors.Is(err, ErrUnsupportedKeyType) {
		t.Fatalf("want *KeyError wrapping ErrUnsupportedKeyType, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "ssh-keygen -t ed25519") {
		t.Fatalf("missing remediation hint: %q", err.Error())
	}
}

// DerivePublicLine must derive a native public-key line from an OpenSSH-format
// private key without shelling out to ssh-keygen (ADR-005).
func TestDerivePublicLineFromGeneratedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id_ed25519")
	if err := generateEd25519PrivateKeyFile(path); err != nil {
		t.Fatal(err)
	}
	line, err := DerivePublicLine(path)
	if err != nil {
		t.Fatalf("DerivePublicLine: %v", err)
	}
	if !strings.HasPrefix(line, "ssh-ed25519 ") {
		t.Fatalf("derived line = %q", line)
	}
	// The derived line must be accepted verbatim by the public-key parser.
	if typ, err := PublicKeyType(line); err != nil || typ != "ssh-ed25519" {
		t.Fatalf("PublicKeyType(%q) = %q,%v", line, typ, err)
	}
}

// §4.13: a private key with group/other permission bits must emit a warning,
// but must remain usable — refusing it would break legitimate shared setups.
func TestResolveKeyWarnsOnLoosePrivateKeyPerms(t *testing.T) {
	t.Setenv("COLAB_TEST_AGENT", "0")
	dir := t.TempDir()

	loose := filepath.Join(dir, "id_ed25519")
	if err := generateEd25519PrivateKeyFile(loose); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	km, err := ResolveKeyWithWriter(loose, &warn)
	if err != nil {
		t.Fatalf("ResolveKeyWithWriter(loose): %v", err)
	}
	if len(km.Signers) == 0 {
		t.Fatal("loose-perm key must still be used for signing")
	}
	// Windows reports 0666 for regular files regardless of chmod; ACLs, not
	// mode bits, govern access there, so no warning is expected on any mode.
	if runtime.GOOS == "windows" {
		if warn.Len() != 0 {
			t.Fatalf("Windows must not warn on mode bits: %q", warn.String())
		}
	} else if !strings.Contains(warn.String(), "chmod 600") || !strings.Contains(warn.String(), loose) {
		t.Fatalf("want actionable permission warning naming %q, got %q", loose, warn.String())
	}

	strict := filepath.Join(dir, "id_strict")
	if err := generateEd25519PrivateKeyFile(strict); err != nil {
		t.Fatal(err)
	}
	warn.Reset()
	if _, err := ResolveKeyWithWriter(strict, &warn); err != nil {
		t.Fatalf("ResolveKeyWithWriter(strict): %v", err)
	}
	if warn.Len() != 0 {
		t.Fatalf("0600 key must not warn on Unix, and any mode must not warn on Windows: %q", warn.String())
	}
}

// Windows reports mode 0666 for regular files even after chmod 0600, so mode
// bits are not a usable permission signal there (ACLs govern access instead).
func TestLoosePermWarningOSGate(t *testing.T) {
	if loosePermWarningsEnabled("windows") {
		t.Error("Windows must not warn on mode bits")
	}
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		if !loosePermWarningsEnabled(goos) {
			t.Errorf("%s must keep the loose-permission warning", goos)
		}
	}
}

// §4.13: the same warning applies to the default ~/.ssh/id_ed25519 scan, not
// just to an explicit --identity path.
func TestResolveKeyDefaultScanWarnsOnLoosePrivateKeyPerms(t *testing.T) {
	t.Setenv("COLAB_TEST_AGENT", "0")
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	priv := filepath.Join(sshDir, "id_ed25519")
	if err := generateEd25519PrivateKeyFile(priv); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(priv, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var warn bytes.Buffer
	km, err := ResolveKeyWithWriter("", &warn)
	if err != nil {
		t.Fatalf("ResolveKeyWithWriter(default scan): %v", err)
	}
	if km.Source != "id_ed25519" {
		t.Fatalf("source = %q; want id_ed25519", km.Source)
	}
	if runtime.GOOS == "windows" {
		if warn.Len() != 0 {
			t.Fatalf("Windows must not warn on mode bits: %q", warn.String())
		}
	} else if !strings.Contains(warn.String(), "chmod 600") {
		t.Fatalf("want permission warning for default-scanned key, got %q", warn.String())
	}
}

// §4.11: COLAB_TEST_AGENT is a test-only escape hatch. A release-like binary
// (no "test.v" flag) must ignore it unless COLAB_TESTING=1 opts in explicitly.
func TestAgentTestEnvGateReleaseBinary(t *testing.T) {
	t.Setenv("COLAB_TEST_AGENT", "0")
	t.Setenv("COLAB_TESTING", "")

	if !suppressAgentForTest() {
		t.Fatal("test binary must honour COLAB_TEST_AGENT=0")
	}

	original := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet("release", flag.ContinueOnError)
	t.Cleanup(func() { flag.CommandLine = original })

	if suppressAgentForTest() {
		t.Error("release-like binary honoured COLAB_TEST_AGENT=0 without COLAB_TESTING=1")
	}
	t.Setenv("COLAB_TESTING", "1")
	if !suppressAgentForTest() {
		t.Error("COLAB_TESTING=1 must opt in to the test-only agent control")
	}
}

// §4.13: mustRead must return a typed error instead of silently swallowing an
// unreadable key. A directory passed where a private key is expected fails
// os.ReadFile on every platform, so the test does not depend on running as a
// non-root user.
func TestUnreadablePrivateKeyReturnsTypedError(t *testing.T) {
	t.Setenv("COLAB_TEST_AGENT", "0")
	dir := t.TempDir()
	pub := filepath.Join(dir, "id_test.pub")
	priv := filepath.Join(dir, "id_test")
	if err := os.WriteFile(pub, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAGENT user@host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(priv, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveKey(priv)
	var ke *KeyError
	if !errors.As(err, &ke) {
		t.Fatalf("want *KeyError for unreadable private key, got %T: %v", err, err)
	}
	if !strings.Contains(ke.Error(), "id_test") {
		t.Fatalf("typed error must name the key: %q", ke.Error())
	}
}
