package cli

// --verify-only is a CI gate: it must never install, never run a release
// check, and must refuse an artifact whose checksum does not match.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateVerifyOnlyRefusesUnverifiableArtifact(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	artifact := filepath.Join(dir, "golab_1.2.3_linux_amd64.tar.gz")
	if err := os.WriteFile(artifact, []byte("tampered artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"),
		[]byte("0000000000000000000000000000000000000000000000000000000000000000  golab_1.2.3_linux_amd64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := runNewCommand(t, h, "update", "--verify-only", artifact)
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "checksum") && !strings.Contains(h.stderr.String(), "verification failed") {
		t.Fatalf("no verification failure hint: %s", h.stderr.String())
	}
	if n := len(h.ctrl.Requests()); n != 0 {
		t.Fatalf("verify-only made %d control-plane requests", n)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("verify-only modified the artifact: %v", err)
	}
}
