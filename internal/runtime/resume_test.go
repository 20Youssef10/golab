package runtime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

// phaseDialer fails every redial until the test flips allow, so an Execute can
// be forced into an OutputGap while a later Resume reattaches to a live socket.
type phaseDialer struct {
	mu    sync.Mutex
	live  wsConn
	allow bool
	calls int
}

func (d *phaseDialer) dial(context.Context, string, string) (wsConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.allow {
		return d.live, nil
	}
	return nil, errors.New("dial refused")
}
func (d *phaseDialer) setAllow(v bool) {
	d.mu.Lock()
	d.allow = v
	d.mu.Unlock()
}
func (d *phaseDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}
func waitPhaseDial(d *phaseDialer, min int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.count() >= min {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// These tests drive the in-process recovery layer through a fake wsConn and
// dialer (the same primitives as the reconnect regression tests). No live
// runtime proxy or billable VM is involved. They pin the HONEST contract: a
// deduplicated replay is emitted once, a gap surfaces as OutputGapError, and a
// resumed execution never resubmits an execute_request.

func execStreamMsg(execID, msgID, text string) *Message {
	m := msgForExec(execID, "iopub", "stream", map[string]any{"name": "stdout", "text": text})
	m.Header.MsgID = msgID
	return m
}

func collectErr(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("execution did not finish within bound")
		return nil
	}
}

func TestExecuteDropWithReplayDeliversEachOutputOnce(t *testing.T) {
	// A kernel that re-sends an output with the SAME msg id after a reconnect
	// must not emit it twice: dedup happens before the accumulator, whose
	// clear_output/update_display_data semantics are not idempotent.
	first := newFakeWS()
	second := newFakeWS()
	d := &fakeDialer{results: []dialResult{{ws: second}}}
	k := newFakeKernel(t, first, d)
	k.conn.reconnectBudget = 2 * time.Second
	k.conn.reconnectInterval = 10 * time.Millisecond
	defer k.Close()

	var emitted []string
	errCh := make(chan error, 1)
	go func() {
		_, err := k.Execute(context.Background(), "work()", ExecOptions{
			IdleTimeout: 5 * time.Second,
			OnOutput:    func(o Output) { emitted = append(emitted, o.Text) },
		})
		errCh <- err
	}()

	execID := waitExecID(t, first)
	first.pushFrame(t, execStreamMsg(execID, "msg-A", "A\n"))
	first.drop()
	if !waitDialCalls(d, 1, 3*time.Second) {
		t.Fatal("transient drop did not trigger a redial")
	}

	// Replay A (duplicate id) then a genuinely new B, then terminal frames.
	second.pushFrame(t, execStreamMsg(execID, "msg-A", "A\n"))
	second.pushFrame(t, execStreamMsg(execID, "msg-B", "B\n"))
	second.pushFrame(t, idleMsg(execID))
	second.pushFrame(t, replyMsg(execID, "ok"))

	if err := collectErr(t, errCh); err != nil {
		t.Fatalf("transient drop surfaced an error: %v", err)
	}
	if len(emitted) != 2 || emitted[0] != "A\n" || emitted[1] != "B\n" {
		t.Fatalf("output was not emitted exactly once each: %q", emitted)
	}
}

func TestExecuteDropDuringCompletionReturnsOutputGap(t *testing.T) {
	// Output produced before a permanently dead socket is recovered in the
	// error, but output emitted DURING the dead window is not: the error is a
	// typed OutputGapError (not a plain timeout) carrying what we did see.
	first := newFakeWS()
	d := &fakeDialer{results: []dialResult{
		{err: errors.New("dial refused")},
		{err: errors.New("dial refused")},
		{err: errors.New("dial refused")},
	}}
	k := newFakeKernel(t, first, d)
	k.conn.reconnectBudget = 120 * time.Millisecond
	k.conn.reconnectInterval = 10 * time.Millisecond
	defer k.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := k.Execute(context.Background(), "work()", ExecOptions{IdleTimeout: 5 * time.Second})
		errCh <- err
	}()

	execID := waitExecID(t, first)
	first.pushFrame(t, execStreamMsg(execID, "msg-A", "early\n"))
	first.drop()
	if !waitDialCalls(d, 1, 3*time.Second) {
		t.Fatal("drop did not trigger a redial")
	}
	// This frame lands on the retired socket and is never read: the honest gap.
	first.pushFrame(t, execStreamMsg(execID, "msg-gap", "during-gap\n"))

	err := collectErr(t, errCh)
	if err == nil {
		t.Fatal("permanent drop returned no error")
	}
	var gap *OutputGapError
	if !errors.As(err, &gap) {
		t.Fatalf("want typed *OutputGapError, got %T: %v", err, err)
	}
	if gap.MsgID == "" {
		t.Fatal("OutputGapError has no msg_id")
	}
	if len(gap.Recovered) != 1 || gap.Recovered[0].Text != "early\n" {
		t.Fatalf("recovered outputs wrong: %+v", gap.Recovered)
	}
	if !errors.Is(err, ConnectionLost) {
		t.Fatalf("OutputGapError must keep errors.Is(err, ConnectionLost): %v", err)
	}
}

func TestPermanentDropWrapsConnectionLostWithinBound(t *testing.T) {
	// Additive to the existing reconnect test: the typed ConnectionLostError
	// must survive as the OutputGapError cause and remain bounded in time.
	first := newFakeWS()
	d := &fakeDialer{results: []dialResult{
		{err: errors.New("dial refused")},
		{err: errors.New("dial refused")},
		{err: errors.New("dial refused")},
	}}
	k := newFakeKernel(t, first, d)
	k.conn.reconnectBudget = 120 * time.Millisecond
	k.conn.reconnectInterval = 10 * time.Millisecond
	defer k.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := k.Execute(context.Background(), "work()", ExecOptions{IdleTimeout: 5 * time.Second})
		errCh <- err
	}()
	execID := waitExecID(t, first)
	first.pushFrame(t, streamMsg(execID, "before\n"))
	first.drop()

	start := time.Now()
	err := collectErr(t, errCh)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("permanent drop was not bounded: %v", elapsed)
	}
	var gap *OutputGapError
	if !errors.As(err, &gap) {
		t.Fatalf("want OutputGapError, got %T: %v", err, err)
	}
	var cle *ConnectionLostError
	if !errors.As(err, &cle) || cle.Attempts < 1 {
		t.Fatalf("ConnectionLostError cause lost: %#v", err)
	}
}

func TestResumeWaitsForLateResultWithoutResubmit(t *testing.T) {
	// After an interrupted Execute, Resume skips the execute_request entirely,
	// re-enters the wait loop and recovers the late output + terminal frames.
	first := newFakeWS()
	live := newFakeWS()
	pd := &phaseDialer{live: live}
	k := newFakeKernel(t, first, &fakeDialer{})
	k.conn.dialWS = pd.dial
	k.conn.reconnectBudget = 120 * time.Millisecond
	k.conn.reconnectInterval = 10 * time.Millisecond
	defer k.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := k.Execute(context.Background(), "work()", ExecOptions{IdleTimeout: 5 * time.Second})
		errCh <- err
	}()
	execID := waitExecID(t, first)
	first.pushFrame(t, execStreamMsg(execID, "msg-A", "A\n"))
	first.drop()
	err := collectErr(t, errCh)
	var gap *OutputGapError
	if !errors.As(err, &gap) {
		t.Fatalf("want OutputGapError from interrupted Execute, got %v", err)
	}
	msgID := gap.MsgID
	callsBefore := pd.count()
	pd.setAllow(true) // allow Resume's reattach to a live socket

	var emitted []string
	done := make(chan *Result, 1)
	resumeErr := make(chan error, 1)
	go func() {
		res, err := k.Resume(context.Background(), msgID, ExecOptions{
			IdleTimeout: 5 * time.Second,
			OnOutput:    func(o Output) { emitted = append(emitted, o.Text) },
		})
		resumeErr <- err
		done <- res
	}()

	// Wait for Resume's reattach before feeding the live socket.
	if !waitPhaseDial(pd, callsBefore+1, 3*time.Second) {
		t.Fatal("Resume did not reattach the channels socket")
	}
	live.pushFrame(t, execStreamMsg(execID, "msg-B", "B\n"))
	live.pushFrame(t, idleMsg(execID))
	live.pushFrame(t, replyMsg(execID, "ok"))

	if err := collectErr(t, resumeErr); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	res := <-done
	if len(res.Outputs) != 2 || res.Outputs[0].Text != "A\n" || res.Outputs[1].Text != "B\n" {
		t.Fatalf("resumed outputs wrong: %+v", res.Outputs)
	}
	if len(emitted) != 1 || emitted[0] != "B\n" {
		t.Fatalf("Resume must emit only the newly recovered output: %q", emitted)
	}
	// Exactly one execute_request total: Resume must not resubmit.
	total := 0
	for _, w := range append(first.written(), live.written()...) {
		msg, dErr := DecodeFrame(w.data, w.typ == websocket.MessageBinary)
		if dErr == nil && msg.Header.MsgType == "execute_request" {
			total++
		}
	}
	if total != 1 {
		t.Fatalf("want exactly one execute_request, got %d", total)
	}
}

func TestResumeAfterIdleThenDropCompletesWithLateReply(t *testing.T) {
	// status:idle arrives before the socket dies, but execute_reply arrives
	// only after Resume reattaches. The idle must be remembered on the journal:
	// otherwise the resumed loop parks the reply waiting for a second idle
	// (the replayed idle is deduped away) and times out a finished execution.
	first := newFakeWS()
	live := newFakeWS()
	pd := &phaseDialer{live: live}
	k := newFakeKernel(t, first, &fakeDialer{})
	k.conn.dialWS = pd.dial
	k.conn.reconnectBudget = 120 * time.Millisecond
	k.conn.reconnectInterval = 10 * time.Millisecond
	defer k.Close()

	execID := "exec-idle-drop"
	emitted := make(chan string, 4)
	errCh := make(chan error, 1)
	go func() {
		_, err := k.Execute(context.Background(), "work()", ExecOptions{
			MsgID:       execID,
			IdleTimeout: 5 * time.Second,
			OnOutput:    func(o Output) { emitted <- o.Text },
		})
		errCh <- err
	}()
	if got := waitExecID(t, first); got != execID {
		t.Fatalf("execute_request used msg id %q, want %q", got, execID)
	}

	idle := idleMsg(execID)
	idle.Header.MsgID = "idle-1"
	first.pushFrame(t, idle)
	// A stream frame after idle proves the idle was consumed (frames are
	// dispatched in order) without peeking at the journal concurrently.
	first.pushFrame(t, execStreamMsg(execID, "msg-A", "A\n"))
	select {
	case got := <-emitted:
		if got != "A\n" {
			t.Fatalf("unexpected output %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle/stream frames were never processed")
	}
	first.drop() // socket dies before execute_reply

	err := collectErr(t, errCh)
	var gap *OutputGapError
	if !errors.As(err, &gap) {
		t.Fatalf("want OutputGapError from interrupted Execute, got %v", err)
	}
	callsBefore := pd.count()
	pd.setAllow(true) // Resume reattaches to a live socket

	done := make(chan *Result, 1)
	resumeErr := make(chan error, 1)
	go func() {
		res, err := k.Resume(context.Background(), execID, ExecOptions{IdleTimeout: 5 * time.Second})
		resumeErr <- err
		done <- res
	}()
	if !waitPhaseDial(pd, callsBefore+1, 3*time.Second) {
		t.Fatal("Resume did not reattach the channels socket")
	}
	// Same-id replay: dedup drops it before the resumed wait loop sees idle.
	// Only the late execute_reply is left, and it must still complete.
	live.pushFrame(t, idle)
	live.pushFrame(t, replyMsg(execID, "ok"))

	if err := collectErr(t, resumeErr); err != nil {
		t.Fatalf("Resume timed out or failed on a finished execution: %v", err)
	}
	res := <-done
	if res == nil || res.Status != "ok" {
		t.Fatalf("Resume returned the wrong result: %+v", res)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Text != "A\n" {
		t.Fatalf("resumed outputs wrong: %+v", res.Outputs)
	}
}

// TestTerminalIdleReplyDeletesJournalAndResumeUsesFreshJournal pins the two
// behaviours around the terminal idle+execute_reply path. The journal's dead
// `terminal` bool used to be set here but was never read: neither recovery nor
// resume gated on it (sawIdle carries the terminal condition). Removing it must
// leave intact (1) deletion of the completed journal and (2) Resume creating a
// fresh journal for the same id and completing against late terminal frames.
func TestTerminalIdleReplyDeletesJournalAndResumeUsesFreshJournal(t *testing.T) {
	ws := newFakeWS()
	k := newFakeKernel(t, ws, &fakeDialer{})
	defer k.Close()

	countRequests := func() int {
		n := 0
		for _, w := range ws.written() {
			msg, err := DecodeFrame(w.data, w.typ == websocket.MessageBinary)
			if err == nil && msg.Header.MsgType == "execute_request" {
				n++
			}
		}
		return n
	}

	const execID = "exec-terminal-idle-reply"
	done := make(chan *Result, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := k.Execute(context.Background(), "work()", ExecOptions{
			MsgID: execID, IdleTimeout: 5 * time.Second,
		})
		errCh <- err
		done <- res
	}()
	if got := waitExecID(t, ws); got != execID {
		t.Fatalf("execute_request used msg id %q, want %q", got, execID)
	}
	// Drive the terminal path: output, status:idle, then execute_reply.
	ws.pushFrame(t, execStreamMsg(execID, "out-1", "A\n"))
	ws.pushFrame(t, idleMsg(execID))
	ws.pushFrame(t, replyMsg(execID, "ok"))

	if err := collectErr(t, errCh); err != nil {
		t.Fatalf("terminal completion returned an error: %v", err)
	}
	res := <-done
	if res == nil || res.Status != "ok" {
		t.Fatalf("terminal completion returned the wrong result: %+v", res)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Text != "A\n" {
		t.Fatalf("terminal completion lost output: %+v", res.Outputs)
	}
	// The terminal journal is deleted; the removed flag never gated this.
	if j := k.lookupJournal(execID); j != nil {
		t.Fatal("terminal journal was not deleted after idle+reply")
	}
	requestsBefore := countRequests()
	if requestsBefore != 1 {
		t.Fatalf("want exactly one execute_request from Execute, got %d", requestsBefore)
	}

	// Resume for the completed id starts from a fresh journal and completes
	// against a new terminal idle+reply, without resubmitting the code.
	resumeDone := make(chan *Result, 1)
	resumeErr := make(chan error, 1)
	go func() {
		res, err := k.Resume(context.Background(), execID, ExecOptions{IdleTimeout: 5 * time.Second})
		resumeErr <- err
		resumeDone <- res
	}()
	ws.pushFrame(t, execStreamMsg(execID, "out-2", "B\n"))
	ws.pushFrame(t, idleMsg(execID))
	ws.pushFrame(t, replyMsg(execID, "ok"))

	if err := collectErr(t, resumeErr); err != nil {
		t.Fatalf("Resume after terminal completion failed: %v", err)
	}
	resumed := <-resumeDone
	if resumed == nil || resumed.Status != "ok" {
		t.Fatalf("Resume returned the wrong result: %+v", resumed)
	}
	if len(resumed.Outputs) != 1 || resumed.Outputs[0].Text != "B\n" {
		t.Fatalf("Resume outputs wrong: %+v", resumed.Outputs)
	}
	if n := countRequests(); n != requestsBefore {
		t.Fatalf("Resume resubmitted execute_request: before=%d after=%d", requestsBefore, n)
	}
}

func countInterruptPOSTs(srv *fakekernel.Server) int {
	n := 0
	for _, r := range srv.Requests() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/interrupt") {
			n++
		}
	}
	return n
}

func waitInterruptPOSTs(t *testing.T, srv *fakekernel.Server, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if countInterruptPOSTs(srv) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("did not see %d interrupt POST(s) within %v", want, timeout)
}

func TestCancelDuringSocketDropPostsOneInterrupt(t *testing.T) {
	// Ctrl-C during a lost channels socket must POST the REST interrupt once.
	// ctx.Err() stays non-nil, so the lostChan branch re-entered interrupt()
	// after the ctxDone/reconnect branch had already done it, double-POSTing
	// and re-arming the graceful-idle window.
	srv := fakekernel.New(t)
	first := newFakeWS()
	d := &fakeDialer{} // every redial fails; cancellation stops the retry loop
	k := newFakeKernel(t, first, d)
	k.id = srv.KernelID()
	k.graceful = 150 * time.Millisecond
	k.rest = &restClient{
		baseURL: srv.URL(), client: srv.Client(), token: srv.Token(),
		agent: "test", requestTimeout: time.Second,
	}
	k.conn.reconnectBudget = 2 * time.Second
	k.conn.reconnectInterval = 10 * time.Millisecond
	defer k.Close()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := k.Execute(ctx, "work()", ExecOptions{IdleTimeout: 5 * time.Second})
		errCh <- err
	}()
	waitExecID(t, first)
	first.drop()
	if !waitDialCalls(d, 1, 2*time.Second) {
		t.Fatal("drop did not start a redial")
	}
	cancel()
	waitInterruptPOSTs(t, srv, 1, 2*time.Second)
	// The closed lost channel stays ready; give the loop room to re-enter the
	// branch that used to POST a second interrupt.
	time.Sleep(100 * time.Millisecond)
	if n := countInterruptPOSTs(srv); n != 1 {
		t.Fatalf("want exactly one interrupt POST, got %d", n)
	}
	if err := collectErr(t, errCh); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if n := countInterruptPOSTs(srv); n != 1 {
		t.Fatalf("want exactly one interrupt POST, got %d", n)
	}
}

// TestAttachHonorsSavedSessionID pins that a non-empty Target.SessionID is
// reused at Attach (instead of minting a fresh id), so a resumed execution can
// reattach to the same Jupyter session.
func TestAttachHonorsSavedSessionID(t *testing.T) {
	srv := fakekernel.New(t)
	rt := newTestRuntime(t, srv)
	tgt := target(srv, "")
	tgt.SessionID = "saved-session-123"
	var notified string
	k, err := rt.Attach(context.Background(), tgt, AttachOptions{
		OnSessionStarted: func(id string) { notified = id },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if notified != "saved-session-123" || k.SessionID() != "saved-session-123" {
		t.Fatalf("saved session id not honored: notified=%q kernel=%q", notified, k.SessionID())
	}
	seen := false
	for _, r := range srv.Requests() {
		if strings.HasSuffix(r.Path, "/channels") {
			seen = true
			if r.Query.Get("session_id") != "saved-session-123" {
				t.Fatalf("channels handshake used session %q", r.Query.Get("session_id"))
			}
		}
	}
	if !seen {
		t.Fatal("no channels handshake was recorded")
	}
}
