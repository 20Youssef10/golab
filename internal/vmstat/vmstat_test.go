package vmstat

import (
	"encoding/json"
	"strings"
	"testing"
)

// Ports the Tier-2 "see inside the VM" contract: all user-controlled input
// travels to the kernel as JSON-decoded DATA (the install trick), never as
// spliced source. Every generated snippet must carry the JSON envelope markers
// so the CLI can extract structured output even when IPython emits warnings.

func TestBuildPSCodeCollectsAllSectionsInOneSnippet(t *testing.T) {
	code := BuildPSCode()
	for _, want := range []string{
		JSONBegin, JSONEnd,
		`"ps"`, "pid,pcpu,pmem,etime,cmd", "--sort=-pcpu",
		`"nvidia-smi"`, "--query-gpu=", "--format=csv,noheader",
		`"free", "-m"`, `"df", "-h", "/content"`,
		"subprocess.run", "timeout=",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("ps snippet missing %q:\n%s", want, code)
		}
	}
	// CPU/TPU must degrade gracefully: nvidia-smi failures are captured, not
	// fatal to the whole collection.
	if !strings.Contains(code, `"available": False`) {
		t.Fatalf("ps snippet has no CPU/TPU fallback:\n%s", code)
	}
}

func TestBuildTailCodeUsesByteOffsetAndBoundsOutput(t *testing.T) {
	code := BuildTailCode("/content/app.log", 10, 4096, false)
	for _, want := range []string{
		JSONBegin, "json.loads", `"tail", "-c"`, "offset + 1", "str(start)", "1048576",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("tail snippet missing %q:\n%s", want, code)
		}
	}
	// The offset is data, never spliced as source: only inside the JSON payload.
	if !strings.Contains(code, "json.loads(") {
		t.Fatalf("tail offset was not passed as JSON data:\n%s", code)
	}
	initial := BuildTailCode("/content/app.log", 10, 0, true)
	if !strings.Contains(initial, `"-n"`) || !strings.Contains(initial, `str(n)`) {
		t.Fatalf("initial tail should use tail -n N:\n%s", initial)
	}
}

func TestBuildGrepCodeDecodesPatternAsDataAndShellQuotes(t *testing.T) {
	spec := GrepSpec{Pattern: "x'; import os; os.system('touch /tmp/pwn'); y='", Path: "/content", IgnoreCase: true, Glob: "*.py", MaxMatches: 7}
	code := BuildGrepCode(spec)
	for _, want := range []string{
		JSONBegin, "json.loads", "shlex.quote", `"grep"`, `"--"`, "head -n ", "shell=True",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("grep snippet missing %q:\n%s", want, code)
		}
	}
	// The dangerous pattern may only appear inside the JSON string literal,
	// never as a bare Python statement.
	if strings.Contains(code, "\nos.system(") || strings.Contains(code, "\nimport os;") {
		t.Fatalf("user input was spliced as code:\n%s", code)
	}
	if !strings.Contains(code, `p["ignore_case"]`) || !strings.Contains(code, `p["max_matches"]`) ||
		!strings.Contains(code, `\"max_matches\":7`) {
		t.Fatalf("grep flags not encoded as data:\n%s", code)
	}
}

func TestBuildDuAndFindAreBounded(t *testing.T) {
	du := BuildDuCode("/content", 25)
	for _, want := range []string{JSONBegin, "json.loads", `"du", "-sh"`, "[:mx]", "os.listdir"} {
		if !strings.Contains(du, want) {
			t.Fatalf("du snippet missing %q:\n%s", want, du)
		}
	}
	find := BuildFindCode("/content", "*.log", 50)
	for _, want := range []string{JSONBegin, "json.loads", `"find"`, `"-maxdepth"`, "head -n ", "shlex.quote"} {
		if !strings.Contains(find, want) {
			t.Fatalf("find snippet missing %q:\n%s", want, find)
		}
	}
}

func TestBuildFetchCodeHasWgetCurlFallbackAndSHA(t *testing.T) {
	code := BuildFetchCode("https://example.com/data.bin", "/content/data.bin", strings.Repeat("a", 64), 600)
	for _, want := range []string{
		JSONBegin, "json.loads", `"wget", "-c"`, `"curl"`, "hashlib.sha256", "timeout=",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("fetch snippet missing %q:\n%s", want, code)
		}
	}
}

func TestValidateFetchURLRejectsNonHTTP(t *testing.T) {
	for _, bad := range []string{"", "ftp://example.com/x", "file:///etc/passwd", "http://", "://broken", "https://"} {
		if err := ValidateFetchURL(bad); err == nil {
			t.Fatalf("ValidateFetchURL(%q) unexpectedly accepted", bad)
		}
	}
	for _, good := range []string{"https://example.com/x", "http://127.0.0.1:8080/a?b=c"} {
		if err := ValidateFetchURL(good); err != nil {
			t.Fatalf("ValidateFetchURL(%q) rejected: %v", good, err)
		}
	}
}

func TestValidateSHA256(t *testing.T) {
	if err := ValidateSHA256(""); err != nil {
		t.Fatalf("empty sha should be allowed: %v", err)
	}
	if err := ValidateSHA256(strings.Repeat("a", 64)); err != nil {
		t.Fatalf("valid sha rejected: %v", err)
	}
	if err := ValidateSHA256(strings.Repeat("a", 63)); err == nil {
		t.Fatal("short sha accepted")
	}
	if err := ValidateSHA256(strings.Repeat("z", 64)); err == nil {
		t.Fatal("non-hex sha accepted")
	}
}

func envelope(obj any) string {
	b, _ := json.Marshal(obj)
	return JSONBegin + "\n" + string(b) + "\n" + JSONEnd
}

func TestParseSnapshotRoundTrip(t *testing.T) {
	out := envelope(Snapshot{
		Processes: []Process{{PID: "1", PCPU: "0.3", PMEM: "1.2", ETime: "01:02", Cmd: "python"}},
		GPU:       GPUInfo{Available: true, Text: "0, Tesla, 5%, 100MiB"},
		Memory:    "Mem: 1000 200", Disk: "Filesystem /content",
	})
	snap, err := ParseSnapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Processes) != 1 || snap.Processes[0].Cmd != "python" || !snap.GPU.Available || snap.Memory == "" || snap.Disk == "" {
		t.Fatalf("bad snapshot: %+v", snap)
	}
}

func TestParseSnapshotWithWarningsAroundEnvelope(t *testing.T) {
	out := "/usr/bin/python: warning\n" + envelope(Snapshot{GPU: GPUInfo{Available: false, Reason: "not found"}})
	snap, err := ParseSnapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	if snap.GPU.Available || snap.GPU.Reason != "not found" {
		t.Fatalf("bad gpu fallback: %+v", snap.GPU)
	}
}

func TestParseCommandsRejectMissingEnvelope(t *testing.T) {
	for _, parse := range []func(string) error{
		func(s string) error { _, e := ParseSnapshot(s); return e },
		func(s string) error { _, e := ParseTail(s); return e },
		func(s string) error { _, e := ParseLines(s); return e },
		func(s string) error { _, e := ParseMatches(s); return e },
		func(s string) error { _, e := ParseFetch(s); return e },
	} {
		if err := parse("no envelope here"); err == nil {
			t.Fatal("missing envelope accepted")
		}
	}
}

func TestParseTailAndFetch(t *testing.T) {
	tail, err := ParseTail(envelope(TailResult{Offset: 99, Chunk: "hello\n", Exists: true}))
	if err != nil || tail.Offset != 99 || tail.Chunk != "hello\n" || !tail.Exists {
		t.Fatalf("parse tail: %+v %v", tail, err)
	}
	fetch, err := ParseFetch(envelope(FetchResult{OK: true, Dest: "/content/x", SHA256: "abc"}))
	if err != nil || !fetch.OK || fetch.Dest != "/content/x" || fetch.SHA256 != "abc" {
		t.Fatalf("parse fetch: %+v %v", fetch, err)
	}
	lines, err := ParseLines(envelope(LinesResult{Lines: "4.0K\t/content/a\n"}))
	if err != nil || lines.Lines == "" {
		t.Fatalf("parse lines: %+v %v", lines, err)
	}
	matches, err := ParseMatches(envelope(TextResult{Matches: "/content/a.py:1:hit\n"}))
	if err != nil || matches.Matches == "" {
		t.Fatalf("parse matches: %+v %v", matches, err)
	}
}
