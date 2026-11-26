package envlock

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Ports the Tier-4 requirement that `golab env freeze` emits a deterministic,
// secret-free lockfile: the pip freeze list is sorted/deduplicated and the
// JSON has a fixed field order and trailing newline.
func TestMarshalIsDeterministicAndSorted(t *testing.T) {
	in := &Lockfile{
		Schema:       Schema,
		Python:       "3.11.9",
		Requirements: []string{"zope==2.0", "numpy==1.26.0", "numpy==1.26.0", "torch==2.1.0"},
		CUDA:         "12.1",
		GPU:          "Tesla T4",
		Accelerator:  "T4",
	}
	a, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("marshal is not deterministic:\n%s\n%s", a, b)
	}
	text := string(a)
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("lockfile lacks trailing newline: %q", text)
	}
	for _, field := range []string{`"schema"`, `"python_version"`, `"pip_freeze"`, `"cuda"`, `"gpu"`, `"accelerator"`} {
		if !strings.Contains(text, field) {
			t.Fatalf("lockfile missing %s: %s", field, text)
		}
	}
	iN, iZ := strings.Index(text, "numpy"), strings.Index(text, "zope")
	if iN < 0 || iZ < 0 || iN > iZ {
		t.Fatalf("requirements not sorted: %s", text)
	}
	if strings.Count(text, "numpy==1.26.0") != 1 {
		t.Fatalf("duplicate requirement survived: %s", text)
	}
	parsed, err := Parse(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Requirements) != 3 || parsed.Requirements[0] != "numpy==1.26.0" {
		t.Fatalf("round trip requirements = %v", parsed.Requirements)
	}
}

// The probe output is untrusted kernel text: only the marker-delimited JSON
// may be consumed, credential-bearing URLs must be scrubbed, and a marker
// embedded in unrelated chatter must not turn into extra lockfile content.
func TestParseProbeScrubsSecretsAndIgnoresChatter(t *testing.T) {
	payload := map[string]any{
		"python_version": "3.11.9",
		"pip_freeze": []string{
			"zope==2.0",
			"private @ git+https://user:planted-secret-xyz@git.example.com/repo.git",
			"numpy==1.26.0",
		},
		"cuda":        "12.1",
		"gpu":         "Tesla T4",
		"accelerator": "T4",
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	out := "some kernel chatter with " + Marker + "not-json\n" +
		Marker + string(b) + "\ntrailing line\n"
	got, err := ParseProbe(out)
	if err != nil {
		t.Fatal(err)
	}
	if got.Python != "3.11.9" || got.CUDA != "12.1" || got.GPU != "Tesla T4" {
		t.Fatalf("probe = %+v", got)
	}
	if strings.Contains(strings.Join(got.Requirements, "\n"), "planted-secret-xyz") {
		t.Fatalf("credential leaked into requirements: %v", got.Requirements)
	}
	if !strings.Contains(strings.Join(got.Requirements, "\n"), "REDACTED@git.example.com") {
		t.Fatalf("credential URL was not scrubbed: %v", got.Requirements)
	}
	if got.Requirements[0] != "numpy==1.26.0" {
		t.Fatalf("requirements not sorted: %v", got.Requirements)
	}
	if _, err := ParseProbe("no marker here\n"); err == nil {
		t.Fatal("ParseProbe accepted output without a marker")
	}
}

// A frozen lockfile is only reusable on a matching interpreter and accelerator.
// Requirement *contents* are installed by --lockfile, so they are deliberately
// not compared here; a mismatch must name the field and both values.
func TestCompareReportsMismatchWithoutEchoingRequirements(t *testing.T) {
	locked := &Lockfile{
		Schema: Schema, Python: "3.11.9", Requirements: []string{"numpy==1.26.0"},
		CUDA: "12.1", GPU: "Tesla T4", Accelerator: "T4",
	}
	current := &Lockfile{
		Schema: Schema, Python: "3.10.12", Requirements: []string{"numpy==2.0.0"},
		CUDA: "12.1", GPU: "Tesla T4", Accelerator: "T4",
	}
	err := Compare(current, locked)
	if err == nil {
		t.Fatal("Compare accepted a different Python version")
	}
	msg := err.Error()
	if !strings.Contains(msg, "python_version") || !strings.Contains(msg, "3.11.9") ||
		!strings.Contains(msg, "3.10.12") {
		t.Fatalf("mismatch message is not actionable: %q", msg)
	}
	if strings.Contains(msg, "numpy") {
		t.Fatalf("mismatch echoed requirement contents: %q", msg)
	}
	if err := Compare(locked, locked); err != nil {
		t.Fatalf("Compare on identical environments: %v", err)
	}
	current.Python, current.GPU = locked.Python, "A100"
	if err := Compare(current, locked); err == nil || !strings.Contains(err.Error(), "gpu") {
		t.Fatalf("GPU mismatch not reported: %v", err)
	}
}

func TestRequirementsBytesAndProbeCode(t *testing.T) {
	reqs := RequirementsBytes(&Lockfile{Requirements: []string{"b==2", "a==1"}})
	if string(reqs) != "a==1\nb==2\n" {
		t.Fatalf("requirements bytes = %q", reqs)
	}
	code := ProbeCode()
	for _, want := range []string{Marker, "json.dumps", "sort_keys=True", "pip", "freeze"} {
		if !strings.Contains(code, want) {
			t.Fatalf("probe code missing %q: %s", want, code)
		}
	}
	if strings.Contains(code, "os.system") || strings.Contains(code, "eval(") {
		t.Fatalf("probe code executes untrusted input: %s", code)
	}
}
