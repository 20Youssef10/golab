package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAgentOutCommand verifies the CLI wrapper emits typed events in both
// machine-readable formats and leaves text mode untouched.
func TestAgentOutCommand(t *testing.T) {
	h := newHarness(t)
	if code := runNewCommand(t, h, "agentout", "--format", "ndjson", "--type", "info", "hello", "world"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	var e struct {
		Schema string `json:"schema"`
		Type   string `json:"type"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &e); err != nil {
		t.Fatalf("stdout not JSON: %v (%s)", err, h.stdout.String())
	}
	if e.Schema != "golab/1" || e.Type != "info" || e.Text != "hello world" {
		t.Fatalf("event = %+v", e)
	}
	if n := len(h.ctrl.Requests()); n != 0 {
		t.Fatalf("agentout made %d control-plane requests", n)
	}

	if code := runNewCommand(t, h, "agentout", "--format", "text", "plain"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	if strings.TrimSpace(h.stdout.String()) != "plain" {
		t.Fatalf("text mode = %q", h.stdout.String())
	}
}
