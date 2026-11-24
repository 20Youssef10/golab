package cli

import (
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
