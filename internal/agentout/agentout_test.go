package agentout

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/logx"
)

// TestNewWriterTextModeIsUnchanged keeps text mode byte-identical: wrapping a
// command's stdout must not change today's human-readable behaviour.
func TestNewWriterTextModeIsUnchanged(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, "text")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "plain output\n"); err != nil {
		t.Fatal(err)
	}
	if err := Emit(w, "error", "diagnostic"); err != nil {
		t.Fatal(err)
	}
	if err := Close(w); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "plain output\ndiagnostic" {
		t.Fatalf("text mode changed: %q", got)
	}
}

// TestNewWriterNDJSONTypedEvents pins the stable field order and one-event-per-
// line framing. Consumers must be able to parse every line with jq.
func TestNewWriterNDJSONTypedEvents(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if err := Emit(w, "info", "starting"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "program output"); err != nil {
		t.Fatal(err)
	}
	if err := Emit(w, "error", "failed"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines: %q", len(lines), buf.String())
	}
	wantPrefix := []string{
		`{"schema":"golab/1","type":"info","text":"starting"}`,
		`{"schema":"golab/1","type":"output","text":"program output"}`,
		`{"schema":"golab/1","type":"error","text":"failed"}`,
	}
	for i, line := range lines {
		if line != wantPrefix[i] {
			t.Errorf("line %d = %s, want %s", i, line, wantPrefix[i])
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d is not JSON: %v", i, err)
		}
	}
}

// TestNewWriterJSONSingleDocument verifies --output json emits exactly one
// parseable document after Close.
func TestNewWriterJSONSingleDocument(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, "json")
	if err != nil {
		t.Fatal(err)
	}
	if err := Emit(w, "info", "a"); err != nil {
		t.Fatal(err)
	}
	if err := Emit(w, "output", "b"); err != nil {
		t.Fatal(err)
	}
	if err := Close(w); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema string  `json:"schema"`
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not a single JSON document: %v (%s)", err, buf.String())
	}
	if doc.Schema != "golab/1" || len(doc.Events) != 2 {
		t.Fatalf("document = %+v", doc)
	}
}

// TestNewWriterRedactsSecrets is the leak golden: any free-form text crossing
// the output boundary is passed through logx.Redact first.
func TestNewWriterRedactsSecrets(t *testing.T) {
	const secret = "planted-agentout-secret-1234"
	raw := `{"token":"` + secret + `","access_token":"` + secret + `"} Bearer abcdefghijklmnopqrstuvwxyz`
	if !strings.Contains(logx.Redact(raw), "REDACTED") {
		t.Fatal("redaction fixture is not a redaction case")
	}
	for _, format := range []string{"json", "ndjson"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			w, err := NewWriter(&buf, format)
			if err != nil {
				t.Fatal(err)
			}
			if err := Emit(w, "output", raw); err != nil {
				t.Fatal(err)
			}
			if err := Close(w); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(buf.String(), secret) || strings.Contains(buf.String(), "abcdefghijklmnopqrstuvwxyz") {
				t.Fatalf("secret leaked in %s mode: %s", format, buf.String())
			}
		})
	}
}

// TestNewWriterRejectsBadInputs covers stable typed errors for bad formats and
// event types instead of panics or silent mis-formatting.
func TestNewWriterRejectsBadInputs(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewWriter(&buf, "xml"); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("error = %v, want ErrUnsupportedFormat", err)
	}
	w, err := NewWriter(&buf, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if err := Emit(w, "verbose", "x"); !errors.Is(err, ErrInvalidType) {
		t.Fatalf("error = %v, want ErrInvalidType", err)
	}
}
