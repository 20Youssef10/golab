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

// ErrFrameTooLarge is returned by EncodeFrame when a message cannot be
// represented within the protocol's 32-bit offsets and maxFrameSize limit.
// Callers must use errors.Is/errors.As; the message text is never matched as
// a string (AGENTS.md error convention).
var ErrFrameTooLarge = errors.New("frame too large")

// FrameSizeError reports the first send-side offset that would overflow the
// wire offset field or exceed maxFrameSize. It carries only numeric limits;
// no message content is interpolated into the error text.
type FrameSizeError struct {
	Offset uint64
	Limit  uint64
}

func (e *FrameSizeError) Error() string {
	return fmt.Sprintf("frame offset %d exceeds limit %d", e.Offset, e.Limit)
}
func (e *FrameSizeError) Is(target error) bool { return target == ErrFrameTooLarge }
func (e *FrameSizeError) Unwrap() error        { return ErrFrameTooLarge }

// checkedFrameOffset converts a running send-side offset to its 32-bit wire
// value, rejecting anything above maxFrameSize or above the uint32 range
// before the cast. maxFrameSize is the tighter bound today, but both are
// checked so raising it later cannot reintroduce a wrap (§4.4).
func checkedFrameOffset(off uint64) (uint32, error) {
	limit := uint64(maxFrameSize)
	if maxUint32 := uint64(^uint32(0)); limit > maxUint32 {
		limit = maxUint32
	}
	if off > limit {
		return 0, &FrameSizeError{Offset: off, Limit: limit}
	}
	return uint32(off), nil
}

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
	// Accumulate in uint64 and validate before every cast so a large buffer
	// can neither wrap a 32-bit offset nor produce a frame the decoder rejects.
	off := uint64(4 * (nbufs + 1))
	first, err := checkedFrameOffset(off)
	if err != nil {
		return 0, nil, err
	}
	offsets[0] = first // start of the JSON envelope
	off += uint64(len(j))
	parts := make([][]byte, 0, nbufs)
	parts = append(parts, j)
	for i, b := range msg.Buffers {
		o, err := checkedFrameOffset(off)
		if err != nil {
			return 0, nil, err
		}
		offsets[i+1] = o // start of buffer i (0-based); i+1 <= nbufs-1 always
		off += uint64(len(b))
		parts = append(parts, b)
	}
	total, err := checkedFrameOffset(off)
	if err != nil {
		return 0, nil, err
	}
	out := make([]byte, 0, int(total))
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
