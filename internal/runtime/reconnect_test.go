package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/coder/websocket"
)

// This file ports the intent of upstream transient-drop regression coverage:
// a channels socket that drops mid-execution must be redialed in place with the
// SAME kernel/session id, and only an exhausted retry budget may surface a
// typed error. Everything is driven through a fake wsConn/dialer so no live
// runtime proxy (and no billable VM) is involved.

// --- fake WebSocket ---------------------------------------------------------

type wsEvent struct {
	typ  websocket.MessageType
	data []byte
	err  error
}

type fakeWrite struct {
	typ  websocket.MessageType
	data []byte
}

type fakeWS struct {
	events chan wsEvent
	closed chan struct{}
	once   sync.Once

	mu     sync.Mutex
	writes []fakeWrite
}

func newFakeWS() *fakeWS {
	return &fakeWS{events: make(chan wsEvent, 16), closed: make(chan struct{})}
}

func (f *fakeWS) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case ev := <-f.events:
		return ev.typ, ev.data, ev.err
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (f *fakeWS) Write(_ context.Context, typ websocket.MessageType, data []byte) error {
	f.mu.Lock()
	f.writes = append(f.writes, fakeWrite{typ: typ, data: data})
	f.mu.Unlock()
	return nil
}

func (f *fakeWS) Ping(context.Context) error { return nil }

func (f *fakeWS) Close(websocket.StatusCode, string) error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeWS) SetReadLimit(int64) {}

func (f *fakeWS) pushFrame(t *testing.T, msg *Message) {
	t.Helper()
	typ, data, err := EncodeFrame(msg)
	if err != nil {
		t.Fatal(err)
	}
	f.events <- wsEvent{typ: typ, data: data}
}

// drop ends this socket generation, mimicking an unexpected reader error.
func (f *fakeWS) drop() {
	f.events <- wsEvent{err: errors.New("simulated channels drop")}
}

func (f *fakeWS) written() []fakeWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeWrite(nil), f.writes...)
}

// --- fake dialer ------------------------------------------------------------

type dialResult struct {
	ws  wsConn
	err error
}

type fakeDialer struct {
	mu       sync.Mutex
	results  []dialResult
	calls    int
	sessions []string
}

func (d *fakeDialer) dial(_ context.Context, _ string, sessionID string) (wsConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.sessions = append(d.sessions, sessionID)
	if len(d.results) > 0 {
		r := d.results[0]
		d.results = d.results[1:]
		return r.ws, r.err
	}
	return nil, errors.New("fake dial: no more results")
}

func (d *fakeDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *fakeDialer) recordedSessions() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.sessions...)
}

// --- helpers ----------------------------------------------------------------

func newFakeKernel(t *testing.T, first *fakeWS, d *fakeDialer) *kernel {
	t.Helper()
	c := newConn(first, logx.Logger())
	c.dialWS = d.dial
	return &kernel{
		id: "kernel-test", sessionID: "session-test", conn: c,
		logger: logx.Logger(), graceful: time.Second,
	}
}

// waitExecID recovers the msg_id Execute chose from the execute_request frame,
// so injected iopub/shell messages carry a matching parent_header.
func waitExecID(t *testing.T, ws *fakeWS) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, w := range ws.written() {
			msg, err := DecodeFrame(w.data, w.typ == websocket.MessageBinary)
			if err != nil || msg.Header.MsgType != "execute_request" {
				continue
			}
			return msg.Header.MsgID
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("execute_request was never sent")
	return ""
}

func msgForExec(execID, channel, msgType string, content any) *Message {
	return newMessage(msgType, channel, "session-test", MessageHeader{MsgID: execID}, content)
}

func streamMsg(execID, text string) *Message {
	return msgForExec(execID, "iopub", "stream", map[string]any{"name": "stdout", "text": text})
}

func idleMsg(execID string) *Message {
	return msgForExec(execID, "iopub", "status", map[string]any{"execution_state": "idle"})
}

func replyMsg(execID, status string) *Message {
	return msgForExec(execID, "shell", "execute_reply", map[string]any{"status": status})
}

func waitDialCalls(d *fakeDialer, n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.count() >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// --- tests ------------------------------------------------------------------

func TestExecuteReconnectsAfterTransientDrop(t *testing.T) {
	// A single drop must be redialed with the same session id; output produced
	// before and after the drop must both reach the caller, with no error.
	first := newFakeWS()
	second := newFakeWS()
	d := &fakeDialer{results: []dialResult{{ws: second}}}
	k := newFakeKernel(t, first, d)
	k.conn.reconnectBudget = 2 * time.Second
	k.conn.reconnectInterval = 10 * time.Millisecond
	defer k.Close()

	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := k.Execute(context.Background(), "work()", ExecOptions{IdleTimeout: 5 * time.Second})
		done <- outcome{res: res, err: err}
	}()

	execID := waitExecID(t, first)
	first.pushFrame(t, streamMsg(execID, "before\n"))
	first.drop()

	if !waitDialCalls(d, 1, 3*time.Second) {
		t.Fatal("transient drop did not trigger a channels redial")
	}
	for _, s := range d.recordedSessions() {
		if s != k.SessionID() {
			t.Fatalf("redial used session %q, want the original %q (no new session)", s, k.SessionID())
		}
	}

	second.pushFrame(t, streamMsg(execID, "after\n"))
	second.pushFrame(t, idleMsg(execID))
	second.pushFrame(t, replyMsg(execID, "ok"))

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("transient drop surfaced an error: %v", got.err)
		}
		if len(got.res.Outputs) != 2 || got.res.Outputs[0].Text != "before\n" ||
			got.res.Outputs[1].Text != "after\n" {
			t.Fatalf("output did not resume across reconnect: %+v", got.res.Outputs)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not resume after reconnect")
	}
	if d.count() != 1 {
		t.Fatalf("want exactly one redial, got %d", d.count())
	}
}

func TestExecutePermanentDropReturnsTypedErrorWithinBound(t *testing.T) {
	// When every redial fails, a typed ConnectionLostError must be returned
	// promptly (well inside the 30 s production budget in tests) and every
	// attempt must reuse the original session id.
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
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("permanent drop returned no error")
		}
		if !errors.Is(err, ConnectionLost) {
			t.Fatalf("want errors.Is(err, ConnectionLost), got %v", err)
		}
		var cle *ConnectionLostError
		if !errors.As(err, &cle) {
			t.Fatalf("want typed *ConnectionLostError, got %T: %v", err, err)
		}
		if cle.Attempts < 1 {
			t.Fatalf("ConnectionLostError has no attempts: %+v", cle)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("permanent drop was not bounded: took %v", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not fail within the reconnect budget")
	}

	sessions := d.recordedSessions()
	if len(sessions) == 0 {
		t.Fatal("no redial attempts were made")
	}
	for _, s := range sessions {
		if s != k.SessionID() {
			t.Fatalf("redial used session %q, want original %q", s, k.SessionID())
		}
	}
}

func TestExecuteReconnectSurfacesSessionLostImmediately(t *testing.T) {
	// A 401/404 is permanent: the session is gone, so redial cannot help and
	// the existing ErrSessionLost contract must survive the reconnect path.
	first := newFakeWS()
	d := &fakeDialer{results: []dialResult{
		{err: &SessionLostError{Op: "open channels", StatusCode: 401}},
	}}
	k := newFakeKernel(t, first, d)
	k.conn.reconnectBudget = time.Second
	k.conn.reconnectInterval = 10 * time.Millisecond
	defer k.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := k.Execute(context.Background(), "work()", ExecOptions{IdleTimeout: 5 * time.Second})
		errCh <- err
	}()

	waitExecID(t, first) // ensure the request was sent before dropping
	first.drop()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrSessionLost) {
			t.Fatalf("want ErrSessionLost, got %v", err)
		}
		var lost *SessionLostError
		if !errors.As(err, &lost) || lost.StatusCode != 401 {
			t.Fatalf("want typed SessionLostError(401), got %#v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("permanent session loss did not surface")
	}
	if d.count() != 1 {
		t.Fatalf("permanent session loss was redialed %d times", d.count())
	}
}

func TestConnectionLostErrorTyping(t *testing.T) {
	// The typed error must keep the sentinel contract and never leak a token.
	err := &ConnectionLostError{Attempts: 3, Elapsed: 30 * time.Second,
		Cause: errors.New("dial failed")}
	if !errors.Is(err, ConnectionLost) {
		t.Fatal("ConnectionLostError must match ConnectionLost")
	}
	if !errors.Is(fmt.Errorf("execute: %w", err), ConnectionLost) {
		t.Fatal("wrapped ConnectionLostError must match ConnectionLost")
	}
	var cle *ConnectionLostError
	if !errors.As(fmt.Errorf("execute: %w", err), &cle) {
		t.Fatal("wrapped ConnectionLostError must be discoverable via errors.As")
	}
}
