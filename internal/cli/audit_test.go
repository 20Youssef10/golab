package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/state"
)

// TestAuditCommandJSON keeps the audit report machine-readable and verifies
// the warning exit code without leaking a planted secret.
func TestAuditCommandJSONAndExitCodes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLAB_CONFIG_DIR", dir)
	h := newHarness(t)
	sessions := filepath.Join(dir, "sessions.json")
	settings := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(sessions, []byte(`{"s1":{"name":"s1","token":"planted-audit-secret-1234","url":""}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"enable_update_check":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := state.NewSettingsStore(settings)
	if err != nil {
		t.Fatal(err)
	}
	h.deps.Store, h.deps.SettingsStore = store, ss

	code := runNewCommand(t, h, "audit", "--json")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (warning); stderr = %s", code, h.stderr.String())
	}
	var rep struct {
		Schema   string `json:"schema"`
		Findings []struct {
			ID       string `json:"id"`
			Severity string `json:"severity"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &rep); err != nil {
		t.Fatalf("stdout not JSON: %v (%s)", err, h.stdout.String())
	}
	if rep.Schema == "" || len(rep.Findings) == 0 {
		t.Fatalf("empty report: %+v", rep)
	}
	if strings.Contains(h.stdout.String(), "planted-audit-secret-1234") {
		t.Fatalf("secret leaked: %s", h.stdout.String())
	}
}

// TestAuditCommandTextOneFindingPerLine checks the human-readable contract.
func TestAuditCommandTextOneFindingPerLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLAB_CONFIG_DIR", dir)
	h := newHarness(t)
	if code := runNewCommand(t, h, "audit"); code != 0 && code != 1 {
		t.Fatalf("exit = %d; stderr = %s", code, h.stderr.String())
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(h.stdout.String()), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "severity=") {
			found = true
			if !strings.Contains(line, "id=") || !strings.Contains(line, "detail=") {
				t.Errorf("incomplete finding: %q", line)
			}
		}
	}
	if !found {
		t.Fatalf("no findings printed: %s", h.stdout.String())
	}
}
