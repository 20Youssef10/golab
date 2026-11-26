package state

// sessions.json gains an additive top-level schema_version. Legacy files must
// still read with version 0, writes must stamp the current version, corrupt
// files must still abort, and migration must never drop unknown session fields.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLegacy(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaVersionDefaultsAndRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	writeLegacy(t, p, `{"s1":{"name":"s1","endpoint":"m-s-1"}}`)
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.SchemaVersion()
	if err != nil || v != 0 {
		t.Fatalf("legacy version = %d, %v; want 0", v, err)
	}
	sessions, err := s.List()
	if err != nil || sessions["s1"].Endpoint != "m-s-1" {
		t.Fatalf("legacy sessions = %+v, %v", sessions, err)
	}
	if err := s.Put(Session{Name: "s2", Endpoint: "m-s-2"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"schema_version": 1`) {
		t.Fatalf("write did not stamp schema_version: %s", raw)
	}
	v, err = s.SchemaVersion()
	if err != nil || v != CurrentSchemaVersion {
		t.Fatalf("version after write = %d, %v", v, err)
	}
	all, err := s.List()
	if err != nil || len(all) != 2 || all["s1"].Endpoint != "m-s-1" {
		t.Fatalf("round trip lost a session: %+v, %v", all, err)
	}
}

func TestMigrationDryRunAndApply(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	legacy := `{"s1":{"name":"s1","endpoint":"m-s-1","future_field":"keep"}}`
	writeLegacy(t, p, legacy)
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.Migrate(true)
	if err != nil {
		t.Fatal(err)
	}
	if report.From != 0 || report.To != CurrentSchemaVersion || !report.WouldWrite || report.Applied {
		t.Fatalf("dry-run report = %+v", report)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != legacy {
		t.Fatalf("dry run modified file: %s", raw)
	}

	report, err = s.Migrate(false)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Applied || report.Sessions != 1 {
		t.Fatalf("apply report = %+v", report)
	}
	raw, _ = os.ReadFile(p)
	if !strings.Contains(string(raw), `"schema_version": 1`) {
		t.Fatalf("apply did not stamp version: %s", raw)
	}
	if !strings.Contains(string(raw), "future_field") {
		t.Fatalf("apply dropped an unknown field: %s", raw)
	}
	all, err := s.List()
	if err != nil || all["s1"].Endpoint != "m-s-1" {
		t.Fatalf("apply lost a session: %+v, %v", all, err)
	}

	again, err := s.Migrate(false)
	if err != nil || again.Applied || again.WouldWrite {
		t.Fatalf("repeat migrate = %+v, %v", again, err)
	}
}

func TestMigrationAbortsOnCorrupt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	writeLegacy(t, p, `{"s1": not json`)
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(false); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("migrate corrupt = %v, want ErrCorrupt", err)
	}
	if _, err := s.List(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("list corrupt = %v, want ErrCorrupt", err)
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	writeLegacy(t, p, `{"schema_version":99,"s1":{"name":"s1"}}`)
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); !errors.Is(err, ErrNewerSchema) || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("newer schema = %v, want ErrNewerSchema+ErrCorrupt", err)
	}
}
