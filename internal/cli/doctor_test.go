package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDoctorCommandOfflineJSON checks the --json contract and confirms that an
// injected control plane receives zero requests in the default offline mode.
func TestDoctorCommandOfflineJSON(t *testing.T) {
	h := newHarness(t)
	if code := runNewCommand(t, h, "doctor", "--json"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	var report struct {
		Schema string `json:"schema"`
		Live   bool   `json:"live"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v (%s)", err, h.stdout.String())
	}
	if report.Schema == "" || report.Live || len(report.Checks) == 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if n := len(h.ctrl.Requests()); n != 0 {
		t.Fatalf("offline doctor made %d control-plane requests", n)
	}
	if strings.Contains(h.stdout.String(), "[colab]") {
		t.Fatalf("diagnostics leaked into stdout: %s", h.stdout.String())
	}
}

// TestDoctorCommandKillSwitchExitTwo maps a blocked live probe to usage exit
// code 2 and still makes zero requests.
func TestDoctorCommandKillSwitchExitTwo(t *testing.T) {
	t.Setenv("LIVE_E2E_ENABLED", "false")
	t.Setenv("GOLAB_LIVE_PROBES", "1")
	h := newHarness(t)
	if code := runNewCommand(t, h, "doctor", "--live"); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", code, h.stderr.String())
	}
	if n := len(h.ctrl.Requests()); n != 0 {
		t.Fatalf("blocked live doctor made %d requests", n)
	}
	if !strings.Contains(h.stderr.String(), "kill switch") {
		t.Fatalf("no kill-switch hint: %s", h.stderr.String())
	}
}
