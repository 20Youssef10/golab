// Ported from upstream docs/public_api_migration_plan.md §4/§6 (the
// SessionState.runtime_name persistence and cross-API matching tests that are
// planned upstream but not implemented there).
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/logx"
)

// Step 4: SessionState.runtime_name round-trips and is omitted when empty.
func TestSessionRuntimeNameRoundTrip(t *testing.T) {
	s := newTestStore(t)
	sess := Session{
		Name: "r1", Token: logx.NewSecret("tok"), URL: "https://x",
		Endpoint: "m-s-endpoint", RuntimeName: "runtimes/r-1",
	}
	if err := s.Put(sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimeName != "runtimes/r-1" {
		t.Fatalf("runtime_name = %q", got.RuntimeName)
	}

	b, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"runtime_name": "runtimes/r-1"`) {
		t.Fatalf("runtime_name not persisted: %s", b)
	}

	// Omitempty: a session without a runtime name must not write the key, and
	// an old file without the key must load cleanly (backward compatibility).
	if err := s.Put(Session{Name: "r2", Token: logx.NewSecret("t")}); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(all["r2"]), "runtime_name") {
		t.Fatalf("runtime_name should be omitted when empty: %s", all["r2"])
	}
}

// Step 4: a legacy sessions.json without runtime_name must not be treated as
// corrupt (preserve old files).
func TestLegacySessionFileWithoutRuntimeNameLoads(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sessions.json")
	legacy := `{"old":{"name":"old","token":"tok","url":"https://x","endpoint":"m-s-old"}}`
	if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("old")
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimeName != "" || got.Endpoint != "m-s-old" {
		t.Fatalf("legacy session = %+v", got)
	}
}

// A legacy settings.json without the newer optional fields must still load
// (JSON omitempty keeps old files valid).
func TestLegacySettingsFileLoads(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(`{"update_url":"https://example","enable_update_check":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewSettingsStore(p)
	if err != nil {
		t.Fatal(err)
	}
	v, err := store.Load()
	if err != nil || v.UpdateURL != "https://example" || !v.EnableUpdateCheck {
		t.Fatalf("legacy settings = %+v, %v", v, err)
	}
	if v.UsePublicAPI || v.PublicAPISupported != nil || v.TunnelAPISupported != nil {
		t.Fatalf("unexpected non-default fields: %+v", v)
	}
}

// Step 4: matching by endpoint OR runtime_name (used when refreshing tokens).
func TestMatchAssignment(t *testing.T) {
	s := Session{Name: "s", Endpoint: "m-s-endpoint", RuntimeName: "runtimes/r-1"}
	for _, tc := range []struct {
		endpoint, runtime string
		want              bool
	}{
		{"m-s-endpoint", "", true},
		{"", "runtimes/r-1", true},
		{"m-s-other", "runtimes/r-1", true},
		{"m-s-endpoint", "runtimes/r-other", true},
		{"m-s-other", "runtimes/r-other", false},
		{"", "", false},
	} {
		if got := MatchAssignment(s, tc.endpoint, tc.runtime); got != tc.want {
			t.Errorf("MatchAssignment(%q,%q) = %v, want %v", tc.endpoint, tc.runtime, got, tc.want)
		}
	}
}

// Step 4: the store helper locates a persisted session by runtime name.
func TestFindByRuntimeName(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put(Session{Name: "a", RuntimeName: "runtimes/r-a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Session{Name: "b", Endpoint: "m-s-b"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.FindByRuntimeName("runtimes/r-a"); !ok || got.Name != "a" {
		t.Fatalf("FindByRuntimeName = %+v, %v", got, ok)
	}
	if _, ok := s.FindByRuntimeName("runtimes/r-none"); ok {
		t.Fatal("unexpected match")
	}
}
