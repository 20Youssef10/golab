package cli

// The compat command must be honest: it reports deltas against a self-snapshot
// by default and never contacts a release server or the upstream CLI. `compat
// refresh` fetches an OFFICIAL upstream source explicitly, and any snapshot it
// produces is labelled upstream-fetched (never live truth).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/compat"
)

func runCompatCommand(t *testing.T, h *testHarness, args ...string) int {
	t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	root := NewRootCommand(strings.NewReader(""), &h.stdout, &h.stderr)
	addIfMissing(root, "compat", RegisterCompatCommands)
	root.SetArgs(args)
	err := root.ExecuteContext(WithDeps(context.Background(), h.deps))
	if err == nil {
		return 0
	}
	mapped := mapError(err)
	var ee *exitError
	if !asExitError(mapped, &ee) {
		panic(mapped)
	}
	if ee.msg != "" {
		h.stderr.WriteString(ee.msg + "\n")
	}
	return ee.code
}

func TestCompatReportJSON(t *testing.T) {
	h := newHarness(t)
	if code := runCompatCommand(t, h, "compat", "report", "--json"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	var rep struct {
		Schema       string `json:"schema"`
		SnapshotKind string `json:"snapshot_kind"`
		Live         bool   `json:"live"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &rep); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, h.stdout.String())
	}
	if rep.Schema == "" || rep.SnapshotKind != "self-snapshot" || rep.Live {
		t.Fatalf("dishonest report: %+v", rep)
	}
	if n := len(h.ctrl.Requests()); n != 0 {
		t.Fatalf("compat report made %d control-plane requests", n)
	}
}

func TestCompatReportTextLabelsSnapshot(t *testing.T) {
	h := newHarness(t)
	if code := runCompatCommand(t, h, "compat", "report"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.Contains(out, "self-snapshot") || !strings.Contains(out, "NOT a live") {
		t.Fatalf("snapshot provenance not printed: %s", out)
	}
}

// withCompatFakeUpstream serves a fake upstream CLI file from httptest and
// points the CLI's fetch seam at it. No production host is contacted.
func withCompatFakeUpstream(t *testing.T) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "compat", "upstream-cli.py"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	original := compatFetch
	compatFetch = func(ctx context.Context, cfg compat.FetchConfig) (compat.Snapshot, error) {
		cfg.BaseURL = srv.URL
		return compat.Fetch(ctx, cfg)
	}
	t.Cleanup(func() { compatFetch = original })
}

func TestCompatRefreshDryRunDoesNotWrite(t *testing.T) {
	withCompatFakeUpstream(t)
	dir := t.TempDir()
	t.Chdir(dir)
	h := newHarness(t)
	if code := runCompatCommand(t, h, "compat", "refresh"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, "upstream-fetched") {
		t.Fatalf("dry-run provenance not printed: %s", out)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("dry-run wrote %d files", len(entries))
	}
	if n := len(h.ctrl.Requests()); n != 0 {
		t.Fatalf("compat refresh made %d control-plane requests", n)
	}
}

func TestCompatRefreshWritesOnlyWithOut(t *testing.T) {
	withCompatFakeUpstream(t)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	h := newHarness(t)
	if code := runCompatCommand(t, h, "compat", "refresh", "--out", path, "--ref", "v0.8.0"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snap compat.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("written snapshot is not JSON: %v", err)
	}
	if snap.Kind != "upstream-fetched" || snap.Ref != "v0.8.0" ||
		snap.SourceURL == "" || snap.FetchedAt == "" || snap.ContentHash == "" ||
		len(snap.Commands) == 0 {
		t.Fatalf("snapshot lacks provenance/surface: %+v", snap)
	}
	// --out - prints JSON to stdout and writes nothing.
	dir := t.TempDir()
	t.Chdir(dir)
	h.stdout.Reset()
	if code := runCompatCommand(t, h, "compat", "refresh", "--out", "-"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	var stdoutSnap compat.Snapshot
	if err := json.Unmarshal(h.stdout.Bytes(), &stdoutSnap); err != nil {
		t.Fatalf("--out - did not print JSON: %v (%s)", err, h.stdout.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("--out - wrote %d files", len(entries))
	}
}

func TestCompatReportConsumesFetchedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	snap := compat.Snapshot{
		Kind: "upstream-fetched", SourceURL: "https://raw.githubusercontent.com/example",
		Ref: "v0.8.0", FetchedAt: "2026-11-27T12:00:00Z", Captured: "2026-11-27T12:00:00Z",
		ContentHash: "sha256:abc", Source: "official upstream CLI source",
		Scope: "partial literal-token capture", Method: "typer-source-literals",
		Commands: map[string][]string{"colab help": {}},
	}
	if err := compat.WriteSnapshot(path, snap); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	if code := runCompatCommand(t, h, "compat", "report", "--snapshot", path); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.Contains(out, "upstream-fetched") || !strings.Contains(out, "NOT live truth") ||
		!strings.Contains(out, snap.SourceURL) || !strings.Contains(out, snap.ContentHash) {
		t.Fatalf("fetched snapshot provenance not printed: %s", out)
	}
	h.stdout.Reset()
	if code := runCompatCommand(t, h, "compat", "report", "--snapshot", path, "--json"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	var rep struct {
		SnapshotKind string `json:"snapshot_kind"`
		SourceURL    string `json:"source_url"`
		Ref          string `json:"ref"`
		ContentHash  string `json:"content_hash"`
		Live         bool   `json:"live"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &rep); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, h.stdout.String())
	}
	if rep.SnapshotKind != "upstream-fetched" || rep.SourceURL != snap.SourceURL ||
		rep.Ref != "v0.8.0" || rep.ContentHash != snap.ContentHash || rep.Live {
		t.Fatalf("dishonest fetched report: %+v", rep)
	}
}
