package cli

// The compat command must be honest: it reports deltas against a self-snapshot
// and never contacts a release server or the upstream CLI.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
