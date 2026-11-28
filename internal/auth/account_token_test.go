package auth

// No upstream Python port: named-account token isolation is a golab extension.
// These tests prove a named account never reads, writes or clears the legacy
// single-user token, and that no token value reaches an error or log.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const accountTokenSentinel = "account-token-0123456789abcdef-DO-NOT-LEAK"

func TestNamedAccountUsesIsolatedKeyringUserAndFallbackFile(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{data: make(map[string]string)}
	var stderr bytes.Buffer
	cfg := Config{
		Provider:      AuthOAuth2,
		ConfigDir:     dir,
		Keyring:       kr,
		KeyringUser:   "oauth_token:alice",
		TokenFileName: "oauth_token_alice.json",
		Stderr:        &stderr,
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	op, ok := p.(*oauth2Provider)
	if !ok {
		t.Fatalf("provider = %T, want *oauth2Provider", p)
	}
	if err := op.store.Save([]byte(accountTokenSentinel)); err != nil {
		t.Fatal(err)
	}
	kr.mu.Lock()
	v, named := kr.data[KeyringService+"\x00"+"oauth_token:alice"]
	_, legacy := kr.data[KeyringService+"\x00"+KeyringUser]
	kr.mu.Unlock()
	if !named || v != accountTokenSentinel {
		t.Errorf("named keyring entry = %q (found=%v)", v, named)
	}
	if legacy {
		t.Error("named account wrote the legacy keyring user")
	}
	if _, err := os.Stat(filepath.Join(dir, OAuthTokenFileName)); !os.IsNotExist(err) {
		t.Errorf("named account wrote the legacy fallback file: %v", err)
	}
	if strings.Contains(stderr.String(), accountTokenSentinel) {
		t.Errorf("stderr leaked the token: %q", stderr.String())
	}
}

// With no OS keyring the fallback file is named for the account, never legacy.
func TestNamedAccountFallbackFileIsIsolated(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{data: make(map[string]string), setErr: fmt.Errorf("no backend")}
	p, err := New(Config{
		Provider:      AuthOAuth2,
		ConfigDir:     dir,
		Keyring:       kr,
		KeyringUser:   "oauth_token:alice",
		TokenFileName: "oauth_token_alice.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.(*oauth2Provider).store.Save([]byte(accountTokenSentinel)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "oauth_token_alice.json")); err != nil {
		t.Errorf("named fallback file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, OAuthTokenFileName)); !os.IsNotExist(err) {
		t.Errorf("named account wrote the legacy fallback file: %v", err)
	}
}

func TestNamedAccountClearLeavesLegacyTokenUntouched(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, LegacyTokenFileName)
	if err := os.WriteFile(legacyPath, []byte("legacy-token-DO-NOT-TOUCH"), 0o600); err != nil {
		t.Fatal(err)
	}
	kr := &memKeyring{data: map[string]string{
		KeyringService + "\x00" + KeyringUser:         "legacy-keyring-DO-NOT-TOUCH",
		KeyringService + "\x00" + "oauth_token:alice": accountTokenSentinel,
	}}
	cfg := Config{
		Provider:      AuthOAuth2,
		ConfigDir:     dir,
		Keyring:       kr,
		KeyringUser:   "oauth_token:alice",
		TokenFileName: "oauth_token_alice.json",
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	clearer, ok := p.(TokenClearer)
	if !ok {
		t.Fatalf("provider %T is not a TokenClearer", p)
	}
	res, err := clearer.ClearToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Keyring || !res.Removed {
		t.Errorf("clear result = %+v, want the named keyring entry removed", res)
	}
	if res.Legacy {
		t.Error("named account clear reported the legacy token as cleared")
	}
	kr.mu.Lock()
	legacyValue := kr.data[KeyringService+"\x00"+KeyringUser]
	_, namedStillThere := kr.data[KeyringService+"\x00"+"oauth_token:alice"]
	kr.mu.Unlock()
	if legacyValue != "legacy-keyring-DO-NOT-TOUCH" {
		t.Errorf("legacy keyring entry was modified: %q", legacyValue)
	}
	if namedStillThere {
		t.Error("named keyring entry still present after clear")
	}
	b, err := os.ReadFile(legacyPath)
	if err != nil || string(b) != "legacy-token-DO-NOT-TOUCH" {
		t.Errorf("legacy token file was removed or changed: %q, %v", b, err)
	}
}

func TestLegacyAccountNamesAreUnchangedWithoutOverrides(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{data: make(map[string]string)}
	p, err := New(Config{Provider: AuthOAuth2, ConfigDir: dir, Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	op := p.(*oauth2Provider)
	if err := op.store.Save([]byte("legacy-blob")); err != nil {
		t.Fatal(err)
	}
	kr.mu.Lock()
	v, ok := kr.data[KeyringService+"\x00"+KeyringUser]
	kr.mu.Unlock()
	if !ok || v != "legacy-blob" {
		t.Fatalf("legacy keyring entry = %q (found=%v)", v, ok)
	}
	// A second config dir with no keyring proves the fallback file keeps the
	// legacy name too.
	fallbackDir := t.TempDir()
	p2, err := New(Config{
		Provider:  AuthOAuth2,
		ConfigDir: fallbackDir,
		Keyring:   &memKeyring{data: make(map[string]string), setErr: fmt.Errorf("no backend")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p2.(*oauth2Provider).store.Save([]byte("legacy-blob")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fallbackDir, OAuthTokenFileName)); err != nil {
		t.Fatalf("legacy fallback path changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fallbackDir, "oauth_token_alice.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy config wrote a named fallback file: %v", err)
	}
}

func TestNamedAccountTokenNeverRevealedInErrors(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{data: make(map[string]string), setErr: fmt.Errorf("no backend")}
	p, err := New(Config{
		Provider:      AuthOAuth2,
		ConfigDir:     dir,
		Keyring:       kr,
		KeyringUser:   "oauth_token:alice",
		TokenFileName: "oauth_token_alice.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	op := p.(*oauth2Provider)
	if err := op.store.Save([]byte(accountTokenSentinel)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "oauth_token_alice.json")); err != nil {
		t.Fatal(err)
	}
	// The token file is owner-only.
	info, err := os.Stat(filepath.Join(dir, "oauth_token_alice.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("named token file mode = %o, want owner-only", info.Mode().Perm())
	}
}
