package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
	"github.com/20Youssef10/golab/internal/update"
	"github.com/20Youssef10/golab/internal/version"
)

func testDeps(t *testing.T, srv *fakecolab.Server) (*Deps, colabapi.Client) {
	t.Helper()
	dir := t.TempDir()
	store, err := state.NewStore(filepath.Join(dir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := state.NewSettingsStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := colabapi.New(colabapi.Config{
		Domain:      srv.TunURL(),
		HTTPClient:  srv.Client(),
		ClientAgent: "github.com/20Youssef10/golab/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	return &Deps{
		Config:        Config{ColabDomain: srv.TunURL()},
		Store:         store,
		SettingsStore: settings,
		HistoryDir:    dir,
		Stdout:        &bytes.Buffer{},
		Stderr:        &stderr,
		Log:           logx.Logger(),
		colab:         client,
	}, client
}

func assign(t *testing.T, client colabapi.Client) *colabapi.AssignmentResponse {
	t.Helper()
	nbh, err := colabapi.RandomNotebookHash()
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Assign(context.Background(), colabapi.AssignRequest{
		NotebookHash: nbh,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Ports upstream tests/test_resolution_logic.py::test_resolve_unique_session.
func TestResolveUniqueSession(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, client := testDeps(t, srv)
	a := assign(t, client)
	expiry := time.Now().Add(time.Hour)
	if err := d.Store.Put(state.Session{
		Name: "s1", Endpoint: a.Endpoint, URL: a.RuntimeProxyInfo.URL,
		Token: a.RuntimeProxyInfo.Token, TokenExpiresAt: &expiry,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := d.ResolveSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "s1" {
		t.Fatalf("resolved %q", got.Name)
	}
	if !strings.Contains(d.Stderr.(*bytes.Buffer).String(), "Using unique session 's1'") {
		t.Fatalf("stderr = %q", d.Stderr.(*bytes.Buffer).String())
	}
}

// Ports upstream test_client.py token-refresh intent (docs/01 §5).
func TestGetSessionRefreshesNearExpiry(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, client := testDeps(t, srv)
	a := assign(t, client)
	expired := time.Now().Add(time.Minute) // inside the 5-minute margin
	if err := d.Store.Put(state.Session{
		Name: "s1", Endpoint: a.Endpoint, URL: "https://stale",
		Token: logx.NewSecret("stale"), TokenExpiresAt: &expired,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetSession(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != a.RuntimeProxyInfo.URL || got.Token.Reveal() != a.RuntimeProxyInfo.Token.Reveal() {
		t.Fatalf("not refreshed: %+v", got)
	}
	persisted, err := d.Store.Get("s1")
	if err != nil || persisted.Token.Reveal() != a.RuntimeProxyInfo.Token.Reveal() {
		t.Fatalf("not persisted: %+v, %v", persisted, err)
	}
}

// Ports upstream tests/test_resolution_logic.py::test_missing_session_pruned.
func TestGetSessionPrunesGoneAssignment(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, _ := testDeps(t, srv)
	expired := time.Now().Add(-time.Hour)
	if err := d.Store.Put(state.Session{
		Name: "gone", Endpoint: "no-such-endpoint", Token: logx.NewSecret("t"),
		TokenExpiresAt: &expired,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetSession(context.Background(), "gone"); err == nil {
		t.Fatal("expected an error for a pruned session")
	}
	if _, err := d.Store.Get("gone"); err != state.ErrNotFound {
		t.Fatalf("session still present: %v", err)
	}
}

func TestResolveMultipleSessionsRequiresName(t *testing.T) {
	srv := fakecolab.New()
	defer srv.Close()
	d, client := testDeps(t, srv)
	for _, name := range []string{"a", "b"} {
		a := assign(t, client)
		expiry := time.Now().Add(time.Hour)
		if err := d.Store.Put(state.Session{
			Name: name, Endpoint: a.Endpoint, Token: a.RuntimeProxyInfo.Token,
			URL: a.RuntimeProxyInfo.URL, TokenExpiresAt: &expiry,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := d.ResolveSession(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "Multiple active sessions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// testRedirectTransport points the pinned api.github.com URL at a local fake
// server without changing the production URL constant.
type testRedirectTransport struct{ base *url.URL }

func (r testRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = r.base.Scheme
	clone.URL.Host = r.base.Host
	clone.Host = r.base.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// Regression test for a production-config defect (no upstream Python port):
// UpdateConfig used to embed version.Number in AssetPattern. A user on v<current>
// then scanned a newer release for an asset named with the OLD version, which
// does not exist -> ErrNoRelease. Self-update could never offer a newer release.
// This drives the real UpdateConfig -> update.Check path with a release whose
// asset carries a version different from the compiled-in one.
func TestUpdateConfigOffersNewerRelease(t *testing.T) {
	const owner, repo = "20Youssef10", "golab"
	const newVersion, newTag = "9.9.9", "v9.9.9" // deliberately != version.Number
	osName, arch := runtime.GOOS, runtime.GOARCH
	archive := fmt.Sprintf("golab_%s_%s_%s.tar.gz", newVersion, osName, arch)
	sig, sbom := archive+".sigstore.json", archive+".sbom.sigstore.json"
	// A second platform's archive and bundles are present too; the anchored,
	// platform-specific pattern must never select them.
	decoyOS, decoyArch := "windows", "arm64"
	if osName == decoyOS && arch == decoyArch {
		decoyOS, decoyArch = "linux", "amd64"
	}
	decoy := fmt.Sprintf("golab_%s_%s_%s.tar.gz", newVersion, decoyOS, decoyArch)

	sum := sha256.Sum256([]byte("fake newer binary"))
	checksum := hex.EncodeToString(sum[:])
	download := func(name string) string {
		return "https://github.com/" + owner + "/" + repo + "/download/" + name
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     newTag,
			"published_at": "2026-11-24T00:00:00Z",
			"prerelease":   false,
			"assets": []map[string]any{
				{"name": decoy, "browser_download_url": download(decoy)},
				{"name": decoy + ".sigstore.json", "browser_download_url": download(decoy + ".sigstore.json")},
				{"name": decoy + ".sbom.sigstore.json", "browser_download_url": download(decoy + ".sbom.sigstore.json")},
				{"name": archive, "browser_download_url": download(archive)},
				{"name": sig, "browser_download_url": download(sig)},
				{"name": sbom, "browser_download_url": download(sbom)},
				{"name": "checksums.txt", "browser_download_url": download("checksums.txt")},
			},
		})
	})
	mux.HandleFunc("/"+owner+"/"+repo+"/download/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", checksum, archive)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	settings, err := state.NewSettingsStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{
		Config:        Config{Owner: owner, Repo: repo},
		SettingsStore: settings,
		Log:           logx.Logger(),
	}
	cfg, err := d.UpdateConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The no-downgrade guard must stay intact: gating belongs to IsNewer, not
	// to the asset pattern.
	if cfg.CurrentVersion != version.Number {
		t.Fatalf("CurrentVersion = %q, want %q", cfg.CurrentVersion, version.Number)
	}
	cfg.HTTPClient = &http.Client{Transport: testRedirectTransport{base: u}}

	rel, due, err := update.Check(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Check with production config: %v", err)
	}
	if !due {
		t.Fatal("expected a due check")
	}
	if rel == nil {
		t.Fatal("expected the newer release to be offered")
	}
	if rel.Version != newVersion || rel.Tag != newTag {
		t.Fatalf("release = %+v, want %s / %s", rel, newVersion, newTag)
	}
	if !strings.HasSuffix(rel.URL, "/"+archive) {
		t.Fatalf("archive URL = %q, want suffix %q", rel.URL, archive)
	}
	if !strings.HasSuffix(rel.SignatureURL, "/"+sig) || !strings.HasSuffix(rel.SBOMURL, "/"+sbom) {
		t.Fatalf("bundle URLs = %q / %q", rel.SignatureURL, rel.SBOMURL)
	}
}

// Regression for a local helper (no upstream Python port): dirOf derives the
// auth config directory from the sessions file path. The hand-rolled dirname
// returned a single character for a separator-less path ("sessions.json" ->
// "s"), so a bare filename resolved to a wrong config directory.
func TestDirOf(t *testing.T) {
	for _, tc := range []struct {
		name, path, want string
	}{
		{"absolute", filepath.FromSlash("/home/user/.config/golab/sessions.json"), filepath.FromSlash("/home/user/.config/golab")},
		{"relative", filepath.FromSlash("data/sessions.json"), filepath.FromSlash("data")},
		{"separator-less", "sessions.json", "."},
		{"root", filepath.FromSlash("/sessions.json"), string(filepath.Separator)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dirOf(tc.path); got != tc.want {
				t.Fatalf("dirOf(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
