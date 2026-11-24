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

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/history"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

// Ports upstream tests/test_cli.py::test_cli_new_tpu and
// tests/test_runtime_options.py::test_resolve_tpu.
func TestNewTPU(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "new", "-s", "my-session", "--tpu", "v5e1"); code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	s, err := h.deps.Store.Get("my-session")
	if err != nil {
		t.Fatal(err)
	}
	if s.Variant != "TPU" || s.Accelerator != "V5E1" {
		t.Fatalf("session = %+v", s)
	}
	if s.TokenExpiresAt == nil || time.Until(*s.TokenExpiresAt) < 55*time.Minute {
		t.Fatalf("expiry = %v", s.TokenExpiresAt)
	}
	if strings.Contains(h.stdout.String(), "Creating session") {
		t.Fatalf("chatter leaked to stdout: %q", h.stdout.String())
	}
	for _, want := range []string{"[colab] Creating session 'my-session'...", "[colab] Session READY."} {
		if !strings.Contains(h.stderr.String(), want) {
			t.Fatalf("stderr missing %q: %q", want, h.stderr.String())
		}
	}
}

// Ports upstream tests/test_cli.py::test_cli_new_gpu_variants and
// tests/test_runtime_options.py::test_resolve_gpu_is_case_insensitive.
func TestNewGPUCaseInsensitive(t *testing.T) {
	for _, tc := range []struct{ flag, want string }{
		{"H100", "H100"}, {"l4", "L4"}, {"t4", "T4"}, {"g4", "G4"}, {"a100", "A100"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			h := newHarness(t)
			if code := h.run(t, "", "new", "-s", "s", "--gpu", tc.flag); code != 0 {
				t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
			}
			s, _ := h.deps.Store.Get("s")
			if s.Accelerator != tc.want || s.Variant != "GPU" {
				t.Fatalf("accelerator = %q variant = %q", s.Accelerator, s.Variant)
			}
		})
	}
}

// Ports upstream tests/test_cli.py::test_cli_new_rejects_unknown_gpu and
// tests/test_runtime_options.py::test_unknown_gpu_is_rejected.
func TestNewRejectsUnknownGPU(t *testing.T) {
	for _, flag := range []string{"A10O", "foo", "V100", "t4x"} {
		t.Run(flag, func(t *testing.T) {
			h := newHarness(t)
			code := h.run(t, "", "new", "-s", "s", "--gpu", flag)
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr=%q", code, h.stderr.String())
			}
			if !strings.Contains(h.stderr.String(), "unsupported value") || !strings.Contains(h.stderr.String(), flag) || !strings.Contains(h.stderr.String(), "--gpu") {
				t.Fatalf("stderr = %q", h.stderr.String())
			}
			if len(h.ctrl.Requests()) != 0 {
				t.Fatalf("network call made despite invalid flag: %+v", h.ctrl.Requests())
			}
			if _, err := h.deps.Store.Get("s"); err == nil {
				t.Fatal("session persisted despite invalid flag")
			}
		})
	}
}

// Ports upstream tests/test_cli.py::test_cli_new_rejects_unknown_tpu.
func TestNewRejectsUnknownTPU(t *testing.T) {
	h := newHarness(t)
	code := h.run(t, "", "new", "-s", "s", "--tpu", "v4")
	if code != 2 || !strings.Contains(h.stderr.String(), "--tpu") {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
}

// Ports docs/05: --tpu silently wins when both flags are supplied.
func TestNewTPUSilentlyWins(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "new", "-s", "s", "--gpu", "t4", "--tpu", "v5e1"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	s, _ := h.deps.Store.Get("s")
	if s.Variant != "TPU" || s.Accelerator != "V5E1" {
		t.Fatalf("session = %+v", s)
	}
	if strings.Contains(h.stderr.String(), "both") || strings.Contains(h.stderr.String(), "conflict") {
		t.Fatalf("unexpected conflict warning: %q", h.stderr.String())
	}
}

// Ports upstream tests/test_cli.py::test_cli_new_no_name and
// test_cli_new_default_is_cpu.
func TestNewDefaultNameAndCPU(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "new"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	sessions, err := h.deps.Store.List()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("store = %v, %v", sessions, err)
	}
	for name, s := range sessions {
		if len(name) != 6 || strings.Trim(name, "0123456789abcdef") != "" {
			t.Fatalf("name = %q", name)
		}
		if s.Variant != "DEFAULT" || s.Accelerator != "NONE" {
			t.Fatalf("session = %+v", s)
		}
	}
	for _, r := range h.ctrl.Requests() {
		if r.Path == "/tun/m/assign" {
			if got := r.Query.Get("accelerator"); got != "NONE" {
				t.Fatalf("accelerator query = %q", got)
			}
			if got := r.Query.Get("variant"); got != "DEFAULT" {
				t.Fatalf("variant query = %q", got)
			}
		}
	}
}

// Ports upstream tests/test_cli.py::test_cli_new_high_mem and the
// --high-mem-only notice in commands/session.py::new.
func TestNewHighMem(t *testing.T) {
	t.Run("supported", func(t *testing.T) {
		h := newHarness(t)
		if code := h.run(t, "", "new", "-s", "hm", "--gpu", "a100", "--high-mem"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		s, _ := h.deps.Store.Get("hm")
		if s.MachineShape != "HIGH_RAM" {
			t.Fatalf("shape = %q", s.MachineShape)
		}
		if strings.Contains(h.stderr.String(), "ignored") {
			t.Fatalf("unexpected notice: %q", h.stderr.String())
		}
		for _, r := range h.ctrl.Requests() {
			if r.Path == "/tun/m/assign" && r.Method == http.MethodGet {
				if got := r.Query.Get("shape"); got != "hm" {
					t.Fatalf("shape query = %q", got)
				}
			}
		}
	})
	t.Run("ignored for L4", func(t *testing.T) {
		h := newHarness(t)
		if code := h.run(t, "", "new", "-s", "l4", "--gpu", "l4", "--high-mem"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		s, _ := h.deps.Store.Get("l4")
		if s.MachineShape != "STANDARD" {
			t.Fatalf("shape = %q", s.MachineShape)
		}
		if !strings.Contains(h.stderr.String(), "--high-mem ignored") {
			t.Fatalf("missing notice: %q", h.stderr.String())
		}
	})
}

// Ports upstream tests/test_cli.py::test_cli_new_412_with_gpu_shows_friendly_error,
// test_cli_new_400_with_gpu_shows_friendly_error and
// test_cli_new_400_without_accelerator_propagates.
func TestNewFriendlyAllocationErrors(t *testing.T) {
	t.Run("412", func(t *testing.T) {
		h := newHarness(t)
		h.ctrl.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/assign", Status: http.StatusPreconditionFailed})
		code := h.run(t, "", "new", "--gpu", "T4")
		if code != 1 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(strings.ToLower(h.stderr.String()), "precondition") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
		if _, err := h.deps.Store.Get(""); err != nil {
			_ = err
		}
		sessions, _ := h.deps.Store.List()
		if len(sessions) != 0 {
			t.Fatalf("partial state saved: %v", sessions)
		}
	})
	t.Run("400 with accelerator", func(t *testing.T) {
		h := newHarness(t)
		h.ctrl.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/assign", Status: http.StatusBadRequest})
		code := h.run(t, "", "new", "--gpu", "A100")
		if code != 1 || !strings.Contains(h.stderr.String(), "A100") || !strings.Contains(strings.ToLower(h.stderr.String()), "quota") {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
	})
	t.Run("400 without accelerator propagates", func(t *testing.T) {
		h := newHarness(t)
		h.ctrl.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/assign", Status: http.StatusBadRequest})
		code := h.run(t, "", "new")
		if code == 0 || strings.Contains(strings.ToLower(h.stderr.String()), "quota") {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
	})
}

// Ports upstream tests/test_cli.py::test_cli_sessions_unified_format,
// test_cli_sessions_orphaned_assignment_marked and
// test_cli_sessions_no_assignments.
func TestSessions(t *testing.T) {
	t.Run("format", func(t *testing.T) {
		h := newHarness(t)
		s := h.assignAndStore(t, "s1", colabapi.AssignRequest{
			Variant: colabapi.VariantGPU, Accelerator: colabapi.AccelT4,
		})
		if code := h.run(t, "", "sessions"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		want := "[s1] " + s.Endpoint + " | Hardware: T4 | Shape: Standard | Variant: GPU"
		if !strings.Contains(h.stdout.String(), want) {
			t.Fatalf("stdout = %q; want %q", h.stdout.String(), want)
		}
	})
	t.Run("orphan and hardware labels", func(t *testing.T) {
		h := newHarness(t)
		assignment, err := h.client.Assign(context.Background(), colabapi.AssignRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if code := h.run(t, "", "sessions"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		want := "[?] " + assignment.Endpoint + " | Hardware: CPU | Shape: Standard | Variant: DEFAULT"
		if !strings.Contains(h.stdout.String(), want) {
			t.Fatalf("stdout = %q; want %q", h.stdout.String(), want)
		}
	})
	t.Run("no assignments", func(t *testing.T) {
		h := newHarness(t)
		if code := h.run(t, "", "sessions"); code != 0 {
			t.Fatalf("exit=%d", code)
		}
		if !strings.Contains(h.stderr.String(), "No active sessions found on server.") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
	})
}

// Ports upstream tests/test_cli.py::test_cli_status and
// test_cli_status_running_shows_busy.
func TestStatus(t *testing.T) {
	h := newHarness(t)
	h.assignAndStore(t, "s1", colabapi.AssignRequest{NotebookHash: randomNBH(t)})
	s, err := h.deps.Store.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	s.Accelerator, s.Variant, s.MachineShape = "NONE", "DEFAULT", "STANDARD"
	s.LastExecution = &state.LastExecution{Kind: "my_notebook.ipynb", Value: ptr("cell_1"), At: "2023-10-27 12:00:00"}
	if err := h.deps.Store.Put(*s); err != nil {
		t.Fatal(err)
	}
	if code := h.run(t, "", "status", "-s", "s1"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	want := "[s1] " + s.Endpoint + " | Hardware: CPU | Shape: Standard | Variant: DEFAULT | Status: IDLE"
	if !strings.Contains(h.stdout.String(), want) {
		t.Fatalf("stdout = %q", h.stdout.String())
	}
	want = "  Last Execution: my_notebook.ipynb | Cell: cell_1 at 2023-10-27 12:00:00"
	if !strings.Contains(h.stdout.String(), want) {
		t.Fatalf("stdout = %q", h.stdout.String())
	}

	s.Running = ptr("exec.py")
	if err := h.deps.Store.Put(*s); err != nil {
		t.Fatal(err)
	}
	h.stdout.Reset()
	h.stderr.Reset()
	if code := h.run(t, "", "status", "-s", "s1"); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(h.stdout.String(), "Status: BUSY (exec.py)") {
		t.Fatalf("stdout = %q", h.stdout.String())
	}

	h.stdout.Reset()
	h.stderr.Reset()
	if code := h.run(t, "", "status", "-s", "missing"); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(h.stderr.String(), "Session 'missing' not found") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
}

// Ports upstream tests/test_cli.py::test_cli_stop, test_cli_stop_all and
// test_cli_stop_all_no_sessions.
func TestStop(t *testing.T) {
	t.Run("all", func(t *testing.T) {
		h := newHarness(t)
		for _, name := range []string{"s1", "s2"} {
			h.assignAndStore(t, name, colabapi.AssignRequest{
				NotebookHash: randomNBH(t),
			})
		}
		if code := h.run(t, "", "stop", "--all"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stderr.String(), "Stopped 2 session(s).") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
		sessions, _ := h.deps.Store.List()
		if len(sessions) != 0 {
			t.Fatalf("sessions remain: %v", sessions)
		}
		assignments, err := h.client.List(context.Background())
		if err != nil || len(assignments) != 0 {
			t.Fatalf("assignments = %v, %v", assignments, err)
		}
	})
	t.Run("all empty", func(t *testing.T) {
		h := newHarness(t)
		if code := h.run(t, "", "stop", "--all"); code != 0 {
			t.Fatalf("exit=%d", code)
		}
		if !strings.Contains(h.stderr.String(), "No active sessions.") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
	})
	t.Run("single", func(t *testing.T) {
		h := newHarness(t)
		s := h.assignAndStore(t, "solo", colabapi.AssignRequest{NotebookHash: randomNBH(t)})
		if code := h.run(t, "", "stop", "-s", "solo"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stderr.String(), "Session terminated.") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
		if _, err := h.deps.Store.Get(s.Name); err == nil {
			t.Fatal("session still present")
		}
	})
}

// Ports upstream tests/test_cli.py::test_cli_log_* via the history package's
// exporters; the CLI selects the exporter from the -o suffix.
func TestLog(t *testing.T) {
	h := newHarness(t)
	payload, _ := json.Marshal(map[string]any{"code": "print('hi')", "outputs": []any{map[string]any{"text": "hi\n"}}})
	h.addHistory(t, "s1", history.Event{EventType: history.EventExecution, Payload: payload})
	if code := h.run(t, "", "log", "-s", "s1"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "EXEC: print('hi')...") {
		t.Fatalf("stdout = %q", h.stdout.String())
	}

	out := filepath.Join(t.TempDir(), "history.md")
	h.stdout.Reset()
	h.stderr.Reset()
	if code := h.run(t, "", "log", "-s", "s1", "-o", out); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	b, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(b), "print('hi')") {
		t.Fatalf("export = %q, %v", b, err)
	}
}

// Ports upstream enhancement #47 and tests/test_cli.py::test_cli_status:
// `status` must report the shape the server actually assigned, not the stale
// locally recorded (requested) shape. The overlay is display-only.
func TestStatusOverlaysServerShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"named", []string{"status", "-s", "s1"}},
		{"all", []string{"status"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.assignAndStore(t, "s1", colabapi.AssignRequest{
				NotebookHash: randomNBH(t),
				Shape:        colabapi.ShapeHighRAM,
			})
			// The server assigned HIGH_RAM; simulate a stale local record of
			// the originally requested STANDARD shape.
			s, err := h.deps.Store.Get("s1")
			if err != nil {
				t.Fatal(err)
			}
			s.MachineShape = "STANDARD"
			if err := h.deps.Store.Put(*s); err != nil {
				t.Fatal(err)
			}

			if code := h.run(t, "", tc.args...); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
			}
			if !strings.Contains(h.stdout.String(), "Shape: High-RAM") {
				t.Fatalf("stdout = %q; want server-assigned Shape: High-RAM", h.stdout.String())
			}
			got, err := h.deps.Store.Get("s1")
			if err != nil {
				t.Fatal(err)
			}
			if got.MachineShape != "STANDARD" {
				t.Fatalf("on-disk shape = %q; want unchanged STANDARD", got.MachineShape)
			}
		})
	}
}

// Ports upstream enhancement #47: `stop --verify` re-reads GET
// /tun/m/assignments after unassigning and fails when the endpoint survives.
func TestStopVerify(t *testing.T) {
	t.Run("gone", func(t *testing.T) {
		h := newHarness(t)
		h.assignAndStore(t, "solo", colabapi.AssignRequest{NotebookHash: randomNBH(t)})
		if code := h.run(t, "", "stop", "-s", "solo", "--verify"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stderr.String(), "Session terminated.") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
		if !strings.Contains(strings.ToLower(h.stderr.String()), "no longer assigned") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
		var sawList bool
		for _, r := range h.ctrl.Requests() {
			if r.Method == http.MethodGet && r.Path == "/tun/m/assignments" {
				sawList = true
			}
		}
		if !sawList {
			t.Fatalf("no GET /tun/m/assignments after stop: %+v", h.ctrl.Requests())
		}
		if assignments, err := h.client.List(context.Background()); err != nil || len(assignments) != 0 {
			t.Fatalf("assignments = %v, %v", assignments, err)
		}
	})
	t.Run("still present", func(t *testing.T) {
		h := newHarness(t)
		h.assignAndStore(t, "solo", colabapi.AssignRequest{NotebookHash: randomNBH(t)})
		// Let the unassign POST succeed without actually deleting the server
		// assignment, so the verification GET still sees it.
		h.ctrl.SetFault(fakecolab.Fault{
			Method: http.MethodPost, PathContains: "/unassign",
			Status: http.StatusOK, Body: "[]",
		})
		code := h.run(t, "", "stop", "-s", "solo", "--verify")
		if code != 1 {
			t.Fatalf("exit=%d, want 1; stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stderr.String(), "still assigned") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
		if _, err := h.deps.Store.Get("solo"); err == nil {
			t.Fatal("local session not removed")
		}
		var sawList bool
		for _, r := range h.ctrl.Requests() {
			if r.Method == http.MethodGet && r.Path == "/tun/m/assignments" {
				sawList = true
			}
		}
		if !sawList {
			t.Fatalf("no GET /tun/m/assignments after stop: %+v", h.ctrl.Requests())
		}
	})
	t.Run("all gone", func(t *testing.T) {
		h := newHarness(t)
		for _, name := range []string{"s1", "s2"} {
			h.assignAndStore(t, name, colabapi.AssignRequest{NotebookHash: randomNBH(t)})
		}
		if code := h.run(t, "", "stop", "--all", "--verify"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stderr.String(), "Stopped 2 session(s).") ||
			!strings.Contains(h.stderr.String(), "Verified: all 2 session(s) are no longer assigned.") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
	})
	t.Run("all still present", func(t *testing.T) {
		h := newHarness(t)
		for _, name := range []string{"s1", "s2"} {
			h.assignAndStore(t, name, colabapi.AssignRequest{NotebookHash: randomNBH(t)})
		}
		h.ctrl.SetFault(fakecolab.Fault{
			Method: http.MethodPost, PathContains: "/unassign",
			Status: http.StatusOK, Body: "[]", Times: -1,
		})
		if code := h.run(t, "", "stop", "--all", "--verify"); code != 1 ||
			!strings.Contains(h.stderr.String(), "still assigned") {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
	})
}

func ptr(s string) *string { return &s }

func randomNBH(t *testing.T) colabapi.NotebookHash {
	t.Helper()
	h, err := colabapi.RandomNotebookHash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}
