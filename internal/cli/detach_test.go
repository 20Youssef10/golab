package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/detach"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

// Upstream analogue: docs/09 detached-run contract + ADR-001. Test-first:
// detach must write a local manifest, spawn NO local process, keep the VM
// allocated, and submit exactly one launcher cell built from safe payloads.

func fixDetachSeams(t *testing.T, h *testHarness, id string) {
	t.Helper()
	oldID, oldGuard := detachNewID, detachLocalLaunchGuard
	detachNewID = func() string { return id }
	detachLocalLaunchGuard = func(spawnedLocal bool) {
		if spawnedLocal {
			t.Fatal("detached run started a LOCAL process")
		}
	}
	t.Cleanup(func() { detachNewID, detachLocalLaunchGuard = oldID, oldGuard })
	_ = h
}

func writeRunScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "job.py")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunDetachWritesManifestAndNoLocalProcess(t *testing.T) {
	h := newHarness(t)
	fixDetachSeams(t, h, "run-abc123")
	script := writeRunScript(t, "print('hello')\n")
	// The launcher cell is short: it starts the remote child and returns.
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))

	code := h.run(t, "", "run", "--detach", "--ttl", "1h", "-s", "det1", script)
	if code != 0 {
		t.Fatalf("run --detach exit = %d; stderr=%s", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "run-abc123") {
		t.Fatalf("stdout lacks run id: %s", h.stdout.String())
	}
	execs := h.kernel.Executions()
	if len(execs) != 1 {
		t.Fatalf("kernel executions = %d, want 1", len(execs))
	}
	if !strings.Contains(execs[0].Code, "start_new_session=True") ||
		!strings.Contains(execs[0].Code, "meta.json") {
		t.Fatalf("launcher cell is not detached: %s", execs[0].Code)
	}

	// Local manifest exists and records the run; no credential/source leaks.
	m, err := detach.LoadManifest(detach.ManifestPath(h.deps.HistoryDir))
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := m.Runs["run-abc123"]
	if !ok || rec.Session != "det1" || rec.TTL != "1h" || rec.Script != script {
		t.Fatalf("manifest record = %+v (ok=%v)", rec, ok)
	}
	if s, err := h.deps.Store.Get("det1"); err != nil || s.Name != "det1" {
		t.Fatalf("detached run must keep the VM allocated: %+v / %v", s, err)
	}
}

func TestRunDetachRequiresPositiveTTL(t *testing.T) {
	h := newHarness(t)
	fixDetachSeams(t, h, "run-ttl")
	script := writeRunScript(t, "print('x')\n")

	// No --ttl at all: usage error, zero allocation.
	if code := h.run(t, "", "run", "--detach", "-s", "det1", script); code != 2 {
		t.Fatalf("run --detach without --ttl exit = %d, want 2; stderr=%s", code, h.stderr.String())
	}
	if len(h.kernel.Executions()) != 0 {
		t.Fatalf("run --detach without --ttl submitted %d cells", len(h.kernel.Executions()))
	}
	// An explicit zero/negative TTL is also refused.
	if code := h.run(t, "", "run", "--detach", "--ttl", "0s", "-s", "det1", script); code != 2 {
		t.Fatalf("run --detach --ttl 0s exit = %d, want 2; stderr=%s", code, h.stderr.String())
	}
	if len(h.kernel.Executions()) != 0 {
		t.Fatalf("run --detach --ttl 0s submitted %d cells", len(h.kernel.Executions()))
	}
}

func TestJobsPollsContentsAPIForStatusExitAndDuration(t *testing.T) {
	h := newHarness(t)
	fc := newFakeContents(t)
	h.putSession(t, "s1")
	h.attachContents(t, fc)
	writeManifest(t, h, detach.Record{
		ID: "run-abc123", Session: "s1", Script: "job.py",
		StartedAt: time.Now().UTC().Add(-2 * time.Minute), TTL: "1h",
	})
	base := "content/.golab/runs/run-abc123"
	fc.addFile(base+"/meta.json", `{"id":"run-abc123","session":"s1","script":"job.py","ttl":"1h"}`)
	fc.addFile(base+"/exit", "0")

	code := h.run(t, "", "jobs")
	if code != 0 {
		t.Fatalf("jobs exit = %d; stderr=%s", code, h.stderr.String())
	}
	out := h.stdout.String()
	for _, want := range []string{"run-abc123", "s1", "0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("jobs output lacks %q: %s", want, out)
		}
	}
	if !strings.Contains(out, "2m") {
		t.Fatalf("jobs output lacks duration: %s", out)
	}
}

func TestJobsSurvivesMissingRemoteFiles(t *testing.T) {
	h := newHarness(t)
	fc := newFakeContents(t)
	h.putSession(t, "s1")
	h.attachContents(t, fc)
	writeManifest(t, h, detach.Record{ID: "run-gone", Session: "s1", Script: "job.py", StartedAt: time.Now().UTC(), TTL: "1h"})

	code := h.run(t, "", "jobs")
	if code != 0 {
		t.Fatalf("jobs must not crash on missing remote files: exit=%d stderr=%s", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "run-gone") {
		t.Fatalf("jobs must still list the run: %s", h.stdout.String())
	}
}

func TestLogsReadsAndFollowsBounded(t *testing.T) {
	h := newHarness(t)
	fc := newFakeContents(t)
	h.putSession(t, "s1")
	h.attachContents(t, fc)
	writeManifest(t, h, detach.Record{ID: "run-abc123", Session: "s1", Script: "job.py", StartedAt: time.Now().UTC(), TTL: "1h"})
	base := "content/.golab/runs/run-abc123"
	fc.addFile(base+"/log", "hello from detached job\n")
	fc.addFile(base+"/exit", "0")

	code := h.run(t, "", "logs", "run-abc123")
	if code != 0 || !strings.Contains(h.stdout.String(), "hello from detached job") {
		t.Fatalf("logs exit=%d out=%s stderr=%s", code, h.stdout.String(), h.stderr.String())
	}
	// Follow must terminate because the exit marker is present, and must not
	// reprint the same bytes on every poll.
	h.stdout.Reset()
	start := time.Now()
	code = h.run(t, "", "logs", "run-abc123", "-f", "--interval", "1ms", "--max", "5")
	if code != 0 {
		t.Fatalf("logs -f exit=%d stderr=%s", code, h.stderr.String())
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("logs -f did not stop promptly")
	}
	if got := strings.Count(h.stdout.String(), "hello from detached job"); got != 1 {
		t.Fatalf("logs -f printed the log %d times, want 1: %s", got, h.stdout.String())
	}
}

func TestLogsRejectsUnknownRun(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "logs", "run-nope"); code != 1 {
		t.Fatalf("logs unknown id exit = %d, want 1; stderr=%s", code, h.stderr.String())
	}
}

func writeManifest(t *testing.T, h *testHarness, recs ...detach.Record) {
	t.Helper()
	p := detach.ManifestPath(h.deps.HistoryDir)
	m, err := detach.LoadManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if !detach.ValidID(r.ID) {
			t.Fatalf("invalid test id %q", r.ID)
		}
		m.Runs[r.ID] = r
	}
	if err := detach.SaveManifest(p, m); err != nil {
		t.Fatal(err)
	}
}
