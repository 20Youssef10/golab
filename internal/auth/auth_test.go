// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
//
// Tests in this file port the intent of upstream tests/test_auth.py to Go.
// No test contacts a real Google endpoint: every OAuth token URI points at a
// local httptest server on loopback.
package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"

	"github.com/zalando/go-keyring"
)

// secretSentinel is used to prove that neither the code, the verifier nor the
// token ever appears in stderr, errors or logs (upstream
// tests/test_local_credentials.py::test_credentials_are_redacted_from_log).
const secretSentinel = "sentinel-0123456789abcdef-DO-NOT-LEAK"

// memKeyring is an in-memory KeyringBackend so tests never touch the real OS
// keyring (or a D-Bus session bus).
type memKeyring struct {
	mu       sync.Mutex
	data     map[string]string
	getErr   error
	setErr   error
	delErr   error
	blockGet chan struct{}
	gets     int
	sets     int
	dels     int
}

func (m *memKeyring) Get(service, user string) (string, error) {
	m.mu.Lock()
	m.gets++
	block := m.blockGet
	m.mu.Unlock()
	if block != nil {
		<-block
	}
	if m.getErr != nil {
		return "", m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[service+"\x00"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (m *memKeyring) Set(service, user, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sets++
	if m.setErr != nil {
		return m.setErr
	}
	if m.data == nil {
		m.data = make(map[string]string)
	}
	m.data[service+"\x00"+user] = value
	return nil
}

func (m *memKeyring) Delete(service, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dels++
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.data, service+"\x00"+user)
	return nil
}

func (m *memKeyring) value() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[KeyringService+"\x00"+KeyringUser]
}

// memStore is an in-memory TokenStore used when a test wants to bypass tiers.
type memStore struct {
	mu      sync.Mutex
	data    []byte
	getErr  error
	saveErr error
}

func (s *memStore) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	return append([]byte(nil), s.data...), nil
}
func (s *memStore) Save(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.data = append([]byte(nil), b...)
	return nil
}
func (s *memStore) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = nil
	return nil
}

func fakeTokenServer(t *testing.T, fn func(url.Values) (int, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token endpoint: ParseForm: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		status, body := fn(r.Form)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func tokenJSON(access, refresh string, expiresIn int64, scopes []string) string {
	return fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":%d,"scope":%q,"token_type":"Bearer"}`,
		access, refresh, expiresIn, strings.Join(scopes, " "))
}

func writeClientConfig(t *testing.T, dir, tokenURI string) string {
	t.Helper()
	path := filepath.Join(dir, "client-oauth-config.json")
	data := fmt.Sprintf(`{"installed":{"client_id":"test-client","client_secret":"test-secret","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":%q}}`, tokenURI)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(t *testing.T, tokenURI, code string) (Config, *memKeyring, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	kr := &memKeyring{data: make(map[string]string)}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cfg := Config{
		Provider:              AuthOAuth2,
		ClientOAuthConfigPath: writeClientConfig(t, dir, tokenURI),
		ConfigDir:             dir,
		Stdin:                 strings.NewReader(code),
		Stdout:                stdout,
		Stderr:                stderr,
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		Keyring:               kr,
		ProbeTimeout:          50 * time.Millisecond,
		TokenTimeout:          2 * time.Second,
	}
	return cfg, kr, stdout, stderr
}

func mustProvider(t *testing.T, cfg Config) Provider {
	t.Helper()
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func mustClient(t *testing.T, p Provider) *http.Client {
	t.Helper()
	client, err := p.HTTPClient(context.Background(), nil)
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}
	return client
}

// ---------------------------------------------------------------------------
// Provider parsing / scopes
// ---------------------------------------------------------------------------

// Ports cli.py callback(auth=..., case_sensitive=False): --auth must be
// case-insensitive and default to oauth2.
func TestParseAuthProvider(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want AuthProvider
	}{
		{"oauth2", AuthOAuth2}, {"OAUTH2", AuthOAuth2}, {"OAuth2", AuthOAuth2},
		{"adc", AuthADC}, {"ADC", AuthADC}, {" AdC ", AuthADC},
	} {
		got, err := ParseAuthProvider(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseAuthProvider(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := ParseAuthProvider("magic"); err == nil {
		t.Fatal("ParseAuthProvider accepted an unknown provider")
	}
	var p AuthProvider
	if err := p.Set("ADC"); err != nil || p != AuthADC {
		t.Fatalf("Set(ADC) = %q, %v", p, err)
	}
	if DefaultAuthProvider != AuthOAuth2 {
		t.Fatalf("default provider = %q, want oauth2", DefaultAuthProvider)
	}
}

// Ports upstream PUBLIC_SCOPES and DEC-10 / doc 08 §5 (S5 minimal-scope
// experiment): the default set stays all six; the documented candidate drops
// cloud-platform, drive.file and userinfo.profile.
func TestScopesAndMinimalScopes(t *testing.T) {
	want := []string{
		"openid",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/colaboratory",
		"https://www.googleapis.com/auth/drive.file",
	}
	if strings.Join(Scopes, "|") != strings.Join(want, "|") {
		t.Fatalf("Scopes = %v; want %v", Scopes, want)
	}
	for _, forbidden := range []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/drive.file",
		"https://www.googleapis.com/auth/userinfo.profile",
	} {
		for _, s := range MinimalScopes {
			if s == forbidden {
				t.Fatalf("MinimalScopes unexpectedly contains %q", forbidden)
			}
		}
	}
	if !contains(MinimalScopes, CollaboratoryScope) {
		t.Fatal("MinimalScopes must retain the colaboratory scope")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// OAuth2 provider
// ---------------------------------------------------------------------------

// Ports upstream test_get_credentials_valid_token with AUDIT-02 S-4 applied:
// the token file holds only a refresh token, so a valid access token is minted
// from it on demand (one bounded exchange) instead of being read from disk.
func TestOAuth2ValidTokenFromKeyring(t *testing.T) {
	var tokenHits int
	srv := fakeTokenServer(t, func(url.Values) (int, string) {
		tokenHits++
		return 200, tokenJSON("minted-access", "", 3600, Scopes)
	})
	defer srv.Close()

	cfg, kr, stdout, stderr := testConfig(t, srv.URL, "") // empty stdin: any read would block/fail
	saved := storedToken{
		RefreshToken: logx.NewSecret("valid-refresh"),
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       append([]string(nil), Scopes...),
	}
	b, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "access_token") {
		t.Fatalf("stored JSON must not contain an access token: %s", b)
	}
	if err := kr.Set(KeyringService, KeyringUser, string(b)); err != nil {
		t.Fatal(err)
	}

	var gotAuth string
	res := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer res.Close()

	client := mustClient(t, mustProvider(t, cfg))
	resp, err := client.Get(res.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotAuth != "Bearer minted-access" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if tokenHits != 1 {
		t.Fatalf("token endpoint hit %d times; want one on-demand mint", tokenHits)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected chatter: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// Ports upstream test_get_credentials_expired_token_refresh and
// test_partial_grant_token_refreshes_with_granted_scopes: an expired token is
// refreshed, and the refresh body uses the *granted* scopes.
func TestOAuth2ExpiredTokenRefreshesWithGrantedScopes(t *testing.T) {
	granted := []string{"openid", CollaboratoryScope}
	var form url.Values
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		form = f
		return 200, tokenJSON("refreshed-access", "", 3600, granted)
	})
	defer srv.Close()

	cfg, kr, stdout, stderr := testConfig(t, srv.URL, "")
	saved := storedToken{
		AccessToken:  logx.NewSecret("expired-access"),
		RefreshToken: logx.NewSecret("the-refresh-token"),
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Hour),
		Scopes:       granted,
	}
	b, _ := json.Marshal(saved)
	if err := kr.Set(KeyringService, KeyringUser, string(b)); err != nil {
		t.Fatal(err)
	}

	client := mustClient(t, mustProvider(t, cfg))
	if client == nil {
		t.Fatal("nil client")
	}
	if form == nil {
		t.Fatal("refresh endpoint was not called")
	}
	if got := form.Get("grant_type"); got != "refresh_token" {
		t.Errorf("grant_type = %q", got)
	}
	if got := form.Get("refresh_token"); got != "the-refresh-token" {
		t.Errorf("refresh_token was not forwarded")
	}
	if got := form.Get("scope"); got != strings.Join(granted, " ") {
		t.Errorf("refresh scope = %q; want %q", got, strings.Join(granted, " "))
	}
	raw := kr.value()
	var out storedToken
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decoding persisted token: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["access_token"]; ok {
		t.Errorf("S-4: refreshed access token must not be persisted: %s", raw)
	}
	if _, ok := fields["client_secret"]; ok {
		t.Errorf("S-4: client secret must not be persisted: %s", raw)
	}
	if out.RefreshToken.Reveal() != "the-refresh-token" {
		t.Errorf("refresh token lost on refresh")
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected chatter: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// Ports upstream test_get_credentials_no_token: with no cached credentials the
// remote copy-paste flow runs, with the registered HTTPS redirect, consent
// prompt, token_usage=remote, PKCE S256 and access_type=offline.
func TestOAuth2NoTokenRunsRemoteCopyPasteFlow(t *testing.T) {
	var exchangeForm url.Values
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		exchangeForm = f
		return 200, tokenJSON("new-access", "new-refresh", 3600, Scopes)
	})
	defer srv.Close()

	cfg, kr, stdout, stderr := testConfig(t, srv.URL, "  pasted-code  \n")
	client := mustClient(t, mustProvider(t, cfg))
	if client == nil {
		t.Fatal("nil client")
	}
	if exchangeForm == nil {
		t.Fatal("token endpoint was not called")
	}
	if got := exchangeForm.Get("code"); got != "pasted-code" {
		t.Fatalf("code = %q", got)
	}
	verifier := exchangeForm.Get("code_verifier")
	if verifier == "" {
		t.Fatal("missing PKCE code_verifier")
	}
	if got := exchangeForm.Get("grant_type"); got != "authorization_code" {
		t.Errorf("grant_type = %q", got)
	}
	if got := exchangeForm.Get("redirect_uri"); got != RemoteRedirectURI {
		t.Errorf("redirect_uri = %q", got)
	}

	authURL := extractAuthURL(t, stderr.String())
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("redirect_uri") != RemoteRedirectURI {
		t.Errorf("auth redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("prompt") != "consent" || q.Get("token_usage") != "remote" {
		t.Errorf("prompt/token_usage = %q/%q", q.Get("prompt"), q.Get("token_usage"))
	}
	if q.Get("access_type") != "offline" {
		t.Errorf("access_type = %q", q.Get("access_type"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", q.Get("code_challenge_method"))
	}
	if got := q.Get("code_challenge"); got != pkceChallengeForTest(verifier) {
		t.Errorf("code_challenge does not match the verifier")
	}
	if strings.Join(strings.Fields(q.Get("scope")), " ") != strings.Join(Scopes, " ") {
		t.Errorf("auth scope = %q", q.Get("scope"))
	}
	if !strings.Contains(stdout.String(), "Enter the authorization code:") {
		t.Errorf("missing stdin prompt on stdout: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "pasted-code") || strings.Contains(stderr.String(), verifier) {
		t.Fatal("stderr leaked the code or verifier")
	}
	var saved storedToken
	if err := json.Unmarshal([]byte(kr.value()), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.RefreshToken.Reveal() != "new-refresh" {
		t.Fatal("exchange refresh token not persisted")
	}
	if strings.Contains(kr.value(), "new-access") || strings.Contains(kr.value(), "test-secret") {
		t.Fatalf("S-4: access token or client secret persisted: %s", kr.value())
	}
}

// pkceChallengeForTest recomputes the S256 challenge without calling the
// implementation under test's URL builder.
func pkceChallengeForTest(verifier string) string {
	sha := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sha[:])
}

func extractAuthURL(t *testing.T, stderr string) string {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "https://") && strings.Contains(line, "o/oauth2/auth") {
			return line
		}
	}
	t.Fatalf("no authorization URL on stderr: %q", stderr)
	return ""
}

// Ports upstream test_get_credentials_refresh_failure_falls_back_to_remote_flow.
func TestOAuth2RefreshFailureFallsBackToRemoteFlow(t *testing.T) {
	var forms []url.Values
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		forms = append(forms, f)
		if f.Get("grant_type") == "refresh_token" {
			return 400, `{"error":"invalid_grant","description":"` + secretSentinel + `"}`
		}
		return 200, tokenJSON("replacement-access", "replacement-refresh", 3600, Scopes)
	})
	defer srv.Close()

	cfg, kr, _, stderr := testConfig(t, srv.URL, "fresh-code\n")
	saved := storedToken{
		AccessToken:  logx.NewSecret("old-access"),
		RefreshToken: logx.NewSecret("bad-refresh"),
		Expiry:       time.Now().Add(-time.Hour),
		Scopes:       Scopes,
	}
	b, _ := json.Marshal(saved)
	_ = kr.Set(KeyringService, KeyringUser, string(b))

	mustClient(t, mustProvider(t, cfg))
	if len(forms) != 2 || forms[0].Get("grant_type") != "refresh_token" || forms[1].Get("code") != "fresh-code" {
		t.Fatalf("expected refresh then exchange; got %v", forms)
	}
	if strings.Contains(stderr.String(), secretSentinel) {
		t.Fatal("stderr leaked a token-endpoint body")
	}
	var out storedToken
	_ = json.Unmarshal([]byte(kr.value()), &out)
	if out.RefreshToken.Reveal() != "replacement-refresh" {
		t.Fatal("replacement refresh token not persisted")
	}
}

// Ports upstream test_get_credentials_corrupt_keyring_data: corrupt stored JSON
// is ignored and overwritten by a fresh remote flow.
func TestOAuth2CorruptStoredTokenFallsBackToRemoteFlow(t *testing.T) {
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		return 200, tokenJSON("fresh-access", "fresh-refresh", 3600, Scopes)
	})
	defer srv.Close()
	cfg, kr, _, _ := testConfig(t, srv.URL, "code\n")
	_ = kr.Set(KeyringService, KeyringUser, "not-valid-json")

	mustClient(t, mustProvider(t, cfg))
	var out storedToken
	if err := json.Unmarshal([]byte(kr.value()), &out); err != nil || out.RefreshToken.Reveal() != "fresh-refresh" {
		t.Fatalf("corrupt token was not replaced: %q, %v", kr.value(), err)
	}
}

// Ports upstream test_remote_flow_partial_grant_persists_only_granted_scopes:
// a partial consent must be persisted (not the originally requested scopes).
func TestOAuth2PartialGrantPersistsOnlyGrantedScopes(t *testing.T) {
	granted := []string{"openid", "https://www.googleapis.com/auth/userinfo.email", CollaboratoryScope}
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		return 200, tokenJSON("partial-access", "partial-refresh", 3600, granted)
	})
	defer srv.Close()
	cfg, kr, _, _ := testConfig(t, srv.URL, "code\n")

	mustClient(t, mustProvider(t, cfg))
	var out storedToken
	_ = json.Unmarshal([]byte(kr.value()), &out)
	if strings.Join(out.Scopes, "|") != strings.Join(granted, "|") {
		t.Fatalf("persisted scopes = %v; want %v", out.Scopes, granted)
	}
}

// Ports upstream test_get_credentials_keyring_inaccessible and
// test_keyring_read_failure_warns_on_stderr: keyring failures fall back to the
// 0600 file tier and emit a single, secret-free stderr notice.
func TestOAuth2KeyringInaccessibleFallsBackToFile(t *testing.T) {
	// S-4: the file holds a refresh token only, so minting the access token
	// legitimately contacts this endpoint once.
	var refreshes int
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		refreshes++
		return 200, tokenJSON("minted-from-file", "", 3600, Scopes)
	})
	defer srv.Close()
	cfg, kr, _, stderr := testConfig(t, srv.URL, "")
	kr.getErr = fmt.Errorf("No recommended backend was available")
	kr.setErr = fmt.Errorf("No recommended backend was available")

	saved := storedToken{
		RefreshToken: logx.NewSecret("file-refresh"),
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       Scopes,
	}
	b, _ := json.Marshal(saved)
	path := filepath.Join(cfg.ConfigDir, OAuthTokenFileName)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	mustClient(t, mustProvider(t, cfg))
	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "keyring") || !strings.Contains(msg, "--auth=adc") {
		t.Fatalf("missing keyring fallback notice: %q", msg)
	}
	if strings.Count(msg, "[colab]") > 1 {
		t.Fatalf("expected a single notice, got %q", msg)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// chmod 0600 is a no-op on Windows; profile ACLs provide the restriction
	// there, and os.Stat reports 0666 for a newly created file.
	if goruntime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %o, want 600", info.Mode().Perm())
	}
}

// Ports upstream test_legacy_token_file_migrated_to_keyring and
// test_legacy_token_file_deleted_when_keyring_has_token: Python's token.json is
// imported once, written to the new store and removed.
func TestLegacyTokenFileMigrated(t *testing.T) {
	srv := fakeTokenServer(t, func(url.Values) (int, string) {
		t.Fatal("token endpoint must not be contacted during legacy import")
		return 500, ""
	})
	defer srv.Close()
	cfg, kr, _, _ := testConfig(t, srv.URL, "")
	dir := t.TempDir()
	cfg.ConfigDir = dir // empty keyring + file store in a fresh dir
	legacy := filepath.Join(dir, LegacyTokenFileName)
	legacyJSON := `{"token":"legacy-access","refresh_token":"legacy-refresh","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z","scopes":["openid","https://www.googleapis.com/auth/colaboratory"],"client_id":"legacy-client","client_secret":"legacy-secret","token_uri":"https://oauth2.googleapis.com/token"}`
	if err := os.WriteFile(legacy, []byte(legacyJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	mustClient(t, mustProvider(t, cfg))
	if kr.value() == "" {
		t.Fatal("legacy token was not imported into the keyring")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy token file was not removed: %v", err)
	}
	var out storedToken
	_ = json.Unmarshal([]byte(kr.value()), &out)
	if out.RefreshToken.Reveal() != "legacy-refresh" {
		t.Fatalf("legacy refresh token not preserved: %+v", out)
	}
	if strings.Contains(kr.value(), "legacy-access") || strings.Contains(kr.value(), "legacy-secret") {
		t.Fatalf("S-4: one-time legacy import persisted a short-lived secret: %s", kr.value())
	}
}

// Ports upstream test_legacy_token_file_kept_when_keyring_save_fails: never
// delete the only copy of a usable token unless it was persisted successfully.
// Here *both* tiers fail, unlike upstream (which has only the keyring tier).
func TestLegacyTokenFileKeptWhenSaveFails(t *testing.T) {
	srv := fakeTokenServer(t, func(url.Values) (int, string) {
		return 200, tokenJSON("fresh-access", "fresh-refresh", 3600, Scopes)
	})
	defer srv.Close()
	cfg, _, _, _ := testConfig(t, srv.URL, "fresh-code\n")
	cfg.TokenStore = &memStore{saveErr: fmt.Errorf("both tiers failed")}
	dir := t.TempDir()
	cfg.ConfigDir = dir
	legacy := filepath.Join(dir, LegacyTokenFileName)
	legacyJSON := `{"token":"legacy-access","refresh_token":"legacy-refresh","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z","scopes":["openid","https://www.googleapis.com/auth/colaboratory"]}`
	if err := os.WriteFile(legacy, []byte(legacyJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	mustClient(t, mustProvider(t, cfg))
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy token was deleted although saving failed: %v", err)
	}
}

// A keyring failure alone is not fatal: our tiered store persists the imported
// token in the 0600 fallback file, so the legacy copy can be removed.
func TestLegacyTokenMovedToFileWhenKeyringSaveFails(t *testing.T) {
	srv := fakeTokenServer(t, func(url.Values) (int, string) {
		t.Fatal("token endpoint must not be contacted")
		return 500, ""
	})
	defer srv.Close()
	cfg, kr, _, _ := testConfig(t, srv.URL, "")
	kr.setErr = fmt.Errorf("locked")
	dir := t.TempDir()
	cfg.ConfigDir = dir
	legacy := filepath.Join(dir, LegacyTokenFileName)
	legacyJSON := `{"token":"legacy-access","refresh_token":"legacy-refresh","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z","scopes":["openid","https://www.googleapis.com/auth/colaboratory"]}`
	if err := os.WriteFile(legacy, []byte(legacyJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	mustClient(t, mustProvider(t, cfg))
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy token kept although it was persisted to the fallback file: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, OAuthTokenFileName))
	if err != nil || !bytes.Contains(b, []byte("legacy-refresh")) {
		t.Fatalf("fallback file did not receive the token: %q, %v", b, err)
	}
}

// Ports upstream test_get_credentials_no_config / fallback_config: when -c is
// absent, the bundled cloud-SDK client config is used.
func TestLoadClientConfigFallback(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{ConfigDir: dir, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
	p := mustProvider(t, cfg).(*oauth2Provider)
	cc, err := p.loadClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cc.ClientID != "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com" {
		t.Fatalf("bundled client id = %q", cc.ClientID)
	}
	if cc.TokenURL != "https://oauth2.googleapis.com/token" {
		t.Fatalf("bundled token URI = %q", cc.TokenURL)
	}
}

// Ports test_local_credentials.py redaction intent: a hostile token endpoint
// body must never reach an error message, stderr or the logger.
func TestNoSecretsInErrorsOrLogs(t *testing.T) {
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		return 400, `{"error":"invalid_grant","code":"` + secretSentinel + `","client_secret":"` + secretSentinel + `"}`
	})
	defer srv.Close()
	cfg, _, stdout, stderr := testConfig(t, srv.URL, "my-secret-code\n")
	logs := &bytes.Buffer{}
	cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))

	p := mustProvider(t, cfg)
	_, err := p.HTTPClient(context.Background(), nil)
	if err == nil {
		t.Fatal("expected exchange error")
	}
	for name, text := range map[string]string{
		"error": err.Error(), "stderr": stderr.String(), "stdout": stdout.String(), "log": logs.String(),
	} {
		for _, needle := range []string{secretSentinel, "my-secret-code"} {
			if strings.Contains(text, needle) {
				t.Errorf("%s leaked %q: %s", name, needle, text)
			}
		}
	}
}

// Explicit timeouts: a wedged token endpoint must be abandoned, not waited on.
func TestTokenEndpointHardTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	cfg, _, _, _ := testConfig(t, srv.URL, "code\n")
	cfg.TokenTimeout = 30 * time.Millisecond
	start := time.Now()
	_, err := mustProvider(t, cfg).HTTPClient(context.Background(), nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("token request took %v; hard timeout did not fire", elapsed)
	}
	if strings.Contains(err.Error(), secretSentinel) {
		t.Fatal("error leaked a secret")
	}
}

// ---------------------------------------------------------------------------
// New() validation
// ---------------------------------------------------------------------------

func TestNewRejectsUnknownProvider(t *testing.T) {
	_, err := New(Config{Provider: AuthProvider("magic")})
	if err == nil {
		t.Fatal("New accepted an unknown provider")
	}
	if !isUnknownProvider(err) {
		t.Fatalf("wrong error type: %v", err)
	}
}

func isUnknownProvider(err error) bool {
	return errors.Is(err, ErrUnknownProvider)
}
