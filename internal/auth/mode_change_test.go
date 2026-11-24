// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package auth

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

// TestModeChangeRequiresReAuth covers issue #71: a cached broad token must not
// be silently reused (or downgraded) when the caller asks for the reduced
// scope set. Switching scopes has to re-run the remote consent flow so the
// persisted grant matches the newly requested mode. Write the test against the
// existing API first: it compiles today and fails because the broad token is
// reused without any exchange.
func TestModeChangeRequiresReAuth(t *testing.T) {
	var exchanges, refreshes int
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		switch f.Get("grant_type") {
		case "refresh_token":
			refreshes++
			return 200, tokenJSON("refreshed-access", "", 3600, Scopes)
		default: // authorization_code
			exchanges++
			return 200, tokenJSON("reconsent-access", "reconsent-refresh", 3600, MinimalScopes)
		}
	})
	defer srv.Close()

	cfg, kr, _, stderr := testConfig(t, srv.URL, "reconsent-code\n")
	broad := storedToken{
		RefreshToken: logx.NewSecret("broad-refresh"),
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour), // valid: the old code no-ops on it
		Scopes:       append([]string(nil), Scopes...),
	}
	b, err := json.Marshal(broad)
	if err != nil {
		t.Fatal(err)
	}
	if err := kr.Set(KeyringService, KeyringUser, string(b)); err != nil {
		t.Fatal(err)
	}

	if _, err := mustProvider(t, cfg).HTTPClient(context.Background(), MinimalScopes); err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}
	if refreshes != 0 {
		t.Fatalf("refreshes = %d; want 0 (a mode change must not reuse the old grant)", refreshes)
	}
	if exchanges != 1 {
		t.Fatalf("auth-code exchanges = %d; want 1 (re-consent required on mode change)", exchanges)
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "scope") {
		t.Fatalf("no scope-change notice on stderr: %q", stderr.String())
	}
	var out storedToken
	if err := json.Unmarshal([]byte(kr.value()), &out); err != nil {
		t.Fatalf("decoding persisted token: %v", err)
	}
	if strings.Join(out.Scopes, "|") != strings.Join(MinimalScopes, "|") {
		t.Fatalf("persisted scopes = %v; want %v", out.Scopes, MinimalScopes)
	}
}
