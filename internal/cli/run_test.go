package cli

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

// Regression for gap-report §3.10 on the `run` path: a failed notebook write
// must not report success or exit 0. Ports upstream
// tests/test_ipynb_exec.py::test_exec_ipynb_creates_output_file for the CLI run
// command with an injected write failure.
func TestRunNotebookSaveFailure(t *testing.T) {
	orig := writeNotebookFile
	writeNotebookFile = func(string, []byte, os.FileMode) error {
		return errors.New("injected run write failure")
	}
	t.Cleanup(func() { writeNotebookFile = orig })

	h := newHarness(t)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(fakekernel.Stream("stdout", "cell output\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))

	n := nb.NewNotebook()
	c := nb.NewCodeCell("print('a')")
	c.ID = "cell-a"
	n.Cells = []nb.Cell{c}
	path, outPath := writeRunNotebook(t, ".ipynb", n.Cells...)

	if code := h.run(t, "", "run", path); code == 0 {
		t.Fatalf("save failure exited 0; stderr=%q", h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "injected run write failure") {
		t.Fatalf("stderr lacks the save error: %q", h.stderr.String())
	}
	for _, falseSuccess := range []string{"Saved notebook", "Saving notebook"} {
		if strings.Contains(h.stderr.String(), falseSuccess) {
			t.Fatalf("stderr claims %q on a failed save: %q", falseSuccess, h.stderr.String())
		}
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output notebook exists (err=%v) after a failed save", err)
	}
}

// Regression for gap-report §3.12: a failed Pending write must abort BEFORE
// the cell is submitted. A handle that was never persisted makes `colab
// resume` report ErrNoPendingExecution, so continuing would silently disable
// recovery on a billable VM.
func TestRunPendingWriteFailureAbortsBeforeSubmit(t *testing.T) {
	orig := storePut
	storePut = func(d *app.Deps, s state.Session) error {
		if s.Pending != nil {
			return errors.New("injected pending record failure")
		}
		return orig(d, s)
	}
	t.Cleanup(func() { storePut = orig })

	h := newHarness(t)
	script := filepath.Join(t.TempDir(), "pending.py")
	if err := os.WriteFile(script, []byte("print('never submitted')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No kernel scripts are queued: any Execute call would be an overrun.

	if code := h.run(t, "", "run", "-s", "sess", script); code == 0 {
		t.Fatalf("pending write failure exited 0; stderr=%q", h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "injected pending record failure") {
		t.Fatalf("stderr lacks the pending error: %q", h.stderr.String())
	}
	if got := h.kernel.Executions(); len(got) != 0 {
		t.Fatalf("cell was submitted despite the failed pending write: %+v", got)
	}
}

// writeRunNotebook writes an nbformat v4.5 notebook and returns its path and
// the derived <base>_output.ipynb path. The extension is a parameter so one
// case pins upstream PR #200's case-insensitive .ipynb detection.
func writeRunNotebook(t *testing.T, ext string, cells ...nb.Cell) (string, string) {
	t.Helper()
	n := nb.NewNotebook()
	n.Cells = cells
	raw, err := nb.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "demo"+ext)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, strings.TrimSuffix(path, filepath.Ext(path)) + "_output.ipynb"
}

func readRunOutput(t *testing.T, path string) *nb.Notebook {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := nb.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// Ports upstream issue #74 / PR #200: `colab run notebook.ipynb` must parse the
// notebook, skip markdown, execute code cells in order on ONE kernel, prepend
// --env per cell, and write <base>_output.ipynb with 0600 permissions.
func TestRunNotebookCellsShareKernelAndSaveOutputs(t *testing.T) {
	h := newHarness(t)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil)) // /content prelude
	h.kernel.Script(fakekernel.Stream("stdout", "first\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(fakekernel.Stream("stdout", "second\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))

	md := nb.NewMarkdownCell("# ignored")
	c1 := nb.NewCodeCell("# @title One\nx = 1\nprint(x)")
	c1.ID = "cell-one"
	c2 := nb.NewCodeCell("print(x + 1)")
	c2.ID = "cell-two"
	path, outPath := writeRunNotebook(t, ".IPYNB", md, c1, c2)

	if code := h.run(t, "", "run", "--env", "A=1", path); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}

	execs := h.kernel.Executions()
	if len(execs) != 3 { // chdir + two code cells
		t.Fatalf("executions = %d (%+v)", len(execs), execs)
	}
	if !strings.Contains(execs[0].Code, "os.makedirs('/content'") {
		t.Fatalf("prelude = %q", execs[0].Code)
	}
	for i, want := range []string{"x = 1", "print(x + 1)"} {
		got := execs[i+1].Code
		if !strings.Contains(got, want) {
			t.Fatalf("cell %d code missing %q: %q", i+1, want, got)
		}
		if !strings.Contains(got, `os.environ["A"] = "1"`) {
			t.Fatalf("cell %d missing env prelude: %q", i+1, got)
		}
		if strings.Contains(got, `"cells"`) || strings.Contains(got, `"nbformat"`) {
			t.Fatalf("cell %d sent raw notebook JSON: %q", i+1, got)
		}
	}
	if execs[1].SessionID != execs[2].SessionID || execs[1].SessionID == "" {
		t.Fatalf("cells used different kernels: %q vs %q", execs[1].SessionID, execs[2].SessionID)
	}
	creates := 0
	for _, r := range h.kernel.Requests() {
		if r.Method == "POST" && r.Path == "/api/kernels" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("kernel creates = %d, want 1", creates)
	}
	if !strings.Contains(h.stdout.String(), "first") || !strings.Contains(h.stdout.String(), "second") {
		t.Fatalf("stdout = %q", h.stdout.String())
	}
	if !strings.Contains(h.stderr.String(), "Executing cell 1/2 - One...") ||
		!strings.Contains(h.stderr.String(), "Executing cell 2/2 - cell-two...") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0666 for all files; owner-only access is enforced via
	// NTFS ACLs by internal/platform, not Unix mode bits.
	if got := info.Mode().Perm(); goruntime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("output mode = %o, want 600", got)
	}
	saved := readRunOutput(t, outPath)
	if len(saved.Cells) != 3 {
		t.Fatalf("saved cells = %d", len(saved.Cells))
	}
	if len(saved.Cells[1].Outputs) != 1 || string(saved.Cells[1].Outputs[0].Text) != "first\n" {
		t.Fatalf("cell 1 outputs = %+v", saved.Cells[1].Outputs)
	}
	if len(saved.Cells[2].Outputs) != 1 || string(saved.Cells[2].Outputs[0].Text) != "second\n" {
		t.Fatalf("cell 2 outputs = %+v", saved.Cells[2].Outputs)
	}
}

// Live v0.2.0 regression: `colab run notebook.ipynb` saved every executed cell
// with "execution_count": null because the reply's count was never copied onto
// nb.Cell. Markdown cells must stay null and text output must stay unchanged.
func TestRunNotebookCellExecutionCount(t *testing.T) {
	h := newHarness(t)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", map[string]any{"execution_count": 1}))
	h.kernel.Script(
		fakekernel.Stream("stdout", "one\n"),
		fakekernel.Status("idle"),
		fakekernel.Reply("ok", map[string]any{"execution_count": 7}),
	)
	h.kernel.Script(
		fakekernel.Stream("stdout", "two\n"),
		fakekernel.Status("idle"),
		fakekernel.Reply("ok", map[string]any{"execution_count": 8}),
	)

	md := nb.NewMarkdownCell("notes")
	c1 := nb.NewCodeCell("print('one')")
	c2 := nb.NewCodeCell("print('two')")
	path, outPath := writeRunNotebook(t, ".ipynb", md, c1, c2)
	if code := h.run(t, "", "run", path); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "one") || !strings.Contains(h.stdout.String(), "two") {
		t.Fatalf("stdout = %q", h.stdout.String())
	}

	saved := readRunOutput(t, outPath)
	if len(saved.Cells) != 3 {
		t.Fatalf("saved cells = %d", len(saved.Cells))
	}
	if saved.Cells[0].ExecutionCount != nil {
		t.Fatalf("markdown execution_count = %v, want nil", *saved.Cells[0].ExecutionCount)
	}
	for i, want := range []int{7, 8} {
		got := saved.Cells[i+1].ExecutionCount
		if got == nil {
			t.Fatalf("cell %d execution_count is null", i+1)
		}
		if *got != want {
			t.Fatalf("cell %d execution_count = %d, want %d", i+1, *got, want)
		}
	}
	if *saved.Cells[1].ExecutionCount >= *saved.Cells[2].ExecutionCount {
		t.Fatalf("execution counts not increasing: %d, %d",
			*saved.Cells[1].ExecutionCount, *saved.Cells[2].ExecutionCount)
	}
	for i, want := range []string{"one\n", "two\n"} {
		if len(saved.Cells[i+1].Outputs) != 1 || string(saved.Cells[i+1].Outputs[0].Text) != want {
			t.Fatalf("cell %d outputs = %+v", i+1, saved.Cells[i+1].Outputs)
		}
	}
}

// A failing cell must abort before the next one is sent, but the partial
// notebook (including the error output) must still be saved.
func TestRunNotebookStopsOnFirstFailingCell(t *testing.T) {
	h := newHarness(t)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(
		fakekernel.Error("ValueError", "boom", []string{"Traceback...", "ValueError: boom"}),
		fakekernel.Status("idle"), fakekernel.Reply("ok", nil),
	)
	// This script would serve a second cell if the runner did not stop.
	h.kernel.Script(fakekernel.Stream("stdout", "should-not-run\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))

	c1 := nb.NewCodeCell("raise ValueError('boom')")
	c1.ID = "bad-cell"
	c2 := nb.NewCodeCell("print('ok')")
	c2.ID = "never-cell"
	path, outPath := writeRunNotebook(t, ".ipynb", c1, c2)

	if code := h.run(t, "", "run", path); code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if execs := h.kernel.Executions(); len(execs) != 2 { // chdir + failing cell only
		t.Fatalf("executions = %d (%+v)", len(execs), execs)
	}
	if strings.Contains(h.stdout.String(), "should-not-run") {
		t.Fatalf("second cell ran: %q", h.stdout.String())
	}
	if !strings.Contains(h.stderr.String(), "boom") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	saved := readRunOutput(t, outPath)
	if len(saved.Cells[0].Outputs) != 1 || saved.Cells[0].Outputs[0].OutputType != "error" ||
		saved.Cells[0].Outputs[0].Ename != "ValueError" {
		t.Fatalf("cell 1 outputs = %+v", saved.Cells[0].Outputs)
	}
	if len(saved.Cells[1].Outputs) != 0 {
		t.Fatalf("cell 2 outputs = %+v", saved.Cells[1].Outputs)
	}
}

func TestRunNotebookKeepSkipsUnassign(t *testing.T) {
	h := newHarness(t)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(fakekernel.Stream("stdout", "kept\n"), fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	path, _ := writeRunNotebook(t, ".ipynb", nb.NewCodeCell("print('kept')"))

	if code := h.run(t, "", "run", "--keep", path); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	sessions, err := h.deps.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("--keep left %d sessions: %v", len(sessions), sessions)
	}
	if strings.Contains(h.stderr.String(), "Session terminated.") {
		t.Fatalf("session was torn down despite --keep: %q", h.stderr.String())
	}
}

func TestRunNotebookExplainMakesNoPosts(t *testing.T) {
	h := newHarness(t)
	path, outPath := writeRunNotebook(t, ".ipynb", nb.NewCodeCell("print(1)"))

	if code := h.run(t, "", "run", "--explain", path); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "operation=run") {
		t.Fatalf("stdout = %q", h.stdout.String())
	}
	if reqs := h.ctrl.Requests(); len(reqs) != 0 {
		t.Fatalf("control-plane requests: %+v", reqs)
	}
	if reqs := h.kernel.Requests(); len(reqs) != 0 {
		t.Fatalf("kernel requests: %+v", reqs)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("--explain wrote an output notebook (err=%v)", err)
	}
}

// buildScriptPayload must surface read failures instead of returning an empty
// payload that would silently execute as a successful no-op.
func TestBuildScriptPayloadReadError(t *testing.T) {
	payload, err := buildScriptPayload(filepath.Join(t.TempDir(), "missing.py"), nil, nil)
	if err == nil {
		t.Fatal("buildScriptPayload returned nil error for an unreadable script")
	}
	if payload != "" {
		t.Fatalf("payload = %q, want empty on read failure", payload)
	}
}

// Ports upstream tests/test_run.py::test_run_missing_script_errors_before_assign
// and pins confirmed defect 2.3: os.Stat can succeed while os.ReadFile fails
// (TOCTOU / special files). The script must be read BEFORE createSession so an
// unreadable script can never run an empty payload on a billable VM. A bound
// unix socket is used because Stat accepts it and IsDir() is false, but
// os.ReadFile rejects it with ENXIO — a read failure the Stat guard cannot see.
func TestRunUnreadableScriptMakesNoPosts(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("unix socket paths are not readable on Windows")
	}
	h := newHarness(t)
	// macOS caps AF_UNIX socket paths at ~104 bytes and t.TempDir() embeds the
	// full test name, so use a deliberately short path under the OS temp root.
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "x")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot bind a short unix socket on this platform: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	code := h.run(t, "", "run", sock)
	if code != 2 {
		t.Fatalf("exit=%d want 2 (unreadable script must be a usage error) stderr=%q stdout=%q",
			code, h.stderr.String(), h.stdout.String())
	}
	if !strings.Contains(h.stderr.String(), "Could not read script") {
		t.Fatalf("stderr missing clear read error: %q", h.stderr.String())
	}
	if reqs := h.ctrl.Requests(); len(reqs) != 0 {
		t.Fatalf("allocated VM despite unreadable script: %+v", reqs)
	}
	if execs := h.kernel.Executions(); len(execs) != 0 {
		t.Fatalf("executed code despite unreadable script: %+v", execs)
	}
}

func TestRunNotebookWarnsOnExtraArgs(t *testing.T) {
	h := newHarness(t)
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	path, _ := writeRunNotebook(t, ".ipynb", nb.NewCodeCell("print('x')"))

	if code := h.run(t, "", "run", path, "extra", "--flag"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "extra arguments ignored") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
	execs := h.kernel.Executions()
	if len(execs) != 2 {
		t.Fatalf("executions = %d (%+v)", len(execs), execs)
	}
	if strings.Contains(execs[1].Code, "sys.argv") || strings.Contains(execs[1].Code, "extra") || strings.Contains(execs[1].Code, "--flag") {
		t.Fatalf("script args were injected into the cell: %q", execs[1].Code)
	}
}
