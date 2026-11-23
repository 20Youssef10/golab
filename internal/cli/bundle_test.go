package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBundleRecordVerifyRoundTrip exercises record -> verify without a VM or
// network call, including the --files-root re-hash path.
func TestBundleRecordVerifyRoundTrip(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	code := filepath.Join(dir, "train.py")
	if err := os.WriteFile(code, []byte("print(1)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "bundle.json")
	if c := runNewCommand(t, h, "bundle", "record", "--out", out, "--root", dir, "--file", code,
		"--accelerator", "T4", "--env", "SEED=42"); c != 0 {
		t.Fatalf("record exit = %d, stderr = %s", c, h.stderr.String())
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("no bundle file: %v", err)
	}
	if n := len(h.ctrl.Requests()); n != 0 {
		t.Fatalf("bundle record made %d control-plane requests", n)
	}
	if c := runNewCommand(t, h, "bundle", "verify", out, "--files-root", dir); c != 0 {
		t.Fatalf("verify exit = %d, stdout = %s stderr = %s", c, h.stdout.String(), h.stderr.String())
	}
	if err := os.WriteFile(code, []byte("print(2)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := runNewCommand(t, h, "bundle", "verify", out, "--files-root", dir); c != 1 {
		t.Fatalf("verify after change exit = %d, want 1", c)
	}
}

// TestBundleRejectsSecretEnvCLI makes sure a credential passed through --env
// is rejected before any file is written and is never echoed.
func TestBundleRejectsSecretEnvCLI(t *testing.T) {
	h := newHarness(t)
	out := filepath.Join(t.TempDir(), "bundle.json")
	c := runNewCommand(t, h, "bundle", "record", "--out", out, "--env", "ACCESS_TOKEN=planted-secret-abc123")
	if c != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", c, h.stderr.String())
	}
	if strings.Contains(h.stdout.String()+h.stderr.String(), "planted-secret-abc123") {
		t.Fatalf("secret leaked: %s%s", h.stdout.String(), h.stderr.String())
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("bundle file was written despite rejected env")
	}
}
