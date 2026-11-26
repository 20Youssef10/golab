package cli

// State migration must be explicit: reporting is the default, applying is
// opt-in, and a corrupt file aborts. No network is involved.

import (
	"context"
	"os"
	"strings"
	"testing"
)

func runStateCommand(t *testing.T, h *testHarness, args ...string) int {
	t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	root := NewRootCommand(strings.NewReader(""), &h.stdout, &h.stderr)
	addIfMissing(root, "state", RegisterStateCommands)
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

func TestStateMigrateCLI(t *testing.T) {
	h := newHarness(t)
	p := h.deps.Store.Path()
	legacy := `{"s1":{"name":"s1","endpoint":"m-s-1"}}`
	if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := runStateCommand(t, h, "state", "migrate", "--dry-run"); code != 0 {
		t.Fatalf("dry-run exit = %d, stderr = %s", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "dry-run") {
		t.Fatalf("no dry-run report: %s", h.stdout.String())
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != legacy {
		t.Fatalf("dry-run changed file: %s", raw)
	}

	if code := runStateCommand(t, h, "state", "migrate", "--json"); code != 0 {
		t.Fatalf("report exit = %d, stderr = %s", code, h.stderr.String())
	}
	raw, _ = os.ReadFile(p)
	if string(raw) != legacy {
		t.Fatalf("report changed file: %s", raw)
	}

	if code := runStateCommand(t, h, "state", "migrate", "--apply"); code != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", code, h.stderr.String())
	}
	raw, _ = os.ReadFile(p)
	if !strings.Contains(string(raw), `"schema_version": 1`) {
		t.Fatalf("apply did not stamp version: %s", raw)
	}
	if !strings.Contains(string(raw), "m-s-1") {
		t.Fatalf("apply lost the session: %s", raw)
	}
}
