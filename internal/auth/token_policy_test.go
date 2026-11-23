package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

// TestSavedTokenOmitsAccessToken is audit P1-1 (S-4): the persisted JSON must
// contain the refresh token and metadata only — never the short-lived access
// token or the OAuth client secret. Access tokens are minted from the refresh
// token on demand (one HTTP exchange per process when none is cached).
func TestSavedTokenOmitsAccessToken(t *testing.T) {
	srv := fakeTokenServer(t, func(_ url.Values) (int, string) {
		return 200, tokenJSON("minted-access", "new-refresh", 3600, Scopes)
	})
	defer srv.Close()

	cfg, kr, _, _ := testConfig(t, srv.URL, "pasted-code\n")
	client := mustClient(t, mustProvider(t, cfg))
	if client == nil {
		t.Fatal("nil client")
	}
	raw := kr.value()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("decode saved token: %v", err)
	}
	for _, forbidden := range []string{"access_token", "client_secret", "token", "code", "code_verifier"} {
		if _, ok := fields[forbidden]; ok {
			t.Errorf("persisted JSON contains forbidden field %q: %s", forbidden, raw)
		}
	}
	for _, required := range []string{"refresh_token", "scopes"} {
		if _, ok := fields[required]; !ok {
			t.Errorf("persisted JSON missing %q: %s", required, raw)
		}
	}
	if strings.Contains(raw, "minted-access") || strings.Contains(raw, "test-secret") {
		t.Errorf("persisted JSON leaked a credential: %s", raw)
	}
}

// TestDecodeStoredLegacyTokenIsInMemoryOnly proves the one-time import of
// Python's token.json still works, but the legacy access token is never part
// of the newly persisted policy blob.
func TestDecodeStoredLegacyTokenIsInMemoryOnly(t *testing.T) {
	legacy := `{"token":"legacy-access","access_token":"legacy-access","refresh_token":"legacy-refresh","client_secret":"legacy-secret","token_uri":"https://oauth2.googleapis.com/token"}`
	s, err := decodeStored([]byte(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if s.AccessToken.Reveal() != "legacy-access" || s.RefreshToken.Reveal() != "legacy-refresh" {
		t.Fatalf("legacy credentials not decoded in memory: %+v", s)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"access_token", "token", "client_secret"} {
		if _, ok := fields[forbidden]; ok {
			t.Errorf("re-encoded legacy token contains %q: %s", forbidden, b)
		}
	}
}

// TestAccessTokenMintedFromRefreshToken covers the online-refresh half of the
// policy: a token file with no access_token is not an error — we mint one.
func TestAccessTokenMintedFromRefreshToken(t *testing.T) {
	var grants []string
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		grants = append(grants, f.Get("grant_type"))
		return 200, tokenJSON("fresh-access", "", 3600, Scopes)
	})
	defer srv.Close()

	cfg, kr, _, _ := testConfig(t, srv.URL, "")
	stored := storedToken{
		RefreshToken: logx.NewSecret("only-refresh"),
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       append([]string(nil), Scopes...),
	}
	b, _ := json.Marshal(stored)
	if err := kr.Set(KeyringService, KeyringUser, string(b)); err != nil {
		t.Fatal(err)
	}

	var authHeader string
	res := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
	}))
	defer res.Close()

	client := mustClient(t, mustProvider(t, cfg))
	resp, err := client.Get(res.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if authHeader != "Bearer fresh-access" {
		t.Fatalf("Authorization = %q, want the minted token", authHeader)
	}
	if len(grants) != 1 || grants[0] != "refresh_token" {
		t.Fatalf("token requests = %v, want one refresh", grants)
	}
}
