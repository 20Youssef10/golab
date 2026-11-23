package runtime

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/coder/websocket"
)

// Protocol limits. A Colab display_data frame can carry multi-megabyte images,
// so the read limit is deliberately >= 64 MiB (ADR-007).
const (
	maxFrameSize = 64 << 20
	maxBuffers   = 4096
)

// DecodeFrame decodes one WebSocket message of the Jupyter default protocol.
// A text frame is a single JSON object; a binary frame is
//
//	[u32be nbufs][u32be offsets x nbufs][json][buffer_1]...[buffer_nbufs-1]
//
// with offsets[0] == 4*(nbufs+1) and nbufs >= 2. The final buffer extends to
// the end of the frame. Every offset is validated before any slice is taken so
// malformed input can never panic or over-allocate (ADR-008 hardening).
func DecodeFrame(data []byte, binaryFrame bool) (*Message, error) {
	if !binaryFrame {
		if len(data) > maxFrameSize {
			return nil, fmt.Errorf("frame exceeds %d bytes", maxFrameSize)
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("invalid JSON frame: %w", err)
		}
		return &msg, nil
	}
	if len(data) < 4 {
		return nil, errors.New("binary frame shorter than its buffer count")
	}
	nbufs := binary.BigEndian.Uint32(data[:4])
	// Per docs/02 §3.3 a binary frame must contain at least one real buffer;
	// a JSON-only message is always sent as a text frame.
	if nbufs < 2 || nbufs > maxBuffers {
		return nil, fmt.Errorf("invalid buffer count %d", nbufs)
	}
	headerSize := 4 * (int(nbufs) + 1)
	if len(data) < headerSize || len(data) > maxFrameSize {
		return nil, errors.New("binary frame header is truncated or oversized")
	}
	offsets := make([]uint32, nbufs)
	for i := range offsets {
		offsets[i] = binary.BigEndian.Uint32(data[4+i*4:])
	}
	if offsets[0] != uint32(headerSize) {
		return nil, fmt.Errorf("first offset must be %d, got %d", headerSize, offsets[0])
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i] <= offsets[i-1] || offsets[i] > uint32(len(data)) {
			return nil, errors.New("binary frame offsets are not strictly increasing and in range")
		}
	}
	msg := &Message{}
	if err := json.Unmarshal(data[offsets[0]:offsets[1]], msg); err != nil {
		return nil, fmt.Errorf("invalid binary frame JSON: %w", err)
	}
	msg.Buffers = make([][]byte, 0, nbufs-1)
	for i := 1; i < int(nbufs); i++ {
		start := int(offsets[i])
		end := len(data)
		if i+1 < int(nbufs) {
			end = int(offsets[i+1])
		}
		msg.Buffers = append(msg.Buffers, data[start:end])
	}
	return msg, nil
}

// EncodeFrame produces the wire frame for msg. Messages with no buffers use a
// text frame; messages with buffers use the binary layout above.
func EncodeFrame(msg *Message) (websocket.MessageType, []byte, error) {
	envelope := map[string]any{
		"header": msg.Header, "parent_header": msg.ParentHeader,
		"metadata": rawObject(msg.Metadata), "content": rawObject(msg.Content),
		"channel": msg.Channel,
	}
	j, err := json.Marshal(envelope)
	if err != nil {
		return 0, nil, err
	}
	if len(msg.Buffers) == 0 {
		return websocket.MessageText, j, nil
	}
	nbufs := len(msg.Buffers) + 1
	offsets := make([]uint32, nbufs)
	off := uint32(4 * (nbufs + 1))
	offsets[0] = off // start of the JSON envelope
	off += uint32(len(j))
	parts := make([][]byte, 0, nbufs)
	parts = append(parts, j)
	for i, b := range msg.Buffers {
		if i+1 < nbufs {
			offsets[i+1] = off // start of buffer i (0-based)
		}
		off += uint32(len(b))
		parts = append(parts, b)
	}
	out := make([]byte, 0, int(off))
	var b4 [4]byte
	binary.BigEndian.PutUint32(b4[:], uint32(nbufs))
	out = append(out, b4[:]...)
	for _, o := range offsets {
		binary.BigEndian.PutUint32(b4[:], o)
		out = append(out, b4[:]...)
	}
	for _, p := range parts {
		out = append(out, p...)
	}
	return websocket.MessageBinary, out, nil
}
