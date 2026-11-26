package runtime

// Honesty tooling needs frame COUNTS (for redacted wire traces), never frame
// payloads. This test pins the observer seam without touching a live proxy.

import (
	"sync"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

func TestFrameObserverCountsInboundAndOutbound(t *testing.T) {
	ws := &fakeWS{events: make(chan wsEvent, 8), closed: make(chan struct{})}
	var mu sync.Mutex
	var got []FrameEvent
	c := newConn(ws, logx.Logger(), func(e FrameEvent) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	defer c.close()

	ws.pushFrame(t, newMessage("stream", "iopub", "session-test", MessageHeader{},
		map[string]any{"name": "stdout", "text": "CANARY_PAYLOAD"}))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := c.send(t.Context(), newMessage("input_reply", "stdin", "session-test",
		MessageHeader{}, map[string]any{"value": "CANARY_PAYLOAD"})); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	var in, out int
	for _, e := range got {
		switch e.Direction {
		case "in":
			in++
			if e.Channel != "iopub" || e.MsgType != "stream" {
				t.Fatalf("inbound event = %+v", e)
			}
		case "out":
			out++
			if e.Channel != "stdin" || e.MsgType != "input_reply" {
				t.Fatalf("outbound event = %+v", e)
			}
		}
	}
	if in != 1 || out != 1 {
		t.Fatalf("frame events = %+v (in=%d out=%d)", got, in, out)
	}
}
