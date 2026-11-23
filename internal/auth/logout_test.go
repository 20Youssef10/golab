// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
//
// Tests port upstream tests/test_auth.py (PR #266):
//
//	test_clear_oauth_token_deletes_keyring_entry
//	test_clear_oauth_token_noop_when_empty
//	test_clear_oauth_token_removes_both_legacy_and_keyring
//	test_clear_oauth_token_legacy_error_still_deletes_keyring
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestClearTokenRemovesKeyringAndLegacyFile ports upstream
// test_clear_oauth_token_removes_both_legacy_and_keyring: both credential
// stores are cleared even if one of them holds nothing.
func TestClearTokenRemovesKeyringAndLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyTokenFileName)
	if err := os.WriteFile(legacy, []byte(`{"token":"legacy-access"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	kr := &memKeyring{data: make(map[string]string)}
	if err := kr.Set(KeyringService, KeyringUser, `{"refresh_token":"refresh"}`); err != nil {
		t.Fatal(err)
	}
	base, _, _, _ := testConfig(t, "http://127.0.0.1:1", "") // token URI never contacted
	cfg := base
	cfg.ConfigDir = dir
	cfg.Keyring = kr

	res, err := ClearToken(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ClearToken: %v", err)
	}
	if !res.Removed || !res.Keyring || !res.Legacy {
		t.Fatalf("result = %+v, want keyring+legacy removed", res)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy token file still present: %v", err)
	}
	if kr.value() != "" {
		t.Fatal("keyring token still present")
	}
}

// TestClearTokenIdempotent ports upstream test_clear_oauth_token_noop_when_empty:
// a second call reports nothing removed and does not fail.
func TestClearTokenIdempotent(t *testing.T) {
	dir := t.TempDir()
	kr := &memKeyring{data: make(map[string]string)}
	cfg := Config{Provider: AuthOAuth2, ConfigDir: dir, Keyring: kr,
		Stdout: io.Discard, Stderr: io.Discard}
	if _, err := ClearToken(context.Background(), cfg); err != nil {
		t.Fatalf("first ClearToken: %v", err)
	}
	res, err := ClearToken(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second ClearToken: %v", err)
	}
	if res.Removed || res.Keyring || res.Legacy || res.File {
		t.Fatalf("second call should be a no-op, got %+v", res)
	}
}

// TestClearTokenRemovesFallbackFile covers the 0600 file tier as well as the
// keyring (upstream only has the keyring tier).
func TestClearTokenRemovesFallbackFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, OAuthTokenFileName)
	if err := os.WriteFile(path, []byte(`{"refresh_token":"r"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	kr := &memKeyring{data: make(map[string]string)}
	kr.getErr = keyring.ErrNotFound // backend answered but is empty
	cfg := Config{Provider: AuthOAuth2, ConfigDir: dir, Keyring: kr,
		Stdout: io.Discard, Stderr: io.Discard}
	res, err := ClearToken(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ClearToken: %v", err)
	}
	if !res.Removed || !res.File || res.Keyring {
		t.Fatalf("result = %+v, want file tier removed only", res)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fallback token file still present: %v", err)
	}
}

// TestClearTokenLegacyErrorStillDeletesKeyring ports upstream
// test_clear_oauth_token_legacy_error_still_deletes_keyring: both stores are
// attempted before any error is reported.
func TestClearTokenLegacyErrorStillDeletesKeyring(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyTokenFileName)
	if err := os.WriteFile(legacy, []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make directory read-only: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	kr := &memKeyring{data: make(map[string]string)}
	_ = kr.Set(KeyringService, KeyringUser, `{"refresh_token":"r"}`)
	cfg := Config{Provider: AuthOAuth2, ConfigDir: dir, Keyring: kr,
		Stdout: io.Discard, Stderr: io.Discard}

	_, _ = ClearToken(context.Background(), cfg)
	if kr.value() != "" {
		t.Fatal("keyring was not cleared after the legacy-file error")
	}
}

// TestClearTokenNeverPrintsToken is a security regression test: the token is
// never formatted into stdout, stderr, the result or an error.
func TestClearTokenNeverPrintsToken(t *testing.T) {
	const canary = "clear-token-canary-DO-NOT-LEAK"
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyTokenFileName)
	if err := os.WriteFile(legacy, []byte(`{"token":"`+canary+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	kr := &memKeyring{data: make(map[string]string)}
	_ = kr.Set(KeyringService, KeyringUser, `{"refresh_token":"`+canary+`"}`)
	var stdout, stderr strings.Builder
	cfg := Config{Provider: AuthOAuth2, ConfigDir: dir, Keyring: kr,
		Stdout: &stdout, Stderr: &stderr}

	res, err := ClearToken(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	for name, text := range map[string]string{
		"stdout": stdout.String(), "stderr": stderr.String(),
		"result": string(b), "error": errString(err),
	} {
		if strings.Contains(text, canary) {
			t.Errorf("%s leaked the token: %q", name, text)
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestPackageClearTokenADCOnly is the provider-level counterpart of the CLI's
// --auth=adc guidance: ADC has no local OAuth token to clear.
func TestPackageClearTokenADCOnly(t *testing.T) {
	res, err := ClearToken(context.Background(), Config{Provider: AuthADC})
	if err != nil || res.Removed {
		t.Fatalf("ADC ClearToken = %+v, %v", res, err)
	}
}

// TestOAuth2ProviderImplementsTokenClearer pins the exported interface used by
// the CLI (which only holds an auth.Provider).
func TestOAuth2ProviderImplementsTokenClearer(t *testing.T) {
	cfg := Config{Provider: AuthOAuth2, ConfigDir: t.TempDir(),
		Stdout: io.Discard, Stderr: io.Discard}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(TokenClearer); !ok {
		t.Fatal("oauth2Provider does not implement auth.TokenClearer")
	}
	if _, ok := p.(interface {
		ClearToken(context.Context) (ClearTokenResult, error)
	}); !ok {
		t.Fatal("unexpected ClearToken signature")
	}
}
