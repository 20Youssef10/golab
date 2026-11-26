package cli

// `golab trace -- <cmd>` is the thin CLI wrapper around internal/trace. The
// test drives an injected fake control plane so no network or billable work
// happens, and proves the wrapper records without leaking a token canary.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runTraceCommand(t *testing.T, h *testHarness, args ...string) int {
	t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	root := NewRootCommand(strings.NewReader(""), &h.stdout, &h.stderr)
	addIfMissing(root, "trace", RegisterTraceCommands)
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

func TestTraceCommandRecordsWrappedSubcommand(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.dir, "wire.ndjson")
	code := runTraceCommand(t, h, "trace", "--output", path, "--", "sessions")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, h.stderr.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no trace file: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, "/tun/m/assignments") {
		t.Fatalf("subcommand request missing from trace: %s", s)
	}
	if strings.Contains(s, "CANARY") || strings.Contains(s, "Bearer ") {
		t.Fatalf("credential-shaped text leaked: %s", s)
	}
	if !strings.Contains(s, `"query_keys":["authuser"]`) {
		t.Fatalf("query keys not recorded: %s", s)
	}
}

func TestTraceCommandRequiresSubcommand(t *testing.T) {
	h := newHarness(t)
	if code := runTraceCommand(t, h, "trace"); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", code, h.stderr.String())
	}
}
