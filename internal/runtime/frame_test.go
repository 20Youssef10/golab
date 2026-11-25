package runtime

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
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

func TestEncodeFrameRejectsOversizedBuffers(t *testing.T) {
	// §4.4: cumulative buffer offsets past maxFrameSize must be rejected on
	// the send side instead of emitting a frame the decoder would refuse.
	// Ports the decoder's "oversized frame" hardening expectation to encode.
	half := maxFrameSize/2 + 1024
	msg := &Message{
		Channel: "iopub",
		Header:  MessageHeader{MsgID: "big", MsgType: "display_data"},
		Buffers: [][]byte{make([]byte, half), make([]byte, half)},
	}
	typ, data, err := EncodeFrame(msg)
	if err == nil {
		t.Fatalf("EncodeFrame accepted cumulative size %d > maxFrameSize %d (type=%v len=%d)",
			2*half, maxFrameSize, typ, len(data))
	}
	if typ != 0 || len(data) != 0 {
		t.Fatalf("failed encode must emit no frame: type=%v len=%d", typ, len(data))
	}
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want typed ErrFrameTooLarge, got %T: %v", err, err)
	}
	var fse *FrameSizeError
	if !errors.As(err, &fse) || fse.Limit > uint64(maxFrameSize) {
		t.Fatalf("want *FrameSizeError bounded by maxFrameSize, got %#v", err)
	}
}

func TestCheckedFrameOffsetGuards(t *testing.T) {
	// §4.4: the cast guard must also reject values beyond the 32-bit wire
	// field, not just beyond maxFrameSize. Tested at helper level so no
	// multi-gigabyte buffer allocation is needed.
	for _, off := range []uint64{uint64(maxFrameSize) + 1, uint64(^uint32(0)) + 1, ^uint64(0)} {
		_, err := checkedFrameOffset(off)
		if !errors.Is(err, ErrFrameTooLarge) {
			t.Fatalf("offset %d: want typed ErrFrameTooLarge, got %v", off, err)
		}
		var fse *FrameSizeError
		if !errors.As(err, &fse) || fse.Offset != off {
			t.Fatalf("offset %d: want *FrameSizeError carrying the offset, got %#v", off, err)
		}
	}
	if got, err := checkedFrameOffset(uint64(maxFrameSize)); err != nil || got != uint32(maxFrameSize) {
		t.Fatalf("boundary offset: got %d, %v", got, err)
	}
}

func TestEncodeFrameGoldenBytes(t *testing.T) {
	// Byte-for-byte regression guard (docs/02 §3.3 wire layout): the offset
	// guard must not change a single byte of a valid multi-buffer frame.
	const wantHex = "" +
		"0000000400000014000000d0000000d3000000d5" +
		"7b226368616e6e656c223a22696f707562222c22636f6e74656e74223a7b2264617461223a" +
		"7b22696d6167652f706e67223a22414141227d2c226d65746164617461223a7b7d7d2c22" +
		"686561646572223a7b226d73675f6964223a22656e6331222c226d73675f74797065223a" +
		"22646973706c61795f64617461227d2c226d65746164617461223a6e756c6c2c22706172" +
		"656e745f686561646572223a7b226d73675f6964223a22726571222c226d73675f747970" +
		"65223a22227d7d616263646566676869"
	want, err := hex.DecodeString(wantHex)
	if err != nil {
		t.Fatalf("bad golden hex: %v", err)
	}
	msg := &Message{
		Channel:      "iopub",
		Header:       MessageHeader{MsgID: "enc1", MsgType: "display_data"},
		ParentHeader: MessageHeader{MsgID: "req"},
		Content:      json.RawMessage(`{"data":{"image/png":"AAA"},"metadata":{}}`),
		Buffers:      [][]byte{[]byte("abc"), []byte("de"), []byte("fghi")},
	}
	typ, got, err := EncodeFrame(msg)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("want binary frame, got %v", typ)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wire bytes changed:\n got %x\nwant %x", got, want)
	}
}
