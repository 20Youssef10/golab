package runtime

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

// colabRequestFrame is the wire shape of an intercepted human-auth request.
func colabRequestFrame(t *testing.T) *Message {
	t.Helper()
	return newMessage("colab_request", "iopub", "session-test", MessageHeader{},
		map[string]any{"request": map[string]any{"authType": "oauth"}})
}

// TestColabRequestHooksAreBoundedAndStopOnClose ports the intent of the
// upstream colab_request interception stress coverage: a hook that parks on a
// human must not spawn one goroutine per frame, and hook work must not outlive
// the connection. Flood + close must leave at most one in-flight hook and no
// queued hook may start after close.
func TestColabRequestHooksAreBoundedAndStopOnClose(t *testing.T) {
	ws := &fakeWS{events: make(chan wsEvent, 256), closed: make(chan struct{})}
	c := newConn(ws, logx.Logger())
	defer c.close()

	const flood = 32
	release := make(chan struct{})
	var active, maxActive, invoked atomic.Int64
	entered := make(chan struct{}, 1)
	c.setColabHook(func(Request) bool {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		invoked.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // a hook that waits for a human
		active.Add(-1)
		return true
	})

	for i := 0; i < flood; i++ {
		ws.pushFrame(t, colabRequestFrame(t))
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("colab_request hook was never invoked")
	}
	// Give the reader time to consume the rest of the flood while parked.
	time.Sleep(100 * time.Millisecond)

	if got := maxActive.Load(); got > 1 {
		t.Fatalf("hook ran with %d concurrent instances; want a single bounded worker", got)
	}
	if got := invoked.Load(); got > 3 {
		t.Fatalf("hook invoked %d times for a %d-frame flood; want at most one in-flight plus a small bounded queue",
			got, flood)
	}

	// Cut the connection while the hook is blocked, then unblock it. The queued
	// tasks must be abandoned rather than executed after close.
	c.close()
	close(release)
	time.Sleep(100 * time.Millisecond)
	if got := invoked.Load(); got != 1 {
		t.Fatalf("hook invoked %d times after close; want only the one already in flight", got)
	}
}

// TestSendUsesSocketHeldUnderWriteLock ports the intent of the upstream
// transient-redial regression: a socket swap mid-write must never let an
// input_reply / execute_request land on a retired socket and vanish. The test
// parks a sender on writeMu, swaps the active socket, then releases it; the
// frame must go to the socket that is current once the lock is acquired.
func TestSendUsesSocketHeldUnderWriteLock(t *testing.T) {
	first := newFakeWS()
	second := newFakeWS()
	c := newConn(first, logx.Logger())
	defer c.close()

	msg := newMessage("input_reply", "stdin", "session-test", MessageHeader{},
		map[string]any{"value": "answer"})
	wantTyp, wantData, err := EncodeFrame(msg)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		// Hold writeMu so the sender is parked before it can snapshot the
		// socket. This widens the old snapshot-before-lock window to a
		// deterministic race we can observe.
		c.writeMu.Lock()
		errCh := make(chan error, 1)
		go func() { errCh <- c.send(context.Background(), msg) }()
		time.Sleep(20 * time.Millisecond) // let the sender reach the mutex

		// Redial swaps the active socket while that sender is waiting.
		c.stateMu.Lock()
		c.ws = second
		c.stateMu.Unlock()
		c.writeMu.Unlock()

		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("send %d failed: %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("send %d did not complete", i)
		}
		if got := len(first.written()); got != 0 {
			t.Fatalf("iteration %d: %d frames landed on the retired socket", i, got)
		}
		if got := second.written(); len(got) != i+1 {
			t.Fatalf("iteration %d: current socket has %d frames, want %d", i, len(got), i+1)
		}
	}

	got := second.written()
	if len(got) == 0 || got[0].typ != wantTyp || !bytes.Equal(got[0].data, wantData) {
		t.Fatal("frame bytes changed across the socket swap")
	}
}
