// Package agentout defines golab's stable structured-output schema.
//
// Schema "golab/1" has exactly three event shapes on stdout:
//
//	{"schema":"golab/1","type":"info","text":"…"}
//	{"schema":"golab/1","type":"output","text":"…"}
//	{"schema":"golab/1","type":"error","text":"…"}
//
// With format "ndjson" each event is one line. With format "json" the stream
// is one document, {"schema":"golab/1","events":[…]}, finalised by Close.
// Format "text" is a byte-for-byte passthrough. Diagnostics always stay on
// stderr. Every text value is passed through logx.Redact before encoding, and
// no token field exists in the schema. Field order is part of the contract:
// adding a field is compatible, renaming or removing one requires schema v2.
package agentout

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/20Youssef10/golab/internal/logx"
)

var (
	// ErrUnsupportedFormat means --output named an unknown format.
	ErrUnsupportedFormat = errors.New("agentout: unsupported output format")
	// ErrInvalidType means an event type outside info|output|error was used.
	ErrInvalidType = errors.New("agentout: invalid event type")
	// ErrNilWriter means no destination was supplied.
	ErrNilWriter = errors.New("agentout: nil writer")
)

// Schema is the current version of the output contract.
const Schema = "golab/1"

// Event is one typed record. Field order is fixed by this struct.
type Event struct {
	Schema string `json:"schema"`
	Type   string `json:"type"`
	Text   string `json:"text"`
}

// NewWriter wraps w for the requested format. The returned io.Writer also
// works with Emit and Close; text mode returns w itself.
func NewWriter(w io.Writer, format string) (io.Writer, error) {
	if w == nil {
		return nil, ErrNilWriter
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		return w, nil
	case "ndjson":
		return &streamWriter{w: w}, nil
	case "json":
		return &documentWriter{w: w}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}
}

// Emit writes one typed event. For a text-mode writer it writes the redacted
// text verbatim so existing human-readable output is unchanged.
func Emit(w io.Writer, typ, text string) error {
	if w == nil {
		return ErrNilWriter
	}
	switch typ {
	case "info", "output", "error":
	default:
		return fmt.Errorf("%w: %q", ErrInvalidType, typ)
	}
	text = logx.Redact(text)
	switch x := w.(type) {
	case *streamWriter:
		return x.emit(Event{Schema: Schema, Type: typ, Text: text})
	case *documentWriter:
		return x.emit(Event{Schema: Schema, Type: typ, Text: text})
	default:
		_, err := io.WriteString(w, text)
		return err
	}
}

// IsWrapped reports whether w is already an agentout writer (installed by the
// root --output flag). Commands must not wrap such a writer a second time or
// the emitted event would be nested inside another event.
func IsWrapped(w io.Writer) bool {
	switch w.(type) {
	case *streamWriter, *documentWriter:
		return true
	}
	return false
}

// Close finalises a json document. It is a no-op for text and ndjson writers
// and safe to call more than once.
func Close(w io.Writer) error {
	if c, ok := w.(interface{ closeDocument() error }); ok {
		return c.closeDocument()
	}
	return nil
}

type streamWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *streamWriter) Write(p []byte) (int, error) {
	if err := s.emit(Event{Schema: Schema, Type: "output", Text: logx.Redact(string(p))}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *streamWriter) emit(e Event) error {
	b, err := encodeEvent(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.w.Write(b)
	return err
}

type documentWriter struct {
	mu      sync.Mutex
	w       io.Writer
	started bool
	closed  bool
}

func (d *documentWriter) Write(p []byte) (int, error) {
	if err := d.emit(Event{Schema: Schema, Type: "output", Text: logx.Redact(string(p))}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (d *documentWriter) emit(e Event) error {
	b, err := encodeEvent(e)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("agentout: write after close")
	}
	if !d.started {
		if _, err := io.WriteString(d.w, `{"schema":"`+Schema+`","events":[`); err != nil {
			return err
		}
		d.started = true
	} else if _, err := io.WriteString(d.w, ","); err != nil {
		return err
	}
	_, err = d.w.Write(b)
	return err
}

func (d *documentWriter) closeDocument() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if !d.started {
		_, err := io.WriteString(d.w, `{"schema":"`+Schema+`","events":[]}`+"\n")
		return err
	}
	_, err := io.WriteString(d.w, "]}\n")
	return err
}

func encodeEvent(e Event) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
