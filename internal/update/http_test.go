package update

// Hardening for docs/gap-report.md §4.6 (no upstream Python analogue):
// browser_download_url comes from release metadata, so a planted release must
// not be able to redirect an authenticated update request off GitHub or
// downgrade it from https to http. Signature verification stops execution of
// a hostile artifact, but it does not stop the request itself.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// updateRedirectServer answers the initial /download/start request with a
// redirect to location and serves a short body everywhere else. Combined with
// fakeConfig's redirectTransport, every host is rewritten to this local server
// at the transport layer — so a redirect is only refused if CheckRedirect
// inspects the URL it was given, before the transport rewrites it.
func updateRedirectServer(t *testing.T, location string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/start") {
			http.Redirect(w, r, location, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(s.Close)
	return s
}

func fetchThroughUpdateRedirect(t *testing.T, location string) ([]byte, error) {
	t.Helper()
	s := updateRedirectServer(t, location)
	cfg := fakeConfig(s)
	return getBytes(context.Background(), httpClient(cfg),
		"https://github.com/20Youssef10/golab/download/start", maxArtifact)
}

func assertRefusedRedirect(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("redirect was followed; want a typed redirect error")
	}
	var redirectErr *RedirectError
	if !errors.As(err, &redirectErr) {
		t.Fatalf("error = %T: %v, want a *RedirectError", err, err)
	}
	if !errors.Is(err, ErrUnsafeRedirect) {
		t.Fatalf("error %v does not unwrap to ErrUnsafeRedirect", err)
	}
}

func TestUpdateRedirectRefusesOffHost(t *testing.T) {
	_, err := fetchThroughUpdateRedirect(t, "https://evil.example.test/asset")
	assertRefusedRedirect(t, err)
}

func TestUpdateRedirectRefusesHostSuffixLookalike(t *testing.T) {
	_, err := fetchThroughUpdateRedirect(t, "https://github.com.evil.example.test/asset")
	assertRefusedRedirect(t, err)
}

func TestUpdateRedirectRefusesDowngrade(t *testing.T) {
	_, err := fetchThroughUpdateRedirect(t, "http://github.com/20Youssef10/golab/download/asset")
	assertRefusedRedirect(t, err)
}

func TestUpdateRedirectErrorRedactsCredentials(t *testing.T) {
	_, err := fetchThroughUpdateRedirect(t, "https://user:s3cr3t@evil.example.test/asset")
	assertRefusedRedirect(t, err)
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("redirect error leaked credentials: %v", err)
	}
}

func TestUpdateRedirectAllowsGitHubHosts(t *testing.T) {
	for _, location := range []string{
		"https://github.com/20Youssef10/golab/releases/download/v1.2.3/asset",
		"https://objects.githubusercontent.com/asset",
	} {
		t.Run(location, func(t *testing.T) {
			got, err := fetchThroughUpdateRedirect(t, location)
			if err != nil {
				t.Fatalf("legitimate in-allowlist redirect failed: %v", err)
			}
			if string(got) != "payload" {
				t.Fatalf("body = %q, want payload", got)
			}
		})
	}
}
