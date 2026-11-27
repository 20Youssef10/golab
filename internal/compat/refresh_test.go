package compat

// Upstream-sync policy test: an OFFICIAL upstream CLI source is fetched with
// pinned provenance. The file served here is a FAKE served by httptest; it is
// not a fixture claiming live upstream truth (AGENTS.md: fakes are not proof).
// Upstream test analogue: tests/test_cli.py (CLI surface), translated as a
// conservative literal-token parser test.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/version"
)

func fakeUpstreamCLI(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "compat", "upstream-cli.py"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseCLISurfaceCapturesExplicitTyperFlags(t *testing.T) {
	got, err := ParseCLISurface(fakeUpstreamCLI(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		// Only explicit long options are captured. Short aliases (-c, -s),
		// derived parameter names and comment/decoy strings are not guessed.
		"colab": {
			"client-oauth-config", "config", "no-public-api", "public-api",
		},
		"colab help":       {},
		"colab fresh":      {"only"},
		"colab registered": {"session"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed surface:\n got = %#v\nwant = %#v", got, want)
	}
}

func TestFetchRecordsOfficialProvenance(t *testing.T) {
	body := fakeUpstreamCLI(t)
	var gotPath string
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	snap, err := Fetch(context.Background(), FetchConfig{
		BaseURL: srv.URL, Ref: "v0.8.0", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := "/" + "v0.8.0" + "/" + DefaultCLIPath
	if gotPath != wantPath {
		t.Fatalf("requested %q, want %q", gotPath, wantPath)
	}
	if gotUA == "" {
		t.Fatal("no honest User-Agent on the fetch request")
	}
	if snap.Kind != KindUpstream {
		t.Fatalf("kind = %q, want %q", snap.Kind, KindUpstream)
	}
	if !strings.HasPrefix(snap.SourceURL, srv.URL+wantPath) {
		t.Fatalf("source_url = %q", snap.SourceURL)
	}
	if snap.Ref != "v0.8.0" || snap.FetchedAt != now.Format(time.RFC3339) ||
		snap.Captured != snap.FetchedAt {
		t.Fatalf("provenance lost: %+v", snap)
	}
	sum := sha256.Sum256(body)
	if want := "sha256:" + hex.EncodeToString(sum[:]); snap.ContentHash != want {
		t.Fatalf("content_hash = %q, want %q", snap.ContentHash, want)
	}
	if len(snap.Commands) == 0 {
		t.Fatal("fetched snapshot has no commands")
	}
	if !strings.Contains(snap.Scope, "partial") {
		t.Fatalf("scope must admit the capture is partial: %q", snap.Scope)
	}
}

func TestFetchDefaultsRefToUpstreamCompatibilityTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fakeUpstreamCLI(t))
	}))
	defer srv.Close()
	snap, err := Fetch(context.Background(), FetchConfig{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Ref != version.Upstream {
		t.Fatalf("ref = %q, want version.Upstream %q", snap.Ref, version.Upstream)
	}
	if !strings.Contains(snap.SourceURL, version.Upstream) {
		t.Fatalf("source_url does not pin the ref: %q", snap.SourceURL)
	}
}

func TestFetchRejectsNonOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), FetchConfig{BaseURL: srv.URL}); err == nil {
		t.Fatal("fetch of a 503 response succeeded")
	}
}

func TestWriteAndLoadSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	in := Snapshot{
		Kind: KindUpstream, SourceURL: "https://example.invalid/cli.py",
		Ref: "v0.8.0", FetchedAt: "2026-11-27T12:00:00Z", ContentHash: "sha256:abc",
		Source: "official upstream CLI source", Captured: "2026-11-27T12:00:00Z",
		Commands: map[string][]string{"colab help": {}},
	}
	if err := WriteSnapshot(path, in); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"source_url"`) ||
		!strings.Contains(string(raw), `"content_hash"`) {
		t.Fatalf("provenance fields missing from written snapshot: %s", raw)
	}
	out, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip drifted:\n got = %#v\nwant = %#v", out, in)
	}
}

func TestLoadSnapshotRejectsUnlabelledFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"commands":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(path); err == nil {
		t.Fatal("snapshot without a kind was accepted")
	}
}
