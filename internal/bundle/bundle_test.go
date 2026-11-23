package bundle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleInput() Input {
	return Input{
		Files: []File{
			{Path: "train.py", Content: []byte("print('hello')\n")},
			{Path: "data.txt", Content: []byte("one\ntwo\n")},
		},
		Env:             map[string]string{"SEED": "42", "MODE": "ci"},
		Accelerator:     "T4",
		Variant:         "GPU",
		Shape:           "STANDARD",
		UpstreamVersion: "0.8.0",
		GolabVersion:    "0.0.1-dev",
	}
}

// TestBundleManifestIsDeterministic is the A10 gate: identical inputs must
// produce byte-identical manifests and IDs, including file order and env
// iteration order.
func TestBundleManifestIsDeterministic(t *testing.T) {
	a, err := New(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	ba, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ba) != string(bb) {
		t.Fatalf("manifest drift:\n%s\n%s", ba, bb)
	}
	if a.ID == "" || len(a.ID) != 64 {
		t.Fatalf("bad bundle ID %q", a.ID)
	}
}

// TestBundleIDChangesPerInput proves the ID covers code, env, accelerator and
// upstream version; a change to one must change exactly that digest and the ID.
func TestBundleIDChangesPerInput(t *testing.T) {
	base, err := New(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Input)
		field  func(*Manifest) string
	}{
		{"code", func(in *Input) { in.Files[0].Content = []byte("print('bye')\n") }, func(m *Manifest) string { return m.CodeHash }},
		{"env", func(in *Input) { in.Env["SEED"] = "43" }, func(m *Manifest) string { return m.EnvHash }},
		{"accelerator", func(in *Input) { in.Accelerator = "A100" }, func(m *Manifest) string { return m.Accelerator }},
		{"upstream", func(in *Input) { in.UpstreamVersion = "0.9.0" }, func(m *Manifest) string { return m.UpstreamVersion }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := sampleInput()
			tc.mutate(&in)
			m, err := New(in)
			if err != nil {
				t.Fatal(err)
			}
			if m.ID == base.ID {
				t.Errorf("bundle ID did not change for %s", tc.name)
			}
			if tc.field(m) == tc.field(base) {
				t.Errorf("field did not change for %s", tc.name)
			}
		})
	}
}

// TestBundleVerifyDetectsTamper checks that a modified provenance file fails
// verification instead of being trusted.
func TestBundleVerifyDetectsTamper(t *testing.T) {
	m, err := New(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	m.Accelerator = "H100"
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(b); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("Verify error = %v, want mismatch", err)
	}
}

// TestBundleVerifyFiles re-hashes local inputs and refuses unsafe paths.
func TestBundleVerifyFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "train.py"), []byte("print('hello')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyFiles(context.Background(), dir); err != nil {
		t.Fatalf("VerifyFiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "train.py"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyFiles(context.Background(), dir); err == nil {
		t.Fatal("VerifyFiles accepted a changed input")
	}
	m.Files[0].Path = "../outside.py"
	if err := m.VerifyFiles(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("traversal error = %v, want unsafe path", err)
	}
}

// TestBundleRejectsSecretEnv prevents credentials from being embedded in a
// bundle through --env; the error must not echo the value.
func TestBundleRejectsSecretEnv(t *testing.T) {
	in := sampleInput()
	in.Env["ACCESS_TOKEN"] = "planted-bundle-secret-9876"
	_, err := New(in)
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("error = %v, want secret-key rejection", err)
	}
	if strings.Contains(err.Error(), "planted-bundle-secret-9876") {
		t.Fatalf("error leaked the secret: %v", err)
	}
}

// TestBundleWriteUsesPrivateMode verifies provenance files are owner-only.
func TestBundleWriteUsesPrivateMode(t *testing.T) {
	m, err := New(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bundle.json")
	if err := Write(context.Background(), path, m); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(got); err != nil {
		t.Fatalf("written bundle does not verify: %v", err)
	}
}
