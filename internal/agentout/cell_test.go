package agentout

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Port of upstream tests/test_exec_json.py::test_exec_json_success_emits_one_record:
// the cell-record writer must keep the exact field set/order of the --json
// contract, with NDJSON framing so every line is independently parseable.
func TestCellWriterNDJSONRecordShape(t *testing.T) {
	id := "cell-a"
	var buf bytes.Buffer
	w, err := NewCellWriter(&buf, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	rec := CellResult{
		CellIndex: 0,
		CellID:    &id,
		Status:    "ok",
		Stdout:    "hello\n",
		Stderr:    "warn\n",
		Data:      []DataItem{{"output_type": "execute_result", "text/plain": "42"}},
		Images:    []string{},
		Error:     nil,
	}
	if err := w.Emit(rec); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(buf.String())
	want := `{"cell_index":0,"cell_id":"cell-a","status":"ok","stdout":"hello\n","stderr":"warn\n","data":[{"output_type":"execute_result","text/plain":"42"}],"images":[],"error":null}`
	if line != want {
		t.Fatalf("record = %s\nwant    = %s", line, want)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
}

// Port of upstream tests/test_exec_json.py::test_exec_json_notebook_emits_record_per_cell:
// json mode must produce a single document containing every cell record in
// order, finalised by Close.
func TestCellWriterJSONDocument(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewCellWriter(&buf, "json")
	if err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"ok", "error"} {
		rec := CellResult{CellIndex: i, Status: status}
		if status == "error" {
			rec.Error = &CellError{EName: "ValueError", EValue: "bad", Traceback: "ValueError: bad"}
		}
		if err := w.Emit(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cells []CellResult `json:"cells"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v (%s)", err, buf.String())
	}
	if len(doc.Cells) != 2 || doc.Cells[0].Status != "ok" || doc.Cells[1].Status != "error" {
		t.Fatalf("document = %+v", doc)
	}
}

// Leak golden: free-form record values crossing stdout are passed through the
// defensive redactor, like every other agentout event.
func TestCellWriterRedactsText(t *testing.T) {
	const secret = "planted-cell-secret-1234"
	raw := `{"token":"` + secret + `"}`
	for _, format := range []string{"json", "ndjson"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			w, err := NewCellWriter(&buf, format)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Emit(CellResult{Stdout: raw, Error: &CellError{EValue: raw}}); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(buf.String(), secret) {
				t.Fatalf("secret leaked in %s mode: %s", format, buf.String())
			}
		})
	}
}

func TestCellWriterRejectsBadInputs(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewCellWriter(&buf, "xml"); err == nil {
		t.Fatal("expected error for unsupported format")
	}
	if _, err := NewCellWriter(nil, "ndjson"); err == nil {
		t.Fatal("expected error for nil writer")
	}
}
