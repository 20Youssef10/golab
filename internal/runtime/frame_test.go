package runtime

import (
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestDecodeFrameText(t *testing.T) {
	// Ports the wire-shape assertions implicit in upstream tests/mock_kernel.py:
	// a message with no buffers arrives as one JSON text frame.
	raw := []byte(`{"channel":"iopub","header":{"msg_id":"m1","msg_type":"stream"},` +
		`"parent_header":{"msg_id":"req"},"metadata":{},"content":{"name":"stdout","text":"hi"}}`)
	msg, err := DecodeFrame(raw, false)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if msg.Channel != "iopub" || msg.Header.MsgID != "m1" || msg.ParentHeader.MsgID != "req" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if len(msg.Buffers) != 0 {
		t.Fatalf("text frame must have no buffers: %v", msg.Buffers)
	}
}

func TestDecodeFrameBinary(t *testing.T) {
	// Ports the binary-buffer path from tests/mock_kernel.py (buffers appended
	// to an otherwise normal Jupyter message).
	payload := []byte("binary-payload")
	typ, data, err := EncodeFrame(&Message{
		Channel: "iopub", Header: MessageHeader{MsgID: "m2", MsgType: "display_data"},
		ParentHeader: MessageHeader{MsgID: "req"},
		Content:      json.RawMessage(`{"data":{"image/png":"abc"},"metadata":{}}`),
		Buffers:      [][]byte{payload},
	})
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("want binary frame, got %v", typ)
	}
	msg, err := DecodeFrame(data, true)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if len(msg.Buffers) != 1 || string(msg.Buffers[0]) != string(payload) {
		t.Fatalf("buffers not round-tripped: %q", msg.Buffers)
	}
	if msg.ParentHeader.MsgID != "req" {
		t.Fatalf("parent lost: %+v", msg.ParentHeader)
	}
}

func TestDecodeFrameRejectsInvalid(t *testing.T) {
	// Protocol fidelity (docs/02 §3.3): nbufs < 2 is invalid; offsets must be
	// strictly increasing and in range.
	cases := map[string][]byte{
		"too short":        {0, 0, 0},
		"zero buffers":     {0, 0, 0, 0},
		"one buffer":       {0, 0, 0, 1},
		"huge nbufs":       {0x7f, 0xff, 0xff, 0xff},
		"bad first offset": mustBinaryFrame(2, []uint32{99, 200}, []byte(`{}`)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeFrame(data, true); err == nil {
				t.Fatalf("expected an error for %q", name)
			}
		})
	}
}

func mustBinaryFrame(nbufs uint32, offsets []uint32, parts ...[]byte) []byte {
	out := make([]byte, 0, 64)
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], nbufs)
	out = append(out, b[:]...)
	for _, o := range offsets {
		binary.BigEndian.PutUint32(b[:], o)
		out = append(out, b[:]...)
	}
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestDecodeFrameRejectsNonIncreasingOffsets(t *testing.T) {
	j := []byte(`{"header":{},"content":{}}`)
	// nbufs=2, offset[0]=12 correct, offset[1] points before the JSON.
	data := mustBinaryFrame(2, []uint32{12, 4}, j, []byte("x"))
	if _, err := DecodeFrame(data, true); err == nil {
		t.Fatal("expected an error for non-increasing offsets")
	}
}

// FuzzDecodeFrame ports doc 07 §7's "WS frame decoder" property test: the
// decoder must never panic and must never accept a frame whose buffers do not
// match the offsets. Run with: go test -fuzz=FuzzDecodeFrame ./internal/runtime
func FuzzDecodeFrame(f *testing.F) {
	f.Add([]byte(`{"channel":"iopub","header":{},"content":{}}`), false)
	f.Add(mustBinaryFrame(2, []uint32{12, 20}, []byte(`{"header":{},"content":{}}`), []byte("x")), true)
	f.Add([]byte{0, 0, 0, 1}, true)
	f.Add([]byte{0xff, 0xff, 0xff, 0xff}, true)
	f.Fuzz(func(t *testing.T, data []byte, binaryFrame bool) {
		msg, err := DecodeFrame(data, binaryFrame)
		if err != nil {
			if msg != nil {
				t.Fatalf("decoder returned both a message and an error: %v", err)
			}
			return
		}
		if msg == nil {
			t.Fatal("decoder returned neither a message nor an error")
		}
		if binaryFrame && len(msg.Buffers) == 0 {
			t.Fatal("binary frame accepted with no buffers")
		}
		if msg.Header.MsgType == "" && strings.Contains(string(data), `"msg_type"`) {
			// Not a hard invariant, but catches truncated JSON being accepted.
			t.Fatal("decoder accepted truncated JSON")
		}
		if _, e := json.Marshal(msg); e != nil {
			t.Fatalf("decoded message is not marshalable: %v", e)
		}
	})
}
