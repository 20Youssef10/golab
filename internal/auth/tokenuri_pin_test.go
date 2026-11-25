package auth

// Regression tests for defect 2.6: a planted stored token blob must not be
// able to choose the OAuth client identity or the token endpoint. These are
// golab-specific (upstream Python's test suite has no equivalent).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

const pinnedTokenURI = "https://oauth2.googleapis.com/token"

func plantToken(t *testing.T, kr *memKeyring, mutate func(*storedToken)) {
	t.Helper()
	s := storedToken{
		RefreshToken: logx.NewSecret("non-expiring-refresh"),
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(24 * time.Hour),
		Scopes:       append([]string(nil), Scopes...),
	}
	if mutate != nil {
		mutate(&s)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := kr.Set(KeyringService, KeyringUser, string(b)); err != nil {
		t.Fatal(err)
	}
}

// recordingTransport keeps every token request local: an unexpected endpoint
// fails the request instead of reaching the network, so a redirect shows up as
// a recorded URL rather than a real outbound call.
type recordingTransport struct {
	urls []string
	base http.RoundTripper
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.urls = append(r.urls, req.URL.String())
	host := req.URL.Hostname()
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		if !strings.EqualFold(host, "localhost") {
			return nil, fmt.Errorf("unexpected non-loopback token endpoint %q", req.URL.String())
		}
	}
	return r.base.RoundTrip(req)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func embeddedClientID(t *testing.T) string {
	t.Helper()
	var f clientConfigFile
	if err := json.Unmarshal(bundledOAuthConfig, &f); err != nil {
		t.Fatal(err)
	}
	d := f.Installed
	if d == nil {
		d = f.Web
	}
	if d == nil || d.ClientID == "" {
		t.Fatal("bundled OAuth config has no client id")
	}
	return d.ClientID
}

// (a) A planted token_uri must not redirect the refresh exchange. The refresh
// token in this blob never expires, so the exchange happens on every run.
func TestPlantedTokenURIIsIgnored(t *testing.T) {
	var forms []url.Values
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		forms = append(forms, f)
		return 200, tokenJSON("fresh-access", "", 3600, Scopes)
	})
	defer srv.Close()

	cfg, kr, _, _ := testConfig(t, srv.URL, "code\n") // fallback flow is local too
	plantToken(t, kr, func(s *storedToken) { s.TokenURI = "https://evil.example/token" })
	p := mustProvider(t, cfg).(*oauth2Provider)
	rec := &recordingTransport{base: http.DefaultTransport}
	p.tokenHTTP = &http.Client{Transport: rec}

	if _, err := p.HTTPClient(context.Background(), nil); err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}
	if len(rec.urls) != 1 || rec.urls[0] != srv.URL {
		t.Fatalf("token endpoints = %v, want only %q", rec.urls, srv.URL)
	}
	if len(forms) != 1 || forms[0].Get("grant_type") != "refresh_token" {
		t.Fatalf("token requests = %v, want one refresh at the configured endpoint", forms)
	}
}

// (b) A planted client_id must not override the client from the OAuth config.
func TestPlantedClientIDIsIgnored(t *testing.T) {
	var forms []url.Values
	srv := fakeTokenServer(t, func(f url.Values) (int, string) {
		forms = append(forms, f)
		return 200, tokenJSON("fresh-access", "", 3600, Scopes)
	})
	defer srv.Close()

	cfg, kr, _, _ := testConfig(t, srv.URL, "code\n")
	plantToken(t, kr, func(s *storedToken) { s.ClientID = "planted-client-id" })
	p := mustProvider(t, cfg).(*oauth2Provider)
	rec := &recordingTransport{base: http.DefaultTransport}
	p.tokenHTTP = &http.Client{Transport: rec}

	if _, err := p.HTTPClient(context.Background(), nil); err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}
	if len(forms) == 0 {
		t.Fatal("token endpoint was not called")
	}
	if got := forms[0].Get("client_id"); got != "test-client" {
		t.Fatalf("refresh client_id = %q, want the configured client %q", got, "test-client")
	}
	if len(rec.urls) != 1 || rec.urls[0] != srv.URL {
		t.Fatalf("token endpoints = %v, want only %q", rec.urls, srv.URL)
	}
}

// (c) The legitimate Google endpoint is still accepted and usable for refresh.
func TestPinnedGoogleEndpointStillRefreshes(t *testing.T) {
	if !allowedTokenURI(pinnedTokenURI) {
		t.Fatalf("pinned endpoint %q rejected", pinnedTokenURI)
	}

	kr := &memKeyring{data: make(map[string]string)}
	plantToken(t, kr, nil)
	cfg := Config{
		ConfigDir: t.TempDir(),
		Keyring:   kr,
		Stdin:     strings.NewReader(""),
		Stdout:    io.Discard,
		Stderr:    io.Discard,
	}
	p := mustProvider(t, cfg).(*oauth2Provider)

	var gotURL string
	var gotForm url.Values
	p.tokenHTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		gotForm, err = url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(tokenJSON("google-access", "", 3600, Scopes))),
			Request:    req,
		}, nil
	})}

	if _, err := p.HTTPClient(context.Background(), nil); err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}
	if gotURL != pinnedTokenURI {
		t.Fatalf("token endpoint = %q, want %q", gotURL, pinnedTokenURI)
	}
	if got := gotForm.Get("client_id"); got != embeddedClientID(t) {
		t.Fatalf("client_id = %q, want the embedded client id", got)
	}
	if got := gotForm.Get("grant_type"); got != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token", got)
	}
}

// Only the pinned endpoint is allowed. Any other HTTPS host (including a
// lookalike) and any plaintext HTTP host are rejected, in test builds too.
func TestAllowedTokenURIOnlyPinsGoogleEndpoint(t *testing.T) {
	for _, raw := range []string{
		pinnedTokenURI,
	} {
		if !allowedTokenURI(raw) {
			t.Errorf("allowedTokenURI(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{
		"https://evil.example/token",
		"https://oauth2.googleapis.com/token.evil.example/token",
		"https://oauth2.googleapis.com:443/token",
		"https://oauth2.googleapis.com/token/",
		"http://127.0.0.1:1/token",
		"http://localhost:1/token",
		"not a url",
	} {
		if allowedTokenURI(raw) {
			t.Errorf("allowedTokenURI(%q) = true, want false", raw)
		}
	}
}

// (d) Loopback is a test-only escape hatch. A release-like FlagSet (no
// "test.v" flag, as in a built binary) must reject it while keeping the pin.
func TestLoopbackTokenURIOnlyInTestBinary(t *testing.T) {
	const loopback = "http://127.0.0.1:1"
	if !allowedTokenURI(loopback) {
		t.Fatalf("loopback token URI rejected in the test binary: %q", loopback)
	}

	original := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet("release", flag.ContinueOnError)
	t.Cleanup(func() { flag.CommandLine = original })

	if allowedTokenURI(loopback) {
		t.Errorf("loopback token URI accepted in a release-like binary: %q", loopback)
	}
	if !allowedTokenURI(pinnedTokenURI) {
		t.Errorf("pinned endpoint rejected in a release-like binary")
	}
	if allowedTokenURI("https://evil.example/token") {
		t.Errorf("unpinned endpoint accepted in a release-like binary")
	}
}

// A client config pointing anywhere but the pin must be refused with a typed
// error, before any token request is made.
func TestLoadClientConfigRejectsUnpinnedTokenURI(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		ConfigDir:             dir,
		ClientOAuthConfigPath: writeClientConfig(t, dir, "https://evil.example/token"),
		Stdin:                 strings.NewReader(""),
		Stdout:                io.Discard,
		Stderr:                io.Discard,
	}
	p := mustProvider(t, cfg).(*oauth2Provider)
	if _, err := p.loadClientConfig(); !errors.Is(err, ErrInsecureTokenURI) {
		t.Fatalf("loadClientConfig error = %v, want ErrInsecureTokenURI", err)
	}
}

// The bundled config remains the source of truth for the pin.
func TestLoadClientConfigBundledPin(t *testing.T) {
	cfg := Config{ConfigDir: t.TempDir(), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
	p := mustProvider(t, cfg).(*oauth2Provider)
	cc, err := p.loadClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cc.ClientID != embeddedClientID(t) || cc.TokenURL != pinnedTokenURI {
		t.Fatalf("bundled config = %q / %q, want embedded id / %q", cc.ClientID, cc.TokenURL, pinnedTokenURI)
	}
}
