package cli

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

// Ports upstream tests/test_exec.py::test_cli_exec_file and
// test_cli_exec_outputs.
func TestExecFile(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(
		fakekernel.Stream("stdout", "hello from kernel\n"),
		fakekernel.DisplayData(map[string]any{"text/plain": "a display"}, nil, ""),
		fakekernel.Stream("stderr", "warn\n"),
		fakekernel.Status("idle"),
		fakekernel.Reply("ok", nil),
	)
	f := filepath.Join(t.TempDir(), "code.py")
	if err := os.WriteFile(f, []byte("print('hello')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run(t, "", "exec", "-s", "s1", "-f", f); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "hello from kernel") || !strings.Contains(h.stdout.String(), "a display") {
		t.Fatalf("stdout = %q", h.stdout.String())
	}
	if !strings.Contains(h.stderr.String(), "warn") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	s, _ := h.deps.Store.Get("s1")
	if s.Running != nil || s.LastExecution == nil || s.LastExecution.Kind != f {
		t.Fatalf("session = %+v", s)
	}
}

// Ports upstream tests/test_exec.py::test_cli_exec_env_injects_prelude,
// test_cli_exec_env_flags_accumulate_and_split_on_first_equals and
// test_cli_exec_env_escapes_tricky_literals.
func TestExecEnvPrelude(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	if code := h.run(t, "print('x')\n", "exec", "-s", "s1", "--env", "A=1", "--env", "B=a=b", "--env", "Q=it's"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	execs := h.kernel.Executions()
	if len(execs) != 2 {
		t.Fatalf("executions = %d", len(execs))
	}
	code := execs[1].Code
	for _, want := range []string{
		`import os`,
		`os.environ["A"] = "1"`,
		`os.environ["B"] = "a=b"`,
		`os.environ["Q"] = "it's"`,
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("code missing %q: %q", want, code)
		}
	}
}

// Ports upstream tests/test_exec.py::test_cli_exec_malformed_env_errors_before_session_resolution.
func TestExecInvalidEnvBeforeSession(t *testing.T) {
	for _, item := range []string{"BAD", "1BAD=x", "A-B=x"} {
		t.Run(item, func(t *testing.T) {
			h := newHarness(t)
			code := h.run(t, "print(1)\n", "exec", "--env", item)
			if code != 2 {
				t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
			}
			if !strings.Contains(h.stderr.String(), "Invalid --env") {
				t.Fatalf("stderr = %q", h.stderr.String())
			}
			if len(h.ctrl.Requests()) != 0 {
				t.Fatalf("made network calls: %+v", h.ctrl.Requests())
			}
		})
	}
}

// Ports upstream tests/test_exec.py::test_cli_exec_empty_code and
// test_cli_exec_no_input.
func TestExecEmptyCodeAndNoTTYInput(t *testing.T) {
	t.Run("empty piped code", func(t *testing.T) {
		h := newHarness(t)
		h.putSession(t, "s1")
		if code := h.run(t, "  \n", "exec", "-s", "s1"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if len(h.kernel.Executions()) != 0 {
			t.Fatalf("executions = %+v", h.kernel.Executions())
		}
	})
	t.Run("TTY without file", func(t *testing.T) {
		t.Setenv("COLAB_TEST_STDIN_TTY", "1")
		h := newHarness(t)
		h.putSession(t, "s1")
		code := h.run(t, "", "exec", "-s", "s1")
		if code != 1 || !strings.Contains(h.stderr.String(), "No input provided") {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
	})
}

// Ports upstream tests/test_exec.py::test_cli_exec_lost_session_prunes.
func TestExecLostSessionPrunes(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.SetUnauthorized(true)
	code := h.run(t, "print(1)\n", "exec", "-s", "s1")
	if code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "appears to be lost") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	if _, err := h.deps.Store.Get("s1"); err == nil {
		t.Fatal("session was not pruned")
	}
}

// Ports upstream tests/test_exec.py::test_cli_exec_timeout.
func TestExecTimeout(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.SetUnauthorized(false)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(fakekernel.Step{Delay: 500 * time.Millisecond, MsgType: "status", Content: map[string]any{"execution_state": "idle"}})
	code := h.run(t, "print(1)\n", "exec", "-s", "s1", "--timeout", "0.01")
	if code != 1 || !strings.Contains(h.stderr.String(), "Timeout") {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
}

// Ports upstream tests/test_ipynb_exec.py::test_exec_ipynb,
// test_exec_ipynb_output_format and test_exec_ipynb_creates_output_file.
func TestExecIPynb(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(fakekernel.Stream("stdout", "first\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(fakekernel.Stream("stdout", "second\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))

	n := nb.NewNotebook()
	md := nb.NewMarkdownCell("ignored")
	c1 := nb.NewCodeCell("# @title First cell\nprint('a')")
	c1.ID = "cell-id-1"
	c2 := nb.NewCodeCell("print('b')")
	c2.ID = "cell-id-2"
	n.Cells = []nb.Cell{md, c1, c2}
	raw, err := nb.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	notebookPath := filepath.Join(t.TempDir(), "demo.ipynb")
	if err := os.WriteFile(notebookPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if code := h.run(t, "", "exec", "-s", "s1", "-f", notebookPath); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "Executing cell 1/2 - First cell...") ||
		!strings.Contains(h.stderr.String(), "Executing cell 2/2 - cell-id-2...") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "first") || !strings.Contains(h.stdout.String(), "second") {
		t.Fatalf("stdout = %q", h.stdout.String())
	}
	outPath := strings.TrimSuffix(notebookPath, ".ipynb") + "_output.ipynb"
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := nb.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Cells) != 3 || len(saved.Cells[1].Outputs) != 1 || len(saved.Cells[2].Outputs) != 1 {
		t.Fatalf("saved notebook = %+v", saved)
	}
	if got := string(saved.Cells[1].Outputs[0].Text); got != "first\n" {
		t.Fatalf("cell output = %q", got)
	}
}

// Live v0.2.0 regression: the cell-level execution_count reported by the
// kernel's execute_reply (runtime.Result.ExecutionCount) was never copied onto
// nb.Cell, so every executed cell was saved with "execution_count": null.
// Ports the notebook fidelity assertions of upstream
// tests/test_ipynb_exec.py::test_exec_ipynb_output_format. Text and NDJSON
// output surfaces must stay byte-identical while the notebook gains counts.
func TestExecIPynbCellExecutionCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		json bool
	}{
		{name: "text"},
		{name: "ndjson", json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.putSession(t, "s1")
			h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", map[string]any{"execution_count": 1})) // /content prelude
			h.kernel.Script(
				fakekernel.Stream("stdout", "first\n"),
				fakekernel.Status("idle"),
				fakekernel.Reply("ok", map[string]any{"execution_count": 4}),
			)
			h.kernel.Script(
				fakekernel.Stream("stdout", "second\n"),
				fakekernel.Status("idle"),
				fakekernel.Reply("ok", map[string]any{"execution_count": 5}),
			)

			n := nb.NewNotebook()
			md := nb.NewMarkdownCell("notes")
			c1 := nb.NewCodeCell("print('a')")
			c1.ID = "cell-a"
			c2 := nb.NewCodeCell("print('b')")
			c2.ID = "cell-b"
			n.Cells = []nb.Cell{md, c1, c2}
			raw, err := nb.Marshal(n)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "counts.ipynb")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}

			args := []string{"exec", "-s", "s1", "-f", path}
			if tc.json {
				args = append(args, "--json")
			}
			if code := h.run(t, "", args...); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
			}

			// Output surfaces are unchanged by the notebook-fidelity fix.
			if tc.json {
				records := jsonRecords(t, h.stdout.String())
				if len(records) != 2 {
					t.Fatalf("records = %d: %q", len(records), h.stdout.String())
				}
				for i, want := range []struct {
					id, stdout string
				}{{"cell-a", "first\n"}, {"cell-b", "second\n"}} {
					r := records[i]
					if r["cell_id"] != want.id || r["stdout"] != want.stdout || r["status"] != "ok" {
						t.Fatalf("record %d = %v", i, r)
					}
				}
			} else if !strings.Contains(h.stdout.String(), "first") || !strings.Contains(h.stdout.String(), "second") {
				t.Fatalf("stdout = %q", h.stdout.String())
			}

			outPath := strings.TrimSuffix(path, ".ipynb") + "_output.ipynb"
			b, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := nb.Parse(b)
			if err != nil {
				t.Fatal(err)
			}
			if len(saved.Cells) != 3 {
				t.Fatalf("saved cells = %d", len(saved.Cells))
			}
			if saved.Cells[0].ExecutionCount != nil {
				t.Fatalf("markdown execution_count = %v, want nil", *saved.Cells[0].ExecutionCount)
			}
			for i, want := range []int{4, 5} {
				got := saved.Cells[i+1].ExecutionCount
				if got == nil {
					t.Fatalf("cell %d execution_count is null; raw notebook = %s", i+1, b)
				}
				if *got != want {
					t.Fatalf("cell %d execution_count = %d, want %d", i+1, *got, want)
				}
			}
			if *saved.Cells[1].ExecutionCount >= *saved.Cells[2].ExecutionCount {
				t.Fatalf("execution counts not increasing: %d, %d",
					*saved.Cells[1].ExecutionCount, *saved.Cells[2].ExecutionCount)
			}
			for i, want := range []string{"first\n", "second\n"} {
				if len(saved.Cells[i+1].Outputs) != 1 || string(saved.Cells[i+1].Outputs[0].Text) != want {
					t.Fatalf("cell %d outputs = %+v", i+1, saved.Cells[i+1].Outputs)
				}
			}
			if !strings.Contains(string(b), `"execution_count":4`) || !strings.Contains(string(b), `"execution_count":5`) {
				t.Fatalf("saved notebook JSON lacks reply counts: %s", b)
			}
		})
	}
}

// Ports upstream tests/test_exec.py::test_cli_exec_file_error_exits_nonzero,
// test_cli_exec_systemexit_zero_exits_zero_silently,
// test_cli_exec_systemexit_nonzero_propagates_code and
// test_cli_exec_systemexit_string_prints_message_and_exits_one (PRs #278/#280).
// IPython keeps the kernel alive by reporting sys.exit as an error output, so
// exec must translate it back to CPython's process-exit semantics.
func TestExecExitSemantics(t *testing.T) {
	systemExit := func(evalue string) fakekernel.Step {
		return fakekernel.Error("SystemExit", evalue, []string{
			"An exception has occurred, use %tb to see the full traceback.\n",
			"SystemExit: " + evalue + "\n",
		})
	}
	quiet := []string{"An exception has occurred", "SystemExit:"}
	for _, tc := range []struct {
		name       string
		step       fakekernel.Step
		wantCode   int
		wantStderr string
		noStderr   []string
	}{
		{name: "success", step: fakekernel.Stream("stdout", "ok\n"), wantCode: 0},
		{
			name:       "error output",
			step:       fakekernel.Error("ValueError", "boom", []string{"Traceback...\n", "ValueError: boom\n"}),
			wantCode:   1,
			wantStderr: "ValueError: boom",
		},
		{name: "systemexit empty", step: systemExit(""), wantCode: 0, noStderr: quiet},
		{name: "systemexit None", step: systemExit("None"), wantCode: 0, noStderr: quiet},
		{name: "systemexit zero", step: systemExit("0"), wantCode: 0, noStderr: quiet},
		{name: "systemexit seven", step: systemExit("7"), wantCode: 7, noStderr: quiet},
		{
			name:       "systemexit message",
			step:       systemExit("fatal config error"),
			wantCode:   1,
			wantStderr: "fatal config error\n",
			noStderr:   quiet,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.putSession(t, "s1")
			h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
			h.kernel.Script(tc.step, fakekernel.Status("idle"), fakekernel.Reply("ok", nil))

			code := h.run(t, "raise SystemExit\n", "exec", "-s", "s1")
			if code != tc.wantCode {
				t.Fatalf("exit=%d want=%d stderr=%q stdout=%q", code, tc.wantCode, h.stderr.String(), h.stdout.String())
			}
			if tc.wantStderr != "" && !strings.Contains(h.stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr missing %q: %q", tc.wantStderr, h.stderr.String())
			}
			for _, unwanted := range tc.noStderr {
				if strings.Contains(h.stderr.String(), unwanted) {
					t.Fatalf("stderr should not contain %q: %q", unwanted, h.stderr.String())
				}
			}
		})
	}
}

// Live v0.2.0 regression: IPython reports sys.exit as an error output even for
// a successful exit. Text mode translates that back to CPython's UX, but the
// --json record builder treated every error output as a failed cell and leaked
// the IPython "%tb" banner. Both modes must now agree (PRs #278/#280).
func TestExecJSONSystemExitMatchesTextMode(t *testing.T) {
	systemExit := func(evalue string) fakekernel.Step {
		return fakekernel.Error("SystemExit", evalue, []string{
			"\x1b[0;31mAn exception has occurred, use %tb to see the full traceback.\x1b[0m\n",
			"SystemExit: " + evalue + "\n",
		})
	}
	const banner = "An exception has occurred, use %tb to see the full traceback."
	for _, tc := range []struct {
		name       string
		eval       string
		wantCode   int
		wantStatus string
		wantError  bool
		wantEValue string
		wantStderr string
	}{
		{name: "no value", eval: "", wantCode: 0, wantStatus: "ok"},
		{name: "None", eval: "None", wantCode: 0, wantStatus: "ok"},
		{name: "zero", eval: "0", wantCode: 0, wantStatus: "ok"},
		{name: "seven", eval: "7", wantCode: 7, wantStatus: "error", wantError: true, wantEValue: "7"},
		{
			name: "message", eval: "fatal config error", wantCode: 1, wantStatus: "error",
			wantError: true, wantEValue: "fatal config error", wantStderr: "fatal config error\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.putSession(t, "s1")
			h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
			h.kernel.Script(
				fakekernel.Stream("stdout", "exec-ok\n"),
				systemExit(tc.eval),
				fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
			)
			if code := h.run(t, "print('exec-ok')\n", "exec", "-s", "s1", "--json"); code != tc.wantCode {
				t.Fatalf("exit=%d want=%d stderr=%q stdout=%q", code, tc.wantCode, h.stderr.String(), h.stdout.String())
			}
			records := jsonRecords(t, h.stdout.String())
			if len(records) != 1 {
				t.Fatalf("records = %d: %q", len(records), h.stdout.String())
			}
			r := records[0]
			if r["status"] != tc.wantStatus {
				t.Fatalf("status = %v, want %q (record %v)", r["status"], tc.wantStatus, r)
			}
			if r["stdout"] != "exec-ok\n" {
				t.Fatalf("stdout field = %v, want %q", r["stdout"], "exec-ok\n")
			}
			if !tc.wantError {
				if r["error"] != nil {
					t.Fatalf("error must be null for a quiet SystemExit: %v", r["error"])
				}
			} else {
				errObj, ok := r["error"].(map[string]any)
				if !ok {
					t.Fatalf("error = %v", r["error"])
				}
				if errObj["ename"] != "SystemExit" || errObj["evalue"] != tc.wantEValue {
					t.Fatalf("error = %v, want ename=SystemExit evalue=%q", errObj, tc.wantEValue)
				}
				if errObj["traceback"] != "" {
					t.Fatalf("traceback must be concise, got %q", errObj["traceback"])
				}
			}
			if r["stderr"] != tc.wantStderr {
				t.Fatalf("stderr field = %q, want %q", r["stderr"], tc.wantStderr)
			}
			if tc.wantStderr != "" && !strings.Contains(h.stderr.String(), tc.wantStderr) {
				t.Fatalf("diagnostic stderr = %q, want %q", h.stderr.String(), tc.wantStderr)
			}
			// No IPython "%tb" banner or ANSI escapes on either surface.
			for _, surface := range []string{h.stdout.String(), h.stderr.String()} {
				if strings.Contains(surface, banner) || strings.Contains(surface, "use %tb") || strings.Contains(surface, "\x1b") {
					t.Fatalf("IPython banner/ANSI leaked: %q", surface)
				}
			}
		})
	}
}

// Ports upstream tests/test_ipynb_exec.py::test_exec_ipynb_cell_error_stops_
// and_exits_nonzero (PR #278): the first failing cell ends execution but the
// deferred *_output.ipynb write still records every cell executed so far.
func TestExecIPynbStopsAtFailingCell(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(fakekernel.Stream("stdout", "first\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(
		fakekernel.Error("RuntimeError", "cell 1 failed", []string{"RuntimeError: cell 1 failed\n"}),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	// No script queued for a hypothetical third cell: executing it would use
	// the kernel's default idle/ok reply, so Executions() detects the overrun.

	n := nb.NewNotebook()
	c1 := nb.NewCodeCell("print('a')")
	c1.ID = "cell-1"
	c2 := nb.NewCodeCell("raise RuntimeError('cell 1 failed')")
	c2.ID = "cell-2"
	c3 := nb.NewCodeCell("print('never')")
	c3.ID = "cell-3"
	n.Cells = []nb.Cell{c1, c2, c3}
	raw, err := nb.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	notebookPath := filepath.Join(t.TempDir(), "broken.ipynb")
	if err := os.WriteFile(notebookPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if code := h.run(t, "", "exec", "-s", "s1", "-f", notebookPath); code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if execs := h.kernel.Executions(); len(execs) != 3 { // prelude + 2 cells, never the third
		t.Fatalf("executions = %d (want 3): %+v", len(execs), execs)
	}
	if !strings.Contains(h.stderr.String(), "RuntimeError: cell 1 failed") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}

	outPath := strings.TrimSuffix(notebookPath, ".ipynb") + "_output.ipynb"
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0666 for all files (NTFS ACLs, not Unix mode bits);
	// owner-only access there is enforced by the platform's ACL helper.
	if got := info.Mode().Perm(); goruntime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("output notebook mode = %o, want 600", got)
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := nb.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Cells) != 3 || len(saved.Cells[0].Outputs) != 1 ||
		len(saved.Cells[1].Outputs) != 1 || len(saved.Cells[2].Outputs) != 0 {
		t.Fatalf("saved notebook = %+v", saved)
	}
	if saved.Cells[1].Outputs[0].OutputType != "error" || saved.Cells[1].Outputs[0].Evalue != "cell 1 failed" {
		t.Fatalf("failing cell output = %+v", saved.Cells[1].Outputs[0])
	}
}

// Ports upstream tests/test_repl.py::test_cli_repl_piped and
// test_cli_repl_piped_empty.
func TestReplPiped(t *testing.T) {
	t.Run("piped", func(t *testing.T) {
		h := newHarness(t)
		h.putSession(t, "s1")
		h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
		h.kernel.Script(fakekernel.Stream("stdout", "hi\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
		if code := h.run(t, "print('hi')\n", "repl", "-s", "s1"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stdout.String(), "hi") {
			t.Fatalf("stdout = %q", h.stdout.String())
		}
		s, _ := h.deps.Store.Get("s1")
		if s.LastExecution == nil || s.LastExecution.Kind != "stdin" {
			t.Fatalf("session = %+v", s)
		}
	})
	t.Run("empty", func(t *testing.T) {
		h := newHarness(t)
		h.putSession(t, "s1")
		if code := h.run(t, " \n", "repl", "-s", "s1"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if len(h.kernel.Executions()) != 1 { // chdir only
			t.Fatalf("executions = %+v", h.kernel.Executions())
		}
	})
}

// Ports upstream tests/test_run.py::test_run_missing_script_errors_before_assign
// and test_run_malformed_env_errors_before_assign.
func TestRunValidatesBeforeAllocation(t *testing.T) {
	h := newHarness(t)
	code := h.run(t, "", "run", filepath.Join(t.TempDir(), "missing.py"))
	if code != 2 || !strings.Contains(h.stderr.String(), "Script not found") {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if len(h.ctrl.Requests()) != 0 {
		t.Fatalf("allocated VM despite missing script: %+v", h.ctrl.Requests())
	}

	h2 := newHarness(t)
	f := filepath.Join(t.TempDir(), "script.py")
	if err := os.WriteFile(f, []byte("print(1)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code = h2.run(t, "", "run", f, "--env", "BAD")
	if code != 2 || !strings.Contains(h2.stderr.String(), "Invalid --env") {
		t.Fatalf("exit=%d stderr=%q", code, h2.stderr.String())
	}
	if len(h2.ctrl.Requests()) != 0 {
		t.Fatalf("allocated VM despite bad env: %+v", h2.ctrl.Requests())
	}
}

// Ports upstream tests/test_run.py::test_run_basic_flow, test_run_passes_argv,
// test_run_propagates_error_exit_code, test_run_systemexit_nonzero_propagates_code
// and test_run_keep_skips_unassign.
func TestRunFlow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		args     []string
		wantCode int
		keep     bool
	}{
		{name: "basic", body: "print('hello')\n", wantCode: 0},
		{name: "argv", body: "import sys; print(sys.argv)\n", args: []string{"--flag", "x=y"}, wantCode: 0},
		{name: "exception", body: "raise ValueError('boom')\n", wantCode: 1},
		{name: "systemexit", body: "import sys; sys.exit(7)\n", wantCode: 7},
		{name: "keep", body: "print('keep')\n", wantCode: 0, keep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
			steps := []fakekernel.Step{fakekernel.Stream("stdout", "script output\n")}
			if tc.name == "exception" {
				steps = append(steps, fakekernel.Error("ValueError", "boom", []string{"Traceback...", "ValueError: boom"}))
			}
			if tc.name == "systemexit" {
				steps = append(steps, fakekernel.Error("SystemExit", "7", nil))
			}
			steps = append(steps, fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
			h.kernel.Script(steps...)

			f := filepath.Join(t.TempDir(), "script.py")
			if err := os.WriteFile(f, []byte("#!/usr/bin/env python\n"+tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"run"}
			if tc.keep {
				args = append(args, "--keep")
			}
			args = append(args, f)
			args = append(args, tc.args...)
			code := h.run(t, "", args...)
			if code != tc.wantCode {
				t.Fatalf("exit=%d want=%d stderr=%q stdout=%q", code, tc.wantCode, h.stderr.String(), h.stdout.String())
			}
			if tc.name == "exception" && !strings.Contains(h.stderr.String(), "boom") {
				t.Fatalf("stderr = %q", h.stderr.String())
			}
			for _, want := range []string{"[colab] Creating session", "[colab] Session READY"} {
				if !strings.Contains(h.stderr.String(), want) {
					t.Fatalf("stderr missing %q: %q", want, h.stderr.String())
				}
			}
			execs := h.kernel.Executions()
			if len(execs) < 2 {
				t.Fatalf("executions = %+v", execs)
			}
			payload := execs[1].Code
			if !strings.Contains(payload, "sys.argv") || !strings.Contains(payload, "__name__ = '__main__'") {
				t.Fatalf("payload = %q", payload)
			}
			if strings.HasPrefix(payload, "#!") {
				t.Fatalf("shebang not stripped: %q", payload)
			}
			if tc.name == "argv" && !strings.Contains(payload, `"--flag"`) {
				t.Fatalf("argv not forwarded: %q", payload)
			}
			sessions, _ := h.deps.Store.List()
			if tc.keep && len(sessions) != 1 {
				t.Fatalf("--keep left %d sessions", len(sessions))
			}
			if !tc.keep && len(sessions) != 0 {
				t.Fatalf("session not torn down: %v", sessions)
			}
		})
	}
}

// jsonRecords decodes one NDJSON cell record per stdout line. Port of upstream
// tests/test_exec_json.py::_records.
func jsonRecords(t *testing.T, raw string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	out := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout line is not JSON: %v (%q)", err, line)
		}
		out = append(out, m)
	}
	return out
}

// Live v0.2.0 follow-up: a clean sys.exit(0) is silent in text mode, but
// IPython emits a harmless "To exit: use ..." UserWarning that leaked into the
// --json record's stderr. The narrow prelude filter stops it at the source;
// the CLI-side scrub below defends records if a kernel still emits it.
// A fake kernel cannot execute Python, so it proves the prelude is sent and
// that an already-emitted warning never reaches a record — not the CPython
// warning machinery itself (that remains a human live-e2e proof).
func TestExecJSONFiltersIPythonExitHint(t *testing.T) {
	const hint = "/usr/local/lib/python3.13/dist-packages/IPython/core/interactiveshell.py:3561: UserWarning: To exit: use 'exit', 'quit', or Ctrl-D.\n" +
		"  warn(\"To exit: use 'exit', 'quit', or Ctrl-D.\", stacklevel=1)\n"
	const otherWarning = "/tmp/other.py:1: UserWarning: something else entirely\n  warnings.warn(\"something else entirely\")\n"
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(
		fakekernel.Stream("stdout", "sys-exit-ok\n"),
		fakekernel.Stream("stderr", hint+otherWarning),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	if code := h.run(t, "print('sys-exit-ok')\n", "exec", "-s", "s1", "--json"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	records := jsonRecords(t, h.stdout.String())
	if len(records) != 1 {
		t.Fatalf("records = %d: %q", len(records), h.stdout.String())
	}
	r := records[0]
	if r["stdout"] != "sys-exit-ok\n" {
		t.Fatalf("stdout field = %v", r["stdout"])
	}
	if got := r["stderr"]; got != otherWarning {
		t.Fatalf("stderr field = %q, want only the unrelated warning %q", got, otherWarning)
	}
	execs := h.kernel.Executions()
	if len(execs) < 2 {
		t.Fatalf("executions = %+v", execs)
	}
	payload := execs[1].Code
	if !strings.Contains(payload, `warnings.filterwarnings("ignore", message="To exit: use")`) {
		t.Fatalf("prelude lacks the narrow warning filter: %q", payload)
	}
	if strings.Contains(payload, "simplefilter") || strings.Count(payload, "filterwarnings") != 1 {
		t.Fatalf("prelude must not suppress warnings globally: %q", payload)
	}
}

// Ports upstream tests/test_exec_json.py::test_exec_json_success_emits_one_record.
// Go keeps its own CPython exit-code mapping; only the output shape is ported.
func TestExecJSONSuccess(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(
		fakekernel.Stream("stdout", "hello\n"),
		fakekernel.Stream("stderr", "warn\n"),
		fakekernel.ExecuteResult(map[string]any{"text/plain": "42"}, 1),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	if code := h.run(t, "print(1)\n", "exec", "-s", "s1", "--json"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	records := jsonRecords(t, h.stdout.String())
	if len(records) != 1 {
		t.Fatalf("records = %d: %q", len(records), h.stdout.String())
	}
	r := records[0]
	for key, want := range map[string]any{
		"cell_index": float64(0), "status": "ok",
		"stdout": "hello\n", "stderr": "warn\n",
	} {
		if r[key] != want {
			t.Errorf("%s = %v, want %v (record %v)", key, r[key], want, r)
		}
	}
	if r["cell_id"] != nil || r["error"] != nil {
		t.Fatalf("cell_id/error must be null: %v", r)
	}
	data, ok := r["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("data = %v", r["data"])
	}
	item, ok := data[0].(map[string]any)
	if !ok || item["output_type"] != "execute_result" || item["text/plain"] != "42" {
		t.Fatalf("data item = %v", data[0])
	}
	if images, ok := r["images"].([]any); !ok || len(images) != 0 {
		t.Fatalf("images = %v", r["images"])
	}
	// The kernel's stderr is captured in the record, never replayed.
	if strings.Contains(h.stderr.String(), "warn") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
}

// Ports upstream tests/test_exec_json.py::test_exec_json_saves_images_and_reports_paths.
func TestExecJSONImages(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	png := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	h.kernel.Script(
		fakekernel.Stream("stdout", "plotting\n"),
		fakekernel.DisplayData(map[string]any{"image/png": png, "text/plain": "<Figure>"}, nil, ""),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	target := filepath.Join(t.TempDir(), "plot.png")
	if code := h.run(t, "plot()\n", "exec", "-s", "s1", "--json", "--output-image", target); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	records := jsonRecords(t, h.stdout.String())
	if len(records) != 1 {
		t.Fatalf("records = %d: %q", len(records), h.stdout.String())
	}
	r := records[0]
	if r["stdout"] != "plotting\n" {
		t.Fatalf("stdout field = %v", r["stdout"])
	}
	images, ok := r["images"].([]any)
	if !ok || len(images) != 1 || images[0] != target {
		t.Fatalf("images = %v (want [%s])", r["images"], target)
	}
	data, ok := r["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("data = %v", r["data"])
	}
	item := data[0].(map[string]any)
	if item["output_type"] != "display_data" || item["text/plain"] != "<Figure>" {
		t.Fatalf("data item = %v", item)
	}
	if strings.Contains(h.stdout.String(), png) || strings.Contains(h.stdout.String(), "png-bytes") {
		t.Fatalf("base64 image inlined in stdout: %q", h.stdout.String())
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "png-bytes" {
		t.Fatalf("saved image = %q, err=%v", b, err)
	}
}

// Review defect: saveCellImage cut the reported path at the FIRST ']', so a
// legitimate filename containing ']' was reported truncated. The record must
// name the exact file that exists on disk.
func TestExecJSONImagePathWithBracket(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	png := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	h.kernel.Script(
		fakekernel.DisplayData(map[string]any{"image/png": png}, nil, ""),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	target := filepath.Join(t.TempDir(), "a]b.png")
	if code := h.run(t, "plot()\n", "exec", "-s", "s1", "--json", "--output-image", target); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	records := jsonRecords(t, h.stdout.String())
	if len(records) != 1 {
		t.Fatalf("records = %d: %q", len(records), h.stdout.String())
	}
	images, ok := records[0]["images"].([]any)
	if !ok || len(images) != 1 || images[0] != target {
		t.Fatalf("images = %v, want exactly [%s]", records[0]["images"], target)
	}
	// The reported path must be the real file, not its truncated prefix.
	if b, err := os.ReadFile(target); err != nil || string(b) != "png-bytes" {
		t.Fatalf("reported image does not exist: %q, err=%v", target, err)
	}
	prefix := strings.SplitN(target, "]", 2)[0]
	if strings.Contains(h.stdout.String(), `"`+prefix+`"`) {
		t.Fatalf("truncated path reported in stdout: %q", h.stdout.String())
	}
}

// Regression: a --output-image path containing ']' must be reported verbatim
// in the NDJSON images field. The render layer appends its own ']' after the
// path, so parsing with IndexByte used to truncate "/tmp/a]b.png" to "/tmp/a".
func TestExecJSONImagesPathWithBracket(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	png := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	h.kernel.Script(
		fakekernel.DisplayData(map[string]any{"image/png": png}, nil, ""),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	target := filepath.Join(t.TempDir(), "a]b.png")
	if code := h.run(t, "plot()\n", "exec", "-s", "s1", "--json", "--output-image", target); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	records := jsonRecords(t, h.stdout.String())
	if len(records) != 1 {
		t.Fatalf("records = %d: %q", len(records), h.stdout.String())
	}
	images, ok := records[0]["images"].([]any)
	if !ok || len(images) != 1 || images[0] != target {
		t.Fatalf("images = %v (want [%s])", records[0]["images"], target)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "png-bytes" {
		t.Fatalf("saved image = %q, err=%v", b, err)
	}
}

// Ports upstream tests/test_exec_json.py::test_exec_json_error_strips_ansi_and_exits_nonzero.
func TestExecJSONErrorSanitized(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(
		fakekernel.Stream("stdout", "before\n"),
		fakekernel.Error("ValueError", "bad", []string{
			"\x1b[0;31mValueError\x1b[0m Traceback",
			"\x1b[1;32m----> 1\x1b[0m boom()",
		}),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	if code := h.run(t, "boom()\n", "exec", "-s", "s1", "--json"); code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	records := jsonRecords(t, h.stdout.String())
	if len(records) != 1 {
		t.Fatalf("records = %d: %q", len(records), h.stdout.String())
	}
	r := records[0]
	if r["status"] != "error" || r["stdout"] != "before\n" {
		t.Fatalf("record = %v", r)
	}
	errObj, ok := r["error"].(map[string]any)
	if !ok {
		t.Fatalf("error = %v", r["error"])
	}
	if errObj["ename"] != "ValueError" || errObj["evalue"] != "bad" {
		t.Fatalf("error = %v", errObj)
	}
	if got := errObj["traceback"]; got != "ValueError Traceback\n----> 1 boom()" {
		t.Fatalf("traceback = %q", got)
	}
	if strings.Contains(h.stdout.String(), "\x1b") || strings.Contains(h.stderr.String(), "\x1b") {
		t.Fatalf("ANSI leaked: stdout=%q stderr=%q", h.stdout.String(), h.stderr.String())
	}
	// Kernel errors are not replayed on stderr in --json mode.
	if strings.Contains(h.stderr.String(), "ValueError") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
}

// Ports upstream tests/test_exec_json.py::test_exec_json_notebook_emits_record_per_cell.
// This fork deliberately keeps the newly-landed CPython stop-at-first-failing-
// cell behaviour, so only the cells actually executed produce records.
func TestExecJSONNotebook(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(fakekernel.Stream("stdout", "first\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(
		fakekernel.Error("NameError", "fail", []string{"NameError: fail\n"}),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	n := nb.NewNotebook()
	c1 := nb.NewCodeCell("ok()")
	c1.ID = "cell-a"
	c2 := nb.NewCodeCell("fail()")
	c2.ID = "cell-b"
	c3 := nb.NewCodeCell("after()")
	c3.ID = "cell-c"
	n.Cells = []nb.Cell{c1, c2, c3}
	raw, err := nb.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "nb.ipynb")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run(t, "", "exec", "-s", "s1", "--json", "-f", path); code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	records := jsonRecords(t, h.stdout.String())
	if len(records) != 2 {
		t.Fatalf("records = %d: %q", len(records), h.stdout.String())
	}
	for i, want := range []struct {
		index  int
		id     string
		status string
	}{{0, "cell-a", "ok"}, {1, "cell-b", "error"}} {
		r := records[i]
		if r["cell_index"] != float64(want.index) || r["cell_id"] != want.id || r["status"] != want.status {
			t.Fatalf("record %d = %v", i, r)
		}
	}
	if execs := h.kernel.Executions(); len(execs) != 3 { // prelude + 2 cells
		t.Fatalf("executions = %d", len(execs))
	}
	// Progress stays on stderr so stdout remains only records.
	if !strings.Contains(h.stderr.String(), "[colab] Executing cell 1/3") ||
		!strings.Contains(h.stderr.String(), "_output.ipynb") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
}

// Exit codes are a CPython contract owned by run.go's exitCodeFromOutputs.
// --json must not perturb them for any error shape (PR #237 compatibility).
func TestExecJSONExitCodesMatchTextMode(t *testing.T) {
	systemExit := func(evalue string) fakekernel.Step {
		return fakekernel.Error("SystemExit", evalue, []string{"SystemExit: " + evalue + "\n"})
	}
	for _, tc := range []struct {
		name string
		step fakekernel.Step
		want int
	}{
		{name: "success", step: fakekernel.Stream("stdout", "ok\n"), want: 0},
		{name: "error", step: fakekernel.Error("ValueError", "boom", []string{"ValueError: boom\n"}), want: 1},
		{name: "systemexit zero", step: systemExit("0"), want: 0},
		{name: "systemexit seven", step: systemExit("7"), want: 7},
		{name: "systemexit message", step: systemExit("fatal config error"), want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codes := make([]int, 0, 2)
			for _, jsonMode := range []bool{false, true} {
				h := newHarness(t)
				h.putSession(t, "s1")
				h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
				h.kernel.Script(tc.step, fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
				args := []string{"exec", "-s", "s1"}
				if jsonMode {
					args = append(args, "--json")
				}
				codes = append(codes, h.run(t, "code()\n", args...))
			}
			if codes[0] != tc.want || codes[1] != tc.want {
				t.Fatalf("exit codes text/json = %v, want both %d", codes, tc.want)
			}
		})
	}
}

// Ports upstream tests/test_exec_json.py::test_exec_json_reports_client_side_failure.
func TestExecJSONTimeoutRecord(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(fakekernel.Step{Delay: 500 * time.Millisecond, MsgType: "status", Content: map[string]any{"execution_state": "idle"}})
	if code := h.run(t, "slow()\n", "exec", "-s", "s1", "--json", "--timeout", "0.01"); code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	records := jsonRecords(t, h.stdout.String())
	if len(records) != 1 {
		t.Fatalf("records = %d: %q", len(records), h.stdout.String())
	}
	r := records[0]
	if r["status"] != "error" {
		t.Fatalf("record = %v", r)
	}
	errObj, ok := r["error"].(map[string]any)
	if !ok || errObj["ename"] == "" || errObj["evalue"] == "" {
		t.Fatalf("error = %v", r["error"])
	}
}

// Ports upstream tests/test_exec_json.py::test_exec_json_lost_session_keeps_stdout_clean.
func TestExecJSONLostSessionCleanStdout(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.SetUnauthorized(true)
	if code := h.run(t, "print(1)\n", "exec", "-s", "s1", "--json"); code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if h.stdout.String() != "" {
		t.Fatalf("stdout = %q", h.stdout.String())
	}
	if !strings.Contains(h.stderr.String(), "appears to be lost") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
}
