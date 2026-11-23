package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Ports upstream tests/test_cli.py (utility cases): `pay` must print the
// sign-up URL and never put program output on stderr with a token attached.
func TestPayPrintsSignupURL(t *testing.T) {
	h := newHarness(t)
	code := h.run(t, "", "pay")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "https://colab.research.google.com/signup") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	if strings.Contains(h.stdout.String(), "[colab]") {
		t.Fatalf("chatter on stdout: %q", h.stdout.String())
	}
}

// Ports upstream tests/test_whoami.py: identity, scopes and expiry are printed;
// the access token itself must NEVER appear anywhere.
func TestWhoamiNeverPrintsToken(t *testing.T) {
	const token = "SUPER_SECRET_ACCESS_TOKEN_DO_NOT_PRINT_12345"
	h := newHarness(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != token {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"email": "dev@example.com", "audience": "aud-123",
			"expires_in": "1800", "scope": "openid https://www.googleapis.com/auth/colaboratory",
		})
	}))
	defer server.Close()

	setStreams(h)
	code := exitCodeOf(t, printWhoami(context.Background(), h.deps, "oauth2", token, server.URL))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{
		"Auth provider: oauth2", "Email:         dev@example.com",
		"Audience:      aud-123", "Expires in:    30m",
		"https://www.googleapis.com/auth/colaboratory",
	} {
		if !strings.Contains(h.stdout.String(), want) {
			t.Fatalf("stdout missing %q: %q", want, h.stdout.String())
		}
	}
	combined := h.stdout.String() + h.stderr.String()
	if strings.Contains(combined, token) {
		t.Fatalf("token leaked: %q", combined)
	}
}

// Ports upstream tests/test_whoami.py (error path): tokeninfo rejections exit 1
// without echoing the response body (which may contain the token).
func TestWhoamiRejectsInvalidToken(t *testing.T) {
	const token = "SECRET_SHOULD_NOT_LEAK"
	h := newHarness(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"invalid_token","access_token":%q}`, token)))
	}))
	defer server.Close()
	setStreams(h)
	err := printWhoami(context.Background(), h.deps, "adc", token, server.URL)
	if code := exitCodeOf(t, err); code == 0 {
		t.Fatalf("invalid token exited 0: %v", err)
	}
	if strings.Contains(h.stdout.String()+h.stderr.String(), token) {
		t.Fatalf("token leaked: %q", h.stdout.String()+h.stderr.String())
	}
}

// Ports upstream tests/test_cli.py::test_test_public_api: both probes are
// reported and their results are persisted into settings.json.
func TestPublicAPIProbePersistsSettings(t *testing.T) {
	h := newHarness(t)
	code := h.run(t, "", "test-public-api")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	for _, want := range []string{
		"Public API (colaboratory.googleapis.com): OK",
		"Tunnel API (colab.research.google.com):   OK",
	} {
		if !strings.Contains(h.stdout.String(), want) {
			t.Fatalf("stdout missing %q: %q", want, h.stdout.String())
		}
	}
	settings, err := h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.PublicAPISupported == nil || !*settings.PublicAPISupported {
		t.Fatalf("PublicAPISupported = %v", settings.PublicAPISupported)
	}
	if settings.TunnelAPISupported == nil || !*settings.TunnelAPISupported {
		t.Fatalf("TunnelAPISupported = %v", settings.TunnelAPISupported)
	}
}

// Ports upstream tests/test_readme.py / tests/test_cli.py (utility cases): the
// bundled, honest documentation is printed by all four aliases.
func TestReadmeAndSkillAliases(t *testing.T) {
	for _, name := range []string{"readme", "README", "skill", "SKILL"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			code := h.run(t, "", name)
			if code != 0 {
				t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
			}
			if !strings.Contains(h.stdout.String(), "golab") {
				t.Fatalf("output = %q", h.stdout.String())
			}
		})
	}
}
