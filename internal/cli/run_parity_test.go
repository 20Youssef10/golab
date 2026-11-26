package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/history"
	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

func writeRunParityScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script.py")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// runHistory reads the single JSONL history file created by a `run` command
// (run generates an ephemeral session name).
func runHistory(t *testing.T, dir string) string {
	t.Helper()
	names, err := history.ListSessions(dir)
	if err != nil || len(names) != 1 {
		t.Fatalf("history sessions = %v, err = %v", names, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, names[0]+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// `run --json` must use the SAME cell-record contract as `exec --json`, while
// keeping run's shebang/argv semantics.
func TestRunJSONRecords(t *testing.T) {
	h := newHarness(t)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(fakekernel.Stream("stdout", "hello\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))

	script := writeRunParityScript(t, "#!/usr/bin/env python3\nprint('hello')\n")
	if code := h.run(t, "", "run", "--json", script); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	records := jsonRecords(t, h.stdout.String())
	if len(records) != 1 {
		t.Fatalf("records = %d: %q", len(records), h.stdout.String())
	}
	if records[0]["status"] != "ok" || records[0]["stdout"] != "hello\n" {
		t.Fatalf("record = %v", records[0])
	}
	execs := h.kernel.Executions()
	if len(execs) != 2 {
		t.Fatalf("executions = %d, want 2", len(execs))
	}
	if strings.Contains(execs[1].Code, "#!/usr/bin/env python3") {
		t.Fatalf("shebang not stripped: %q", execs[1].Code)
	}
	if !strings.Contains(execs[1].Code, "sys.argv = [") {
		t.Fatalf("argv not injected: %q", execs[1].Code)
	}
}

// A --secret value must reach the kernel payload but no output or history
// surface, exactly as with exec.
func TestRunSecretInjectedAndRedacted(t *testing.T) {
	const secret = "planted-run-secret-7777"
	t.Setenv("GOLAB_TEST_RUN_SECRET", secret)
	h := newHarness(t)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(
		fakekernel.Stream("stdout", "echo:"+secret+"\n"),
		fakekernel.Stream("stderr", "warn:"+secret+"\n"),
		fakekernel.DisplayData(map[string]any{"text/plain": secret}, nil, ""),
		fakekernel.Error("ValueError", secret, []string{"ValueError: " + secret + "\n"}),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	script := writeRunParityScript(t, "print('x')\n")

	if code := h.run(t, "", "run", "--json", "--secret", "GOLAB_TEST_RUN_SECRET", script); code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	execs := h.kernel.Executions()
	if len(execs) < 2 || !strings.Contains(execs[1].Code, pyRepr(secret)) {
		t.Fatalf("secret not injected into kernel payload: %+v", execs)
	}
	assertNoSecret(t, secret, h.stdout.String(), h.stderr.String(), runHistory(t, h.dir))
}

// --env-file is an owner-only secrets bridge; every value is registered with
// the redactor regardless of how innocuous the key looks.
func TestRunEnvFileInjectedAndRedacted(t *testing.T) {
	const secret = "SUPERSECRETRUNVALUE_9642"
	envPath := filepath.Join(t.TempDir(), "vars.env")
	if err := os.WriteFile(envPath, []byte("E2E_KEY="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(
		fakekernel.Stream("stdout", "echo:"+secret+"\n"),
		fakekernel.Error("ValueError", secret, []string{"ValueError: " + secret + "\n"}),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	script := writeRunParityScript(t, "print('x')\n")

	if code := h.run(t, "", "run", "--json", "--env-file", envPath, script); code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	execs := h.kernel.Executions()
	if len(execs) < 2 || !strings.Contains(execs[1].Code, pyRepr(secret)) {
		t.Fatalf("env-file value not injected: %+v", execs)
	}
	assertNoSecret(t, secret, h.stdout.String(), h.stderr.String(), runHistory(t, h.dir))
}

func TestRunOutputImageSaved(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	for _, jsonMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonMode], func(t *testing.T) {
			h := newHarness(t)
			h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
			h.kernel.Script(
				fakekernel.Stream("stdout", "plotting\n"),
				fakekernel.DisplayData(map[string]any{"image/png": png, "text/plain": "<Figure>"}, nil, ""),
				fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
			)
			target := filepath.Join(t.TempDir(), "plot.png")
			script := writeRunParityScript(t, "plot()\n")
			args := []string{"run", "--output-image", target, script}
			if jsonMode {
				args = append([]string{"run", "--json", "--output-image", target}, script)
			}
			if code := h.run(t, "", args...); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
			}
			if b, err := os.ReadFile(target); err != nil || string(b) != "png-bytes" {
				t.Fatalf("saved image = %q, err = %v", b, err)
			}
			if jsonMode {
				records := jsonRecords(t, h.stdout.String())
				if len(records) != 1 {
					t.Fatalf("records = %d: %q", len(records), h.stdout.String())
				}
				images, ok := records[0]["images"].([]any)
				if !ok || len(images) != 1 || images[0] != target {
					t.Fatalf("images = %v (want [%s])", records[0]["images"], target)
				}
			} else if !strings.Contains(h.stdout.String(), target) {
				t.Fatalf("stdout lacks saved path: %q", h.stdout.String())
			}
		})
	}
}

// Flags are resolved before any allocation: a bad --secret or --env-file must
// never reach the kernel.
func TestRunParityFlagsValidateBeforeAllocation(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--secret", "bad-name"},
		{"run", "--env-file", filepath.Join(t.TempDir(), "missing.env")},
	} {
		h := newHarness(t)
		script := writeRunParityScript(t, "print('never')\n")
		args := append(args, script)
		if code := h.run(t, "", args...); code != 2 {
			t.Fatalf("%v exit=%d, want 2; stderr=%q", args, code, h.stderr.String())
		}
		if len(h.kernel.Executions()) != 0 {
			t.Fatalf("%v submitted a cell despite a flag error: %+v", args, h.kernel.Executions())
		}
	}
}

// --json and injected values must not change notebook semantics: one cell per
// record, shared kernel, --env re-applied per cell.
func TestRunJSONWithNotebook(t *testing.T) {
	h := newHarness(t)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(fakekernel.Stream("stdout", "first\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(fakekernel.Stream("stdout", "second\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))

	c1 := nb.NewCodeCell("print('a')")
	c1.ID = "cell-a"
	c2 := nb.NewCodeCell("print('b')")
	c2.ID = "cell-b"
	path, _ := writeRunNotebook(t, ".ipynb", c1, c2)
	if code := h.run(t, "", "run", "--json", "--env", "A=1", path); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	records := jsonRecords(t, h.stdout.String())
	if len(records) != 2 {
		t.Fatalf("records = %d: %q", len(records), h.stdout.String())
	}
	execs := h.kernel.Executions()
	if len(execs) != 3 {
		t.Fatalf("executions = %d, want 3", len(execs))
	}
	for _, e := range execs[1:] {
		if !strings.Contains(e.Code, `os.environ["A"] = "1"`) {
			t.Fatalf("--env not re-applied: %q", e.Code)
		}
	}
}
