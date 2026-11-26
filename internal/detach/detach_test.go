package detach

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Upstream analogue: docs/09 detach contract + ADR-001 detached runs. These
// tests pin the payload-safety and remote-path invariants before any CLI wiring.

func TestRunDirUsesPOSIXPathAndValidatesID(t *testing.T) {
	if got, want := RunDir("run-abc123"), "/content/.golab/runs/run-abc123"; got != want {
		t.Fatalf("RunDir = %q, want %q", got, want)
	}
	if got, want := RemoteFile("run-abc123", "log"), "/content/.golab/runs/run-abc123/log"; got != want {
		t.Fatalf("RemoteFile = %q, want %q", got, want)
	}
	for _, id := range []string{"run-abc123", "run-0123456789abcdef", "a", "RUN_X-1"} {
		if !ValidID(id) {
			t.Fatalf("ValidID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"", "../etc", "a/b", "a b", "a;b", "run-../../../x"} {
		if ValidID(id) {
			t.Fatalf("ValidID(%q) = true, want false", id)
		}
	}
}

func TestLauncherUsesDetachedSessionAndNeverSplicesUserStrings(t *testing.T) {
	malicious := "print('ok')\n'; import os; os.system('touch /tmp/pwned') #"
	script := []byte(malicious)
	args := []string{"--flag", "a b", "'; echo pwn"}
	env := map[string]string{"API_KEY": "s3cr3t with spaces", "MODE": "fast"}

	r := Record{ID: "run-abc123", Session: "s1", Script: "job.py", StartedAt: time.Unix(1, 0).UTC(), TTL: "1h"}
	src, err := BuildLauncher(r, script, args, env)
	if err != nil {
		t.Fatal(err)
	}

	// The remote child must be detached from the launching cell's session.
	for _, want := range []string{"start_new_session=True", "meta.json", "log", "exit"} {
		if !strings.Contains(src, want) {
			t.Fatalf("launcher lacks %q:\n%s", want, src)
		}
	}
	// No user-controlled string is ever spliced into generated Python source.
	if strings.Contains(src, malicious) || strings.Contains(src, "s3cr3t with spaces") ||
		strings.Contains(src, "echo pwn") {
		t.Fatalf("launcher splices user text into source:\n%s", src)
	}

	// Everything the child needs round-trips through the base64 JSON payload.
	p, err := buildPayload(r, script, args, env)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(p.Script)
	if err != nil || !bytes.Equal(decoded, script) {
		t.Fatalf("script payload round-trip failed: %q / %v", decoded, err)
	}
	if !reflectEqual(p.Argv, append([]string{"job.py"}, args...)) {
		t.Fatalf("argv payload = %v", p.Argv)
	}
	if p.Env["API_KEY"] != env["API_KEY"] || p.Env["MODE"] != env["MODE"] {
		t.Fatalf("env payload = %v", p.Env)
	}
	metaJSON, err := base64.StdEncoding.DecodeString(p.Meta)
	if err != nil || !json.Valid(metaJSON) {
		t.Fatalf("meta payload is not valid JSON: %q / %v", metaJSON, err)
	}
	var meta Record
	if err := json.Unmarshal(metaJSON, &meta); err != nil || meta.ID != r.ID || meta.TTL != "1h" {
		t.Fatalf("meta payload round-trip failed: %+v / %v", meta, err)
	}
	if !strings.Contains(src, base64.StdEncoding.EncodeToString(mustJSON(t, p))) {
		t.Fatalf("launcher does not embed the encoded payload:\n%s", src)
	}
}

func TestManifestRoundTripAndCorruption(t *testing.T) {
	dir := t.TempDir()
	p := ManifestPath(dir)
	if !strings.HasSuffix(p, "detached-runs.json") {
		t.Fatalf("unexpected manifest path %q", p)
	}
	m, err := LoadManifest(p) // missing file is an empty manifest
	if err != nil || len(m.Runs) != 0 {
		t.Fatalf("LoadManifest(missing) = %+v / %v", m, err)
	}
	m.Schema = ManifestSchema
	m.Runs["run-abc"] = Record{ID: "run-abc", Session: "s1", Script: "job.py", StartedAt: time.Unix(2, 0).UTC(), TTL: "30m"}
	if err := SaveManifest(p, m); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("manifest must be 0600, got %v / %v", info, err)
	}
	got, err := LoadManifest(p)
	if err != nil || len(got.Runs) != 1 || got.Runs["run-abc"].TTL != "30m" {
		t.Fatalf("LoadManifest = %+v / %v", got, err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(p); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt manifest error = %v, want ErrCorrupt", err)
	}
}

func TestNewIDIsValidAndDistinct(t *testing.T) {
	a, b := NewID(), NewID()
	if !ValidID(a) || !ValidID(b) || a == b {
		t.Fatalf("NewID() = %q, %q", a, b)
	}
}

func reflectEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
