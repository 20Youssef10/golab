package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
	"github.com/20Youssef10/golab/internal/vmstat"
)

// vmEnvelopeStream is a kernel stdout stream carrying a vmstat JSON envelope.
func vmEnvelopeStream(obj any) fakekernel.Step {
	b, _ := json.Marshal(obj)
	return fakekernel.Stream("stdout", vmstat.JSONBegin+"\n"+string(b)+"\n"+vmstat.JSONEnd+"\n")
}

// runCtx is h.run with a caller-supplied context, for bounded `tail -f` tests.
func (h *testHarness) runCtx(ctx context.Context, stdin string, args ...string) int {
	h.stdout.Reset()
	h.stderr.Reset()
	ctx = WithDeps(ctx, h.deps)
	return execute(ctx, args, strings.NewReader(stdin), &h.stdout, &h.stderr)
}

func sampleSnapshot() vmstat.Snapshot {
	return vmstat.Snapshot{
		Processes: []vmstat.Process{
			{PID: "12", PCPU: "3.1", PMEM: "0.5", ETime: "01:10", Cmd: "python train.py"},
			{PID: "20", PCPU: "0.2", PMEM: "0.1", ETime: "00:09", Cmd: "bash"},
		},
		GPU:    vmstat.GPUInfo{Available: true, Text: "0, Tesla T4, 7 %, 200 MiB / 15360 MiB"},
		Memory: "             total        used        free\nMem:          15936        4096        3000",
		Disk:   "Filesystem      Size  Used Avail Use% Mounted on\n/dev/root       20G   12G  8.0G  60% /content",
	}
}

// Ports the Tier-2 ps contract: ONE bounded kernel execution collects all four
// sections, sanitized and redacted, with a structured --json mode.
func TestPSCollectsSnapshotInOneExecution(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK()) // /content prelude
	h.kernel.Script(vmEnvelopeStream(sampleSnapshot()), statusIdle(), replyOK())

	if code := h.run(t, "", "ps", "-s", "s1"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	out := h.stdout.String()
	for _, want := range []string{"python train.py", "Tesla T4", "Mem:", "/content", "PID"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ps stdout missing %q: %q", want, out)
		}
	}
	execs := h.kernel.Executions()
	if len(execs) != 2 { // prelude + exactly one collection
		t.Fatalf("executions = %d", len(execs))
	}
	code := execs[1].Code
	for _, want := range []string{"nvidia-smi", "free", "df", "ps", "subprocess.run"} {
		if !strings.Contains(code, want) {
			t.Fatalf("collection code missing %q: %s", want, code)
		}
	}
}

func TestPSJSONIsStructuredAndClean(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK())
	snap := sampleSnapshot()
	// Inject a token-shaped string the shared redaction path recognises.
	snap.Processes[0].Cmd = "python Bearer abcdef123456"
	h.kernel.Script(vmEnvelopeStream(snap), statusIdle(), replyOK())

	if code := h.run(t, "", "ps", "-s", "s1", "--json"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	raw := h.stdout.String()
	if strings.Contains(raw, "abcdef123456") {
		t.Fatalf("ps --json leaked a token: %q", raw)
	}
	var got vmstat.Snapshot
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("ps --json is not one JSON document: %v (%q)", err, raw)
	}
	if len(got.Processes) != 2 || !got.GPU.Available {
		t.Fatalf("bad json snapshot: %+v", got)
	}
}

func TestPSErrorPath(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK())
	h.kernel.Script(fakekernel.Error("RuntimeError", "collection failed", []string{"boom"}), statusIdle(), replyOK())
	code := h.run(t, "", "ps", "-s", "s1")
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", h.stdout.String(), h.stderr.String())
	}
}

func TestPSTopAlias(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK())
	h.kernel.Script(vmEnvelopeStream(sampleSnapshot()), statusIdle(), replyOK())
	if code := h.run(t, "", "top", "-s", "s1"); code != 0 {
		t.Fatalf("top alias exit=%d stderr=%q", code, h.stderr.String())
	}
}

// Ports the Tier-2 cat contract: raw bytes reach stdout untouched. No added
// trailing newline, NUL bytes preserved, pipe-safe.
func TestCatIsBinarySafe(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	// NUL + non-ASCII + no trailing newline. The fake Contents API encodes
	// content as a JSON string, so the payload must be valid UTF-8 here (the
	// real base64 path is covered by internal/contents tests).
	payload := "\x00\x01binary-é-no-newline"
	fc.addFile("blob.bin", payload)
	if code := h.run(t, "", "cat", "-s", "s1", "blob.bin"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if h.stdout.String() != payload {
		t.Fatalf("cat mangled bytes: got %q want %q", h.stdout.String(), payload)
	}
}

func TestCatNoDuplicateNewline(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	fc.addFile("lines.txt", "a\nb\n")
	if code := h.run(t, "", "cat", "-s", "s1", "lines.txt"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if h.stdout.String() != "a\nb\n" {
		t.Fatalf("cat added a newline: %q", h.stdout.String())
	}
}

func TestCatNotFound(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	code := h.run(t, "", "cat", "-s", "s1", "missing.txt")
	if code != 1 || !strings.Contains(h.stderr.String(), "not found") {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
}

// Ports the Tier-2 tail contract: one cheap kernel execution per poll, with a
// bounded -f loop under context cancellation.
func TestTailNonFollowAndRedaction(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK()) // prelude
	h.kernel.Script(vmEnvelopeStream(vmstat.TailResult{Offset: 6, Chunk: "Bearer abcdef123456\n", Exists: true}), statusIdle(), replyOK())
	if code := h.run(t, "", "tail", "-s", "s1", "app.log", "-n", "5"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.Contains(out, "Bearer REDACTED") || strings.Contains(out, "abcdef123456") {
		t.Fatalf("tail redaction failed: %q", out)
	}
	execs := h.kernel.Executions()
	if len(execs) != 2 {
		t.Fatalf("executions = %d", len(execs))
	}
	if !strings.Contains(execs[1].Code, `"tail"`) {
		t.Fatalf("tail did not use kernel tail: %s", execs[1].Code)
	}
}

func TestTailFollowBoundedByContext(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK()) // prelude
	h.kernel.Script(vmEnvelopeStream(vmstat.TailResult{Offset: 7, Chunk: "line-1\n", Exists: true}), statusIdle(), replyOK())
	// Queue plenty of follow polls so the ticker never runs dry, then bound the
	// whole run with a generous context deadline (a normal `Ctrl-C` path).
	for i := 2; i <= 40; i++ {
		h.kernel.Script(vmEnvelopeStream(vmstat.TailResult{Offset: int64(i * 7), Chunk: "line\n", Exists: true}), statusIdle(), replyOK())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	code := h.runCtx(ctx, "", "tail", "-s", "s1", "app.log", "-f", "--interval", "50ms")
	if code != 0 {
		t.Fatalf("follow exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "line-1") {
		t.Fatalf("follow did not emit initial chunk: stdout=%q stderr=%q execs=%d",
			h.stdout.String(), h.stderr.String(), len(h.kernel.Executions()))
	}
	if len(h.kernel.Executions()) > 42 { // prelude + initial + at most 40 polls
		t.Fatalf("follow polled too many times: %d", len(h.kernel.Executions()))
	}
}

// Ports the Tier-2 grep contract: pattern is JSON data, shell-quoted remotely,
// output redacted and match count bounded.
func TestGrepHappyPathAndBound(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK())
	h.kernel.Script(vmEnvelopeStream(vmstat.TextResult{Matches: "/content/a.py:1:Bearer abcdef123456\n"}), statusIdle(), replyOK())
	code := h.run(t, "", "grep", "-s", "s1", "token", "/content", "-i", "--glob", "*.py", "--max-matches", "7")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.Contains(out, "REDACTED") || strings.Contains(out, "abcdef123456") {
		t.Fatalf("grep redaction failed: %q", out)
	}
	execs := h.kernel.Executions()
	snippet := execs[1].Code
	for _, want := range []string{"json.loads", "shlex.quote", "head -n", "grep"} {
		if !strings.Contains(snippet, want) {
			t.Fatalf("grep snippet missing %q: %s", want, snippet)
		}
	}
}

func TestGrepPatternIsJSONDataNotSource(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK())
	h.kernel.Script(vmEnvelopeStream(vmstat.TextResult{Matches: ""}), statusIdle(), replyOK())
	evil := "x'; import os; os.system('touch /tmp/pwn'); y='"
	code := h.run(t, "", "grep", "-s", "s1", evil, "/content")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	snippet := h.kernel.Executions()[1].Code
	if strings.Contains(snippet, "\nos.system(") || strings.Contains(snippet, "\nimport os;") {
		t.Fatalf("grep spliced the pattern as code: %s", snippet)
	}
}

// Ports the Tier-2 du/find contracts: bounded, sanitized listings.
func TestDuAndFind(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		obj        any
		want       string
		boundToken string
	}{
		{"du", []string{"du", "-s", "s1", "/content"}, vmstat.LinesResult{Lines: "4.0K\t/content/a\n"}, "/content/a", "[:mx]"},
		{"find", []string{"find", "-s", "s1", "*.log"}, vmstat.LinesResult{Lines: "/content/x.log\n"}, "/content/x.log", "head -n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.putSession(t, "s1")
			h.kernel.Script(statusIdle(), replyOK())
			h.kernel.Script(vmEnvelopeStream(tc.obj), statusIdle(), replyOK())
			if code := h.run(t, "", tc.args...); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
			}
			if !strings.Contains(h.stdout.String(), tc.want) {
				t.Fatalf("stdout missing %q: %q", tc.want, h.stdout.String())
			}
			snippet := h.kernel.Executions()[1].Code
			if !strings.Contains(snippet, tc.boundToken) || !strings.Contains(snippet, "json.loads") {
				t.Fatalf("%s snippet is not bounded/data-driven: %s", tc.name, snippet)
			}
		})
	}
}

// Ports the Tier-2 fetch contract: validate URL before execution, wget with a
// curl fallback, optional SHA-256 verification, progress on stderr.
func TestFetchHappyPathAndSHA(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK())
	h.kernel.Script(vmEnvelopeStream(vmstat.FetchResult{OK: true, Dest: "/content/data.bin", SHA256: strings.Repeat("a", 64)}), statusIdle(), replyOK())
	code := h.run(t, "", "fetch", "-s", "s1", "https://example.com/data.bin", "/content/data.bin", "--sha256", strings.Repeat("a", 64))
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "Fetching") {
		t.Fatalf("no progress on stderr: %q", h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "/content/data.bin") {
		t.Fatalf("no destination on stdout: %q", h.stdout.String())
	}
	snippet := h.kernel.Executions()[1].Code
	for _, want := range []string{"wget", "curl", "hashlib.sha256", "json.loads"} {
		if !strings.Contains(snippet, want) {
			t.Fatalf("fetch snippet missing %q: %s", want, snippet)
		}
	}
}

func TestFetchRejectsBadURLBeforeAnyExecution(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	code := h.run(t, "", "fetch", "-s", "s1", "ftp://example.com/x", "/content/x")
	if code != 2 && code != 1 {
		t.Fatalf("bad URL exit=%d stderr=%q", code, h.stderr.String())
	}
	if len(h.kernel.Executions()) != 0 {
		t.Fatalf("bad URL reached the kernel: %d executions", len(h.kernel.Executions()))
	}
}

func TestFetchReportsSHAMismatch(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK())
	h.kernel.Script(vmEnvelopeStream(vmstat.FetchResult{OK: false, Dest: "/content/x", Error: "sha256 mismatch"}), statusIdle(), replyOK())
	code := h.run(t, "", "fetch", "-s", "s1", "https://example.com/x", "/content/x", "--sha256", strings.Repeat("b", 64))
	if code == 0 {
		t.Fatalf("expected failure for sha mismatch; stdout=%q stderr=%q", h.stdout.String(), h.stderr.String())
	}
}
