package cli

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

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
