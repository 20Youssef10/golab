package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
)

// stubRecoveryKernel is a minimal Kernel that lets the CLI recovery tests
// force a transport failure and observe Close / Resume without a live proxy.
type stubRecoveryKernel struct {
	mu            sync.Mutex
	execCalls     int
	failOnCall    int
	executeErr    error
	resumeErr     error
	resumeOutputs []runtime.Output
	closed        bool
	execMsgIDs    []string
	resumeMsgIDs  []string
}

func (s *stubRecoveryKernel) ID() string        { return "kernel-stub" }
func (s *stubRecoveryKernel) SessionID() string { return "session-stub" }

func (s *stubRecoveryKernel) Execute(_ context.Context, _ string, opts runtime.ExecOptions) (*runtime.Result, error) {
	s.mu.Lock()
	s.execCalls++
	call := s.execCalls
	s.execMsgIDs = append(s.execMsgIDs, opts.MsgID)
	s.mu.Unlock()
	if s.failOnCall == call {
		return nil, s.executeErr
	}
	return &runtime.Result{Status: "ok"}, nil
}

func (s *stubRecoveryKernel) Resume(_ context.Context, msgID string, opts runtime.ExecOptions) (*runtime.Result, error) {
	s.mu.Lock()
	s.resumeMsgIDs = append(s.resumeMsgIDs, msgID)
	s.mu.Unlock()
	if s.resumeErr != nil {
		return nil, s.resumeErr
	}
	for _, o := range s.resumeOutputs {
		opts.OnOutput(o)
	}
	return &runtime.Result{Outputs: s.resumeOutputs, Status: "ok"}, nil
}

func (s *stubRecoveryKernel) Restart(context.Context) error   { return nil }
func (s *stubRecoveryKernel) Interrupt(context.Context) error { return nil }
func (s *stubRecoveryKernel) Shutdown(context.Context) error  { return nil }
func (s *stubRecoveryKernel) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}
func (s *stubRecoveryKernel) SendColabReply(context.Context, runtime.Request, any) error { return nil }
func (s *stubRecoveryKernel) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
func (s *stubRecoveryKernel) msgIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execMsgIDs...)
}

type stubRecoveryRuntime struct {
	kernel  *stubRecoveryKernel
	mu      sync.Mutex
	targets []runtime.Target
}

func (r *stubRecoveryRuntime) Attach(_ context.Context, target runtime.Target, opts runtime.AttachOptions) (runtime.Kernel, error) {
	r.mu.Lock()
	r.targets = append(r.targets, target)
	r.mu.Unlock()
	if opts.OnKernelStarted != nil {
		opts.OnKernelStarted(r.kernel.ID())
	}
	if opts.OnSessionStarted != nil {
		sid := target.SessionID
		if sid == "" {
			sid = "session-minted"
		}
		opts.OnSessionStarted(sid)
	}
	return r.kernel, nil
}
func (r *stubRecoveryRuntime) sessions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.targets))
	for _, t := range r.targets {
		out = append(out, t.SessionID)
	}
	return out
}

// TestRunTransportErrorKeepsSessionAndWritesPending pins the honest teardown
// behaviour: a transport interruption must NOT unassign/delete the session,
// must keep Closeing the socket, persist Pending and print the resume hint.
func TestRunTransportErrorKeepsSessionAndWritesPending(t *testing.T) {
	h := newHarness(t)
	stub := &stubRecoveryKernel{
		failOnCall: 2,
		executeErr: &runtime.OutputGapError{Cause: errors.New("connection lost")},
	}
	rt := &stubRecoveryRuntime{kernel: stub}
	h.deps.Runtime = rt

	script := filepath.Join(t.TempDir(), "main.py")
	if err := os.WriteFile(script, []byte("print('hi')\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := h.run(t, "", "run", "-s", "sess", script); code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "colab resume -s sess") {
		t.Fatalf("missing resume hint: %q", h.stderr.String())
	}
	if strings.Contains(h.stderr.String(), "Session terminated.") {
		t.Fatalf("session was torn down: %q", h.stderr.String())
	}
	for _, r := range h.ctrl.Requests() {
		if strings.Contains(r.Path, "unassign") || r.Method == "DELETE" {
			t.Fatalf("transport error unassigned the runtime: %+v", r)
		}
	}
	s, err := h.deps.Store.Get("sess")
	if err != nil {
		t.Fatalf("session was deleted: %v", err)
	}
	if s.Pending == nil {
		t.Fatal("no Pending execution was persisted")
	}
	if s.Pending.MsgID == "" || s.Pending.CodeSHA256 == "" {
		t.Fatalf("incomplete Pending: %+v", s.Pending)
	}
	if s.Pending.SessionID == "" {
		t.Fatalf("Pending has no session id: %+v", s.Pending)
	}
	ids := stub.msgIDs()
	if len(ids) != 2 || ids[1] != s.Pending.MsgID {
		t.Fatalf("Pending msg id does not match the execution: %q vs %+v", s.Pending.MsgID, ids)
	}
	if !stub.isClosed() {
		t.Fatal("socket was not closed on transport error")
	}
}

// TestResumeCommandTypedFailuresAndTimeout covers: no Pending (typed failure),
// --timeout 0 (unlimited, accepted), Ctrl-C (130) and Pending cleared on
// success. It also verifies the saved SessionID is honored at Attach.
func TestResumeCommandTypedFailuresAndTimeout(t *testing.T) {
	t.Run("no pending", func(t *testing.T) {
		h := newHarness(t)
		h.putSession(t, "sess")
		stub := &stubRecoveryKernel{}
		h.deps.Runtime = &stubRecoveryRuntime{kernel: stub}

		if code := h.run(t, "", "resume", "-s", "sess"); code != 1 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stderr.String(), "No pending execution") {
			t.Fatalf("missing no-pending hint: %q", h.stderr.String())
		}
	})

	newPending := func() state.PendingExecution {
		return state.PendingExecution{
			MsgID: "exec-saved", SessionID: "saved-session", Delivered: 1,
			StartedAt: time.Now(), CodeSHA256: "sha",
		}
	}

	t.Run("timeout zero succeeds and clears pending", func(t *testing.T) {
		h := newHarness(t)
		s := h.putSession(t, "sess")
		s.KernelID = "kernel-stub"
		s.Pending = ptrPending(newPending())
		if err := h.deps.Store.Put(s); err != nil {
			t.Fatal(err)
		}
		stub := &stubRecoveryKernel{resumeOutputs: []runtime.Output{
			{OutputType: "stream", Name: "stdout", Text: "late\n"},
		}}
		rt := &stubRecoveryRuntime{kernel: stub}
		h.deps.Runtime = rt

		if code := h.run(t, "", "resume", "-s", "sess", "--timeout", "0"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stdout.String(), "late") {
			t.Fatalf("late output not rendered: %q", h.stdout.String())
		}
		fresh, err := h.deps.Store.Get("sess")
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Pending != nil {
			t.Fatalf("Pending not cleared after success: %+v", fresh.Pending)
		}
		for _, sid := range rt.sessions() {
			if sid != "saved-session" {
				t.Fatalf("Attach did not honor saved SessionID: %q", sid)
			}
		}
		if len(stub.resumeMsgIDs) != 1 || stub.resumeMsgIDs[0] != "exec-saved" {
			t.Fatalf("Resume got wrong msg id: %q", stub.resumeMsgIDs)
		}
	})

	t.Run("interrupt maps to 130 and keeps pending", func(t *testing.T) {
		h := newHarness(t)
		s := h.putSession(t, "sess")
		s.Pending = ptrPending(newPending())
		if err := h.deps.Store.Put(s); err != nil {
			t.Fatal(err)
		}
		stub := &stubRecoveryKernel{resumeErr: context.Canceled}
		h.deps.Runtime = &stubRecoveryRuntime{kernel: stub}

		if code := h.run(t, "", "resume", "-s", "sess"); code != 130 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		fresh, err := h.deps.Store.Get("sess")
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Pending == nil {
			t.Fatal("Pending should survive an interrupted resume")
		}
	})
}

func ptrPending(v state.PendingExecution) *state.PendingExecution { return &v }
