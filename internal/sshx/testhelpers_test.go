package sshx

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// syncBuffer is a bytes.Buffer safe for concurrent Write/Len/String.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Len()
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// withRawHooks temporarily swaps the raw-mode hooks so terminal behaviour can
// be tested without a real TTY (the CI mandate in AGENTS.md #4).
func withRawHooks(t *testing.T, hooks rawHooks, fn func()) {
	t.Helper()
	old := currentRawHooks
	currentRawHooks = hooks
	defer func() { currentRawHooks = old }()
	fn()
}

// generateEd25519PrivateKeyFile writes an OpenSSH-format ed25519 private key to
// path. Used by tests that need a real signer against the fake sshd.
func generateEd25519PrivateKeyFile(path string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "test")
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
}

// parseWindowsCommandLine is a test-only inverse of WindowsProxyCommand,
// implementing the documented CommandLineToArgvW rules.
func parseWindowsCommandLine(cmd string) ([]string, error) {
	var args []string
	var buf strings.Builder
	i := 0
	inQuotes := false
	for i < len(cmd) {
		c := cmd[i]
		switch {
		case c == '\\':
			n := 0
			for i < len(cmd) && cmd[i] == '\\' {
				n++
				i++
			}
			if i < len(cmd) && cmd[i] == '"' {
				buf.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					buf.WriteByte('"')
					i++
				} else {
					inQuotes = !inQuotes
					i++
				}
			} else {
				buf.WriteString(strings.Repeat(`\`, n))
			}
		case c == '"':
			inQuotes = !inQuotes
			i++
		case (c == ' ' || c == '\t') && !inQuotes:
			args = append(args, buf.String())
			buf.Reset()
			i++
		default:
			buf.WriteByte(c)
			i++
		}
	}
	if inQuotes {
		return nil, fmt.Errorf("unterminated quote")
	}
	args = append(args, buf.String())
	return args, nil
}

func sshClientConfigForTest() *ssh.ClientConfig { return newSSHClientConfig(nil) }

func ed25519SignerForTest() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

func ed25519KeyForTest() (ed25519.PrivateKey, ssh.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	blob := ssh.Marshal(struct {
		Type string
		Key  string
	}{ssh.KeyAlgoED25519, string(pub)})
	sshPub, err := ssh.ParsePublicKey(blob)
	return priv, sshPub, err
}
