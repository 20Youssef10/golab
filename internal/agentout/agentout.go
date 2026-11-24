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

// DataItem is one non-image MIME bundle entry of a cell record. The map is
// kept open-ended because the upstream contract forwards arbitrary MIME types
// (text/plain, text/html, application/json, …) alongside "output_type".
type DataItem map[string]any

// CellError is the sanitized error detail of a failed cell.
type CellError struct {
	EName     string `json:"ename"`
	EValue    string `json:"evalue"`
	Traceback string `json:"traceback"`
}

// CellResult is one per-cell record of `golab exec --json`. Field order is
// part of the contract and mirrors upstream PR #237.
type CellResult struct {
	CellIndex int        `json:"cell_index"`
	CellID    *string    `json:"cell_id"`
	Status    string     `json:"status"`
	Stdout    string     `json:"stdout"`
	Stderr    string     `json:"stderr"`
	Data      []DataItem `json:"data"`
	Images    []string   `json:"images"`
	Error     *CellError `json:"error"`
}

// CellWriter emits CellResult records with the same NDJSON/JSON framing as
// the typed event writers. With format "ndjson" (the default, and what
// `exec --json` uses) each record is one line; with "json" all records form a
// single document finalised by Close. If w is already an agentout writer the
// root --output flag owns framing, so records are forwarded as typed output
// events instead of being wrapped a second time.
type CellWriter struct {
	mu        sync.Mutex
	w         io.Writer
	document  bool
	viaEvents bool
	started   bool
	closed    bool
}

// NewCellWriter wraps w for per-cell records.
func NewCellWriter(w io.Writer, format string) (*CellWriter, error) {
	if w == nil {
		return nil, ErrNilWriter
	}
	if IsWrapped(w) {
		return &CellWriter{w: w, viaEvents: true}, nil
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "ndjson":
		return &CellWriter{w: w}, nil
	case "json":
		return &CellWriter{w: w, document: true}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}
}

// Emit writes one cell record after a defensive redaction pass.
func (c *CellWriter) Emit(rec CellResult) error {
	if c == nil {
		return ErrNilWriter
	}
	b, err := encodeJSON(redactCellResult(rec))
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("agentout: cell write after close")
	}
	if c.viaEvents {
		return Emit(c.w, "output", string(b))
	}
	if c.document {
		if !c.started {
			if _, err := io.WriteString(c.w, `{"schema":"`+Schema+`","cells":[`); err != nil {
				return err
			}
			c.started = true
		} else if _, err := io.WriteString(c.w, ","); err != nil {
			return err
		}
		_, err = c.w.Write(b)
		return err
	}
	b = append(b, '\n')
	_, err = c.w.Write(b)
	return err
}

// Close finalises a cell document. It is a no-op for NDJSON and for a wrapped
// writer (whose owner finalises the enclosing document).
func (c *CellWriter) Close() error {
	if c == nil || c.viaEvents || !c.document {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if !c.started {
		_, err := io.WriteString(c.w, `{"schema":"`+Schema+`","cells":[]}`+"\n")
		return err
	}
	_, err := io.WriteString(c.w, "]}\n")
	return err
}

func redactCellResult(r CellResult) CellResult {
	if r.Data == nil {
		r.Data = []DataItem{}
	}
	if r.Images == nil {
		r.Images = []string{}
	}
	r.Stdout = logx.Redact(r.Stdout)
	r.Stderr = logx.Redact(r.Stderr)
	if r.CellID != nil {
		id := logx.Redact(*r.CellID)
		r.CellID = &id
	}
	for i := range r.Images {
		r.Images[i] = logx.Redact(r.Images[i])
	}
	data := make([]DataItem, len(r.Data))
	for i, item := range r.Data {
		clean := DataItem{}
		for k, v := range item {
			clean[logx.Redact(k)] = redactCellValue(v)
		}
		data[i] = clean
	}
	r.Data = data
	if r.Error != nil {
		e := *r.Error
		e.EName = logx.Redact(e.EName)
		e.EValue = logx.Redact(e.EValue)
		e.Traceback = logx.Redact(e.Traceback)
		r.Error = &e
	}
	return r
}

func redactCellValue(v any) any {
	switch x := v.(type) {
	case string:
		return logx.Redact(x)
	case []any:
		for i := range x {
			x[i] = redactCellValue(x[i])
		}
		return x
	case map[string]any:
		for k, value := range x {
			x[k] = redactCellValue(value)
		}
		return x
	default:
		return v
	}
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

func encodeEvent(e Event) ([]byte, error) { return encodeJSON(e) }

// encodeJSON shares the compact, HTML-safe encoding used by both event and
// cell-record writers so framing bytes are identical across the two schemas.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
