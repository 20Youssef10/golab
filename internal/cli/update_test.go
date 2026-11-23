package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/update"
)

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return -1
}

type countingTransport struct {
	n  int
	rt http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n++
	return c.rt.RoundTrip(r)
}

// Ports upstream tests/test_update.py::test_auto_update_skips_when_recent: a
// throttled check must not make a network call and must not rewrite LastCheck.
func TestUpdateThrottledNoNetwork(t *testing.T) {
	h := newHarness(t)
	recent := time.Now().Add(-1 * time.Hour)
	settings, err := h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.LastCheck = &recent
	if err := h.deps.SettingsStore.Save(settings); err != nil {
		t.Fatal(err)
	}
	ct := &countingTransport{rt: http.DefaultTransport}
	cfg := update.Config{
		Owner: "o", Repo: "r", AssetPattern: "golab_.*",
		CurrentVersion: "1.0.0", LastCheck: &recent, Enabled: true,
		HTTPClient: &http.Client{Transport: ct, Timeout: 5 * time.Second},
	}
	setStreams(h)
	if code := exitCodeOf(t, runUpdate(context.Background(), h.deps, cfg, false)); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if ct.n != 0 {
		t.Fatalf("network calls while throttled: %d", ct.n)
	}
	settings, err = h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.LastCheck == nil || !settings.LastCheck.Equal(recent) {
		t.Fatalf("LastCheck changed while throttled: %v", settings.LastCheck)
	}
}

// Ports upstream tests/test_update.py::test_check_persists_latest_version_from_pypi:
// a due check persists both LastCheck and LatestVersion into settings.json.
func TestUpdatePersistsSettingsWhenDue(t *testing.T) {
	h := newHarness(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("BIN"))
	}))
	defer server.Close()
	cfg := update.Config{Owner: "o", Repo: "r", CurrentVersion: "1.0.0", Enabled: true}
	rel := &update.Release{
		Version: "2.0.0", Tag: "v2.0.0", URL: server.URL,
		Checksum: strings.Repeat("0", 64), SignatureURL: "http://sig", SBOMURL: "http://sbom",
	}
	setStreams(h)
	if code := exitCodeOf(t, applyUpdate(context.Background(), h.deps, cfg, rel, true, false)); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	settings, err := h.deps.SettingsStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.LastCheck == nil || time.Since(*settings.LastCheck) > time.Minute {
		t.Fatalf("LastCheck = %v", settings.LastCheck)
	}
	if settings.LatestVersion != "2.0.0" {
		t.Fatalf("LatestVersion = %q", settings.LatestVersion)
	}
}

// Ports upstream tests/test_update.py::test_verification_failure: a release
// whose checksum does not match must be refused with a clear, nonzero error.
func TestUpdateVerificationFailureExitNonzero(t *testing.T) {
	h := newHarness(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("BIN"))
	}))
	defer server.Close()
	cfg := update.Config{Owner: "o", Repo: "r", CurrentVersion: "1.0.0", Enabled: true}
	rel := &update.Release{
		Version: "2.0.0", URL: server.URL,
		Checksum: strings.Repeat("0", 64), SignatureURL: "http://sig", SBOMURL: "http://sbom",
	}
	setStreams(h)
	err := applyUpdate(context.Background(), h.deps, cfg, rel, true, true)
	if code := exitCodeOf(t, err); code == 0 {
		t.Fatalf("verification failure exited 0: %v", err)
	}
	if !strings.Contains(err.Error(), "verification failed") || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("unclear error: %v", err)
	}
	if errors.Is(err, update.ErrChecksumMismatch) == false {
		t.Fatalf("error should wrap ErrChecksumMismatch: %v", err)
	}
}
