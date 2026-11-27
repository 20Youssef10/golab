package cli

import (
	"context"
	"encoding/json"
	"fmt"
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

// ADR-001 follow-ups: TTL is enforced by a remote watchdog (no local daemon),
// jobs reports expiry, and --max-cu is a foreground-only flag.
func TestRunDetachWritesAbsoluteDeadlineAndRemoteWatchdog(t *testing.T) {
	h := newHarness(t)
	fixDetachSeams(t, h, "run-deadline")
	script := writeRunScript(t, "print('hello')\n")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	if code := h.run(t, "", "run", "--detach", "--ttl", "30m", "-s", "det1", script); code != 0 {
		t.Fatalf("run --detach exit=%d stderr=%s", code, h.stderr.String())
	}

	// The local index carries the absolute deadline too.
	raw, err := os.ReadFile(detach.ManifestPath(h.deps.HistoryDir))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Runs map[string]map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	dl, ok := manifest.Runs["run-deadline"]["deadline_at"].(string)
	if !ok || dl == "" {
		t.Fatalf("manifest lacks an absolute deadline: %s", raw)
	}
	parsed, err := time.Parse(time.RFC3339, dl)
	if err != nil {
		t.Fatalf("deadline_at %q is not RFC3339: %v", dl, err)
	}
	if parsed.Before(time.Now().Add(29*time.Minute)) || parsed.After(time.Now().Add(31*time.Minute)) {
		t.Fatalf("deadline_at %q does not match ttl=30m", dl)
	}

	execs := h.kernel.Executions()
	if len(execs) != 1 {
		t.Fatalf("kernel executions = %d, want 1", len(execs))
	}
	for _, want := range []string{"watchdog.py", "ttl-reason", "ttl_exceeded", "start_new_session=True"} {
		if !strings.Contains(execs[0].Code, want) {
			t.Fatalf("launcher cell lacks %q:\n%s", want, execs[0].Code)
		}
	}
}

func TestJobsReportsTTLExpiry(t *testing.T) {
	h := newHarness(t)
	fc := newFakeContents(t)
	h.putSession(t, "s1")
	h.attachContents(t, fc)
	start := time.Now().UTC().Add(-2 * time.Hour)
	deadline := start.Add(time.Hour)
	writeManifest(t, h, detach.Record{
		ID: "run-expired", Session: "s1", Script: "job.py", StartedAt: start, TTL: "1h",
	})
	base := "content/.golab/runs/run-expired"
	fc.addFile(base+"/meta.json", fmt.Sprintf(`{"id":"run-expired","ttl":"1h","deadline_at":%q}`, deadline.Format(time.RFC3339)))
	fc.addFile(base+"/exit", "124")
	fc.addFile(base+"/ttl-reason", "ttl_exceeded")

	if code := h.run(t, "", "jobs"); code != 0 {
		t.Fatalf("jobs exit=%d stderr=%s", code, h.stderr.String())
	}
	out := h.stdout.String()
	for _, want := range []string{"run-expired", "ttl=exceeded", "ttl_exceeded", "124"} {
		if !strings.Contains(out, want) {
			t.Fatalf("jobs output lacks %q: %s", want, out)
		}
	}
}

func TestJobsReportsTTLWithinDeadline(t *testing.T) {
	h := newHarness(t)
	fc := newFakeContents(t)
	h.putSession(t, "s1")
	h.attachContents(t, fc)
	now := time.Now().UTC()
	deadline := now.Add(time.Hour)
	writeManifest(t, h, detach.Record{
		ID: "run-live", Session: "s1", Script: "job.py", StartedAt: now, TTL: "1h",
	})
	base := "content/.golab/runs/run-live"
	fc.addFile(base+"/meta.json", fmt.Sprintf(`{"id":"run-live","ttl":"1h","deadline_at":%q}`, deadline.Format(time.RFC3339)))

	if code := h.run(t, "", "jobs"); code != 0 {
		t.Fatalf("jobs exit=%d stderr=%s", code, h.stderr.String())
	}
	out := h.stdout.String()
	for _, want := range []string{"run-live", "ttl=ok", "running"} {
		if !strings.Contains(out, want) {
			t.Fatalf("jobs output lacks %q: %s", want, out)
		}
	}
	if strings.Contains(out, "ttl=exceeded") {
		t.Fatalf("live run reported as TTL-expired: %s", out)
	}
}

func TestRunDetachRejectsMaxCUAndEnforce(t *testing.T) {
	// --max-cu samples CU in the foreground run loop; a detached CLI returns
	// before the remote child runs, so the flag must be rejected, not ignored.
	cases := []struct {
		name  string
		flags []string
	}{
		{"max-cu", []string{"--max-cu", "10"}},
		{"max-cu-zero", []string{"--max-cu", "0"}},
		{"enforce", []string{"--enforce"}},
		{"both", []string{"--max-cu", "10", "--enforce"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			fixDetachSeams(t, h, "run-cu")
			script := writeRunScript(t, "print('x')\n")
			args := append([]string{"run", "--detach", "--ttl", "1h", "-s", "det1"}, tc.flags...)
			args = append(args, script)
			if code := h.run(t, "", args...); code != 2 {
				t.Fatalf("exit=%d, want 2; stderr=%s", code, h.stderr.String())
			}
			if !strings.Contains(h.stderr.String(), "foreground runs only") {
				t.Fatalf("stderr must explain that the flag is foreground-only: %s", h.stderr.String())
			}
			if len(h.kernel.Executions()) != 0 {
				t.Fatalf("conflicting flags submitted %d cells", len(h.kernel.Executions()))
			}
			if _, err := h.deps.Store.Get("det1"); err == nil {
				t.Fatal("conflicting flags still allocated a VM")
			}
		})
	}
}

func TestStopReleasesVMAfterDetachedRun(t *testing.T) {
	h := newHarness(t)
	fixDetachSeams(t, h, "run-stop")
	script := writeRunScript(t, "print('x')\n")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	if code := h.run(t, "", "run", "--detach", "--ttl", "1h", "-s", "det1", script); code != 0 {
		t.Fatalf("detach exit=%d stderr=%s", code, h.stderr.String())
	}
	if code := h.run(t, "", "stop", "-s", "det1"); code != 0 {
		t.Fatalf("stop exit=%d stderr=%s", code, h.stderr.String())
	}
	if _, err := h.deps.Store.Get("det1"); err == nil {
		t.Fatal("stop did not release the detached session")
	}
	assignments, err := h.client.List(context.Background())
	if err != nil || len(assignments) != 0 {
		t.Fatalf("assignments = %v, %v", assignments, err)
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
