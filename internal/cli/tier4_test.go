package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/bundle"
	"github.com/20Youssef10/golab/internal/envlock"
	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
	"github.com/20Youssef10/golab/internal/version"
)

// tier4ProbeStep emits one marker-delimited environment probe on the fake
// kernel. The accelerator field is intentionally empty: the CLI fills it from
// the resolved session, which is what actually determines hardware mismatch.
func tier4ProbeStep(t *testing.T, py string, reqs []string, cuda, gpu string) fakekernel.Step {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"python_version": py, "pip_freeze": reqs, "cuda": cuda, "gpu": gpu, "accelerator": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	return fakekernel.Stream("stdout", envlock.Marker+string(b)+"\n")
}

func queueTier4Exec(h *testHarness, steps ...fakekernel.Step) {
	steps = append(steps, fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
	h.kernel.Script(steps...)
}

func tier4Lockfile(t *testing.T, py string, reqs []string, cuda, gpu, accel string) string {
	t.Helper()
	b, err := envlock.Marshal(&envlock.Lockfile{
		Schema: envlock.Schema, Python: py, Requirements: reqs,
		CUDA: cuda, GPU: gpu, Accelerator: accel,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "requirements.lock")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// `env freeze` must capture interpreter, pip freeze and GPU into one
// deterministic, redacted lockfile. The untrusted kernel stream carries a
// planted credential that must never reach the file or the terminal.
func TestEnvFreezeIsDeterministicAndRedacted(t *testing.T) {
	h := newHarness(t)
	s := h.putSession(t, "freeze-sess")
	s.Accelerator = "T4"
	if err := h.deps.Store.Put(s); err != nil {
		t.Fatal(err)
	}
	probe := tier4ProbeStep(t, "3.11.9", []string{
		"zope==2.0",
		"private @ git+https://user:planted-secret-xyz@git.example.com/repo.git",
		"numpy==1.26.0",
	}, "12.1", "Tesla T4")
	queueTier4Exec(h, fakekernel.Stream("stdout", "chatter "+envlock.Marker+"not-json\n"), probe)
	queueTier4Exec(h, probe)

	out1 := filepath.Join(t.TempDir(), "one.lock")
	out2 := filepath.Join(t.TempDir(), "two.lock")
	if c := h.run(t, "", "env", "freeze", "-s", "freeze-sess", "-o", out1); c != 0 {
		t.Fatalf("freeze exit = %d, stderr = %s", c, h.stderr.String())
	}
	if c := h.run(t, "", "env", "freeze", "-s", "freeze-sess", "-o", out2); c != 0 {
		t.Fatalf("second freeze exit = %d, stderr = %s", c, h.stderr.String())
	}
	a, err := os.ReadFile(out1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out2)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("freeze output not deterministic:\n%s\n%s", a, b)
	}
	got, err := envlock.Parse(a)
	if err != nil {
		t.Fatal(err)
	}
	if got.Python != "3.11.9" || got.CUDA != "12.1" || got.GPU != "Tesla T4" || got.Accelerator != "T4" {
		t.Fatalf("frozen environment = %+v", got)
	}
	if len(got.Requirements) != 3 || got.Requirements[0] != "numpy==1.26.0" {
		t.Fatalf("requirements = %v", got.Requirements)
	}
	combined := string(a) + h.stdout.String() + h.stderr.String()
	if strings.Contains(combined, "planted-secret-xyz") {
		t.Fatalf("credential leaked: %s", combined)
	}
	info, err := os.Stat(out1)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lockfile mode = %o, want 600", info.Mode().Perm())
	}
}

// --lockfile verifies hardware/interpreter then installs through the existing
// `uv pip install --system -r` automation, uploading only a fixed remote name.
func TestRunLockfileInstallAndMismatch(t *testing.T) {
	t.Run("matching environment installs", func(t *testing.T) {
		h := newHarness(t)
		fc := newFakeContents(t)
		h.deps.Config.ContentsHTTPClient = &http.Client{
			Transport: rewriteTransport{base: fc.Client().Transport, to: mustURL(t, fc.URL)},
			Timeout:   10 * time.Second,
		}
		lock := tier4Lockfile(t, "3.11.9", []string{"numpy==1.26.0"}, "12.1", "Tesla T4", "T4")
		queueTier4Exec(h)                                                                   // content prelude
		queueTier4Exec(h, tier4ProbeStep(t, "3.11.9", nil, "12.1", "Tesla T4"))             // probe
		queueTier4Exec(h, fakekernel.Stream("stdout", "Installation Complete (via uv)!\n")) // install
		queueTier4Exec(h, fakekernel.Stream("stdout", "ran\n"))                             // script

		script := filepath.Join(t.TempDir(), "train.py")
		if err := os.WriteFile(script, []byte("print('ran')\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if c := h.run(t, "", "run", "-s", "lock-sess", "--gpu", "t4", "--lockfile", lock, script); c != 0 {
			t.Fatalf("run exit = %d, stderr = %s", c, h.stderr.String())
		}
		execs := h.kernel.Executions()
		if len(execs) != 4 {
			t.Fatalf("executions = %d, want 4: %+v", len(execs), execs)
		}
		install := execs[2].Code
		for _, want := range []string{"uv", "pip", "install", "--system", "-r", "/content/golab-requirements.txt"} {
			if !strings.Contains(install, want) {
				t.Fatalf("install code missing %q: %s", want, install)
			}
		}
		fc.mu.Lock()
		uploaded := string(fc.files["content/golab-requirements.txt"])
		fc.mu.Unlock()
		if uploaded != "numpy==1.26.0\n" {
			t.Fatalf("uploaded requirements = %q", uploaded)
		}
		if !strings.Contains(h.stdout.String(), "ran") {
			t.Fatalf("script output = %q", h.stdout.String())
		}
	})

	t.Run("mismatch fails before install", func(t *testing.T) {
		h := newHarness(t)
		fc := newFakeContents(t)
		h.deps.Config.ContentsHTTPClient = &http.Client{
			Transport: rewriteTransport{base: fc.Client().Transport, to: mustURL(t, fc.URL)},
			Timeout:   10 * time.Second,
		}
		lock := tier4Lockfile(t, "3.11.9", []string{"numpy==1.26.0"}, "12.1", "Tesla T4", "T4")
		queueTier4Exec(h)
		queueTier4Exec(h, tier4ProbeStep(t, "3.10.12", nil, "12.1", "Tesla T4"))
		script := filepath.Join(t.TempDir(), "train.py")
		if err := os.WriteFile(script, []byte("print('never')\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if c := h.run(t, "", "run", "-s", "lock-sess", "--gpu", "t4", "--lockfile", lock, script); c != 1 {
			t.Fatalf("mismatch exit = %d, want 1; stderr = %s", c, h.stderr.String())
		}
		if !strings.Contains(h.stderr.String(), "Environment mismatch") ||
			!strings.Contains(h.stderr.String(), "python_version") {
			t.Fatalf("stderr lacks mismatch detail: %s", h.stderr.String())
		}
		if got := h.kernel.Executions(); len(got) != 2 {
			t.Fatalf("executions after mismatch = %d, want 2: %+v", len(got), got)
		}
		fc.mu.Lock()
		puts := append([]string(nil), fc.puts...)
		fc.mu.Unlock()
		if len(puts) != 0 {
			t.Fatalf("uploaded requirements despite mismatch: %v", puts)
		}
	})
}

// Papermill-style -p values are Python literals injected after the tagged
// parameters cell, in name order; inline flags after the notebook path count.
func TestRunNotebookParameterInjection(t *testing.T) {
	h := newHarness(t)
	params := nb.NewCodeCell("EPOCHS = 1")
	params.ID = "params-cell"
	params.Metadata["tags"] = []any{"parameters"}
	later := nb.NewCodeCell("print(EPOCHS, LR, epochs, flag)")
	later.ID = "later-cell"
	md := nb.NewMarkdownCell("# notes")
	path, outPath := writeRunNotebook(t, ".ipynb", md, params, later)

	pfile := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(pfile, []byte("epochs: 7\nflag: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	queueTier4Exec(h) // content prelude
	queueTier4Exec(h) // parameters cell
	queueTier4Exec(h) // injected cell
	queueTier4Exec(h) // later cell

	// -p before the path is a cobra flag; -p and --params-file after the
	// path are extracted from the forwarded args (interspersed is off).
	if c := h.run(t, "", "run", "-p", "EPOCHS=10", path, "-p", "LR=0.01", "--params-file", pfile); c != 0 {
		t.Fatalf("run exit = %d, stderr = %s", c, h.stderr.String())
	}
	execs := h.kernel.Executions()
	if len(execs) != 4 {
		t.Fatalf("executions = %d, want 4: %+v", len(execs), execs)
	}
	if !strings.Contains(execs[1].Code, "EPOCHS = 1") {
		t.Fatalf("parameters cell is not second: %q", execs[1].Code)
	}
	injected := execs[2].Code
	for _, want := range []string{`EPOCHS = "10"`, `LR = "0.01"`, "epochs = 7", "flag = True"} {
		if !strings.Contains(injected, want) {
			t.Fatalf("injected cell missing %q: %q", want, injected)
		}
	}
	if strings.Index(injected, "EPOCHS") > strings.Index(injected, "LR") ||
		strings.Index(injected, "LR") > strings.Index(injected, "epochs") {
		t.Fatalf("injected assignments out of order: %q", injected)
	}
	if !strings.Contains(execs[3].Code, "print(EPOCHS, LR, epochs, flag)") {
		t.Fatalf("later cell is not fourth: %q", execs[3].Code)
	}
	saved := readRunOutput(t, outPath)
	if len(saved.Cells) != 4 || !strings.Contains(string(saved.Cells[2].Source), `EPOCHS = "10"`) {
		t.Fatalf("saved notebook lacks injected cell: %+v", saved.Cells)
	}
}

func TestRunNotebookParamsWithoutTaggedCellFailsEarly(t *testing.T) {
	h := newHarness(t)
	path, _ := writeRunNotebook(t, ".ipynb", nb.NewCodeCell("print(1)"))
	if c := h.run(t, "", "run", path, "-p", "EPOCHS=10"); c != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", c, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "parameters") {
		t.Fatalf("stderr lacks usage hint: %s", h.stderr.String())
	}
	if got := h.kernel.Executions(); len(got) != 0 {
		t.Fatalf("-p allocated a VM without a parameters cell: %+v", got)
	}
}

// --bundle records code/env/hardware/upstream provenance. It must stay
// "recorded", never claim a verified replay or a fresh-VM check.
func TestRunBundleRecordsProvenance(t *testing.T) {
	h := newHarness(t)
	queueTier4Exec(h)                                                                 // content prelude
	queueTier4Exec(h, fakekernel.Stream("stdout", "ok\n"))                            // script
	queueTier4Exec(h, tier4ProbeStep(t, "3.11.9", []string{"numpy==1.26.0"}, "", "")) // env probe (after run)

	script := filepath.Join(t.TempDir(), "train.py")
	body := []byte("print('ok')\n")
	if err := os.WriteFile(script, body, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "run.json")
	if c := h.run(t, "", "run", "--bundle", "--out", out, script); c != 0 {
		t.Fatalf("run exit = %d, stderr = %s", c, h.stderr.String())
	}
	m, err := bundle.Read(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if m.Provenance.RecordedBy != "golab run --bundle" {
		t.Fatalf("recorded_by = %q", m.Provenance.RecordedBy)
	}
	if m.Environment == nil || m.Environment.PythonVersion != "3.11.9" ||
		len(m.Environment.PipFreeze) != 1 {
		t.Fatalf("environment = %+v", m.Environment)
	}
	// bundle.New hashes the (path, digest) record, not the raw file body.
	wantCodeHash := bundle.Hash([]byte("train.py\x00" + bundle.Hash(body) + "\n"))
	if m.CodeHash != wantCodeHash || m.UpstreamVersion != version.Upstream ||
		m.GolabVersion != version.Number || m.Accelerator != "NONE" {
		t.Fatalf("manifest = %+v", m)
	}
	if strings.Contains(strings.ToLower(h.stderr.String()), "verified") {
		t.Fatalf("bundle run claimed verification: %s", h.stderr.String())
	}
}

// `bundle verify --env` actually allocates a fresh VM, probes it and tears it
// down. A stored manifest is never mutated into a "verified" claim.
func TestBundleVerifyEnvUsesFreshVM(t *testing.T) {
	env := &bundle.Environment{
		PythonVersion: "3.11.9", PipFreeze: []string{"numpy==1.26.0"},
		CUDA: "12.1", GPU: "Tesla T4", Accelerator: "T4",
	}
	m, err := bundle.NewRun(bundle.Input{
		Files:       []bundle.File{{Path: "train.py", Content: []byte("print(1)\n")}},
		Env:         map[string]string{"python_version": "3.11.9"},
		Accelerator: "T4", Variant: "DEFAULT", Shape: "STANDARD",
		UpstreamVersion: version.Upstream, GolabVersion: version.Number,
		Environment: env,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "run.json")
	if err := bundle.Write(context.Background(), manifest, m); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(manifest)

	t.Run("match in fresh VM", func(t *testing.T) {
		h := newHarness(t)
		queueTier4Exec(h, tier4ProbeStep(t, "3.11.9", []string{"numpy==1.26.0"}, "12.1", "Tesla T4"))
		if c := h.run(t, "", "bundle", "verify", "--env", manifest); c != 0 {
			t.Fatalf("verify exit = %d, stderr = %s", c, h.stderr.String())
		}
		if !strings.Contains(h.stdout.String(), "FRESH VM") ||
			!strings.Contains(h.stdout.String(), "matched") {
			t.Fatalf("stdout = %q", h.stdout.String())
		}
		assigned := 0
		for _, r := range h.ctrl.Requests() {
			if r.Method == "POST" && strings.Contains(r.Path, "/assign") {
				assigned++
			}
		}
		if assigned != 1 {
			t.Fatalf("fresh VM allocations = %d, want 1", assigned)
		}
		sessions, err := h.deps.Store.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 0 {
			t.Fatalf("fresh VM was not torn down: %v", sessions)
		}
		after, _ := os.ReadFile(manifest)
		if string(before) != string(after) {
			t.Fatal("verify mutated the recorded manifest")
		}
	})

	t.Run("mismatch in fresh VM", func(t *testing.T) {
		h := newHarness(t)
		queueTier4Exec(h, tier4ProbeStep(t, "3.10.12", nil, "12.1", "Tesla T4"))
		if c := h.run(t, "", "bundle", "verify", "--env", manifest); c != 1 {
			t.Fatalf("mismatch exit = %d, want 1; stderr = %s", c, h.stderr.String())
		}
		if !strings.Contains(h.stderr.String(), "Environment mismatch") {
			t.Fatalf("stderr lacks mismatch: %s", h.stderr.String())
		}
		sessions, err := h.deps.Store.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 0 {
			t.Fatalf("mismatch left a session behind: %v", sessions)
		}
	})

	t.Run("metadata-only verify allocates nothing", func(t *testing.T) {
		h := newHarness(t)
		if c := h.run(t, "", "bundle", "verify", manifest); c != 0 {
			t.Fatalf("metadata verify exit = %d, stderr = %s", c, h.stderr.String())
		}
		if len(h.ctrl.Requests()) != 0 {
			t.Fatalf("metadata verify allocated: %+v", h.ctrl.Requests())
		}
		if !strings.Contains(h.stdout.String(), "recorded") {
			t.Fatalf("metadata verify did not report honesty: %q", h.stdout.String())
		}
	})
}
