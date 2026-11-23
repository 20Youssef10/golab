package nb

import (
	"encoding/json"
	"strings"
	"testing"
)

// The notebook shape here mirrors the JSON asserted by upstream
// tests/test_log_export.py::TestLogExport::test_log_export (cells, source,
// outputs) and tests/test_ipynb_exec.py::test_exec_ipynb_output_format
// (# @title labels / cell-id fallback).
func TestNotebookRoundTripPreservesUnknownFields(t *testing.T) {
	raw := []byte(`{
	  "cells": [{
	    "cell_type": "code",
	    "execution_count": 3,
	    "id": "abc123",
	    "metadata": {"collapsed": true},
	    "outputs": [{
	      "output_type": "stream",
	      "name": "stdout",
	      "text": ["hello\n", "world\n"],
	      "custom_output": 7
	    }],
	    "source": ["print('hello')\n", "print('world')"],
	    "custom_cell": "keep-me"
	  }],
	  "metadata": {"kernelspec": {"name": "python3"}, "custom_nb": 42},
	  "nbformat": 4,
	  "nbformat_minor": 5,
	  "custom_top": "yes"
	}`)

	notebook, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if notebook.NBFormat != 4 || notebook.NBFormatMinor != 5 {
		t.Fatalf("nbformat = %d.%d", notebook.NBFormat, notebook.NBFormatMinor)
	}
	if len(notebook.Cells) != 1 {
		t.Fatalf("cells = %d", len(notebook.Cells))
	}
	cell := notebook.Cells[0]
	if cell.ID != "abc123" || cell.CellType != "code" {
		t.Fatalf("cell = %+v", cell)
	}
	if string(cell.Source) != "print('hello')\nprint('world')" {
		t.Fatalf("source = %q", cell.Source)
	}
	if cell.ExecutionCount == nil || *cell.ExecutionCount != 3 {
		t.Fatalf("execution_count = %v", cell.ExecutionCount)
	}
	if len(cell.Outputs) != 1 || string(cell.Outputs[0].Text) != "hello\nworld\n" {
		t.Fatalf("outputs = %+v", cell.Outputs)
	}

	out, err := Marshal(notebook)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["custom_top"] != "yes" {
		t.Errorf("top-level unknown field lost: %v", got["custom_top"])
	}
	meta, _ := got["metadata"].(map[string]any)
	if meta["custom_nb"] != float64(42) {
		t.Errorf("notebook metadata unknown field lost: %v", meta["custom_nb"])
	}
	cells, _ := got["cells"].([]any)
	gotCell, _ := cells[0].(map[string]any)
	if gotCell["custom_cell"] != "keep-me" {
		t.Errorf("cell unknown field lost: %v", gotCell["custom_cell"])
	}
	gotOutputs, _ := gotCell["outputs"].([]any)
	gotOutput, _ := gotOutputs[0].(map[string]any)
	if gotOutput["custom_output"] != float64(7) {
		t.Errorf("output unknown field lost: %v", gotOutput["custom_output"])
	}

	// A second round-trip must be stable.
	again, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(out2) != string(out) {
		t.Fatalf("second round-trip differs:\n%s\n%s", out, out2)
	}
}

// Ports the label logic of upstream
// tests/test_ipynb_exec.py::test_exec_ipynb_output_format: "# @title My Special
// Cell" wins, otherwise the cell id is the label.
func TestCodeCellsPreserveOrderAndTitle(t *testing.T) {
	n := NewNotebook()
	c1 := NewCodeCell("# @title My Special Cell\nprint('hello')")
	c1.ID = "my-cell-id-123"
	c2 := NewMarkdownCell("some notes")
	c2.ID = "md-1"
	c3 := NewCodeCell("  #   @title   Spaced Title  \nprint('world')")
	c3.ID = "fallback-id-456"
	c4 := NewCodeCell("print(3)")
	c4.ID = "last-id"
	n.Cells = []Cell{c1, c2, c3, c4}

	cells := n.CodeCells()
	if len(cells) != 3 {
		t.Fatalf("code cells = %d, want 3", len(cells))
	}
	wantSources := []string{
		"# @title My Special Cell\nprint('hello')",
		"  #   @title   Spaced Title  \nprint('world')",
		"print(3)",
	}
	wantLabels := []string{"My Special Cell", "Spaced Title", "last-id"}
	for i, c := range cells {
		if string(c.Source) != wantSources[i] {
			t.Errorf("cell %d source = %q, want %q", i, c.Source, wantSources[i])
		}
		if got := c.Label(); got != wantLabels[i] {
			t.Errorf("cell %d label = %q, want %q", i, got, wantLabels[i])
		}
	}
	if got := Title("x = 1\n# @title Later\nprint(x)"); got != "Later" {
		t.Errorf("multiline Title = %q", got)
	}
	if got := Title("print('no title')"); got != "" {
		t.Errorf("Title without marker = %q", got)
	}
}

func TestSourceStringAndListForms(t *testing.T) {
	for _, raw := range []string{
		`{"cell_type":"markdown","id":"x","metadata":{},"source":"one\ntwo"}`,
		`{"cell_type":"markdown","id":"x","metadata":{},"source":["one\n","two"]}`,
	} {
		cell, err := ParseCell([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if string(cell.Source) != "one\ntwo" {
			t.Fatalf("source = %q", cell.Source)
		}
		out, err := json.Marshal(cell)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"source":"one\ntwo"`) {
			t.Fatalf("marshalled source = %s", out)
		}
	}
}

func TestAttachmentsRoundTrip(t *testing.T) {
	raw := []byte(`{"cell_type":"markdown","id":"m1","metadata":{},` +
		`"source":"![x](attachment:image.png)",` +
		`"attachments":{"image.png":{"image/png":"iVBORw0KGgo="}}}`)
	cell, err := ParseCell(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cell.Attachments["image.png"]; !ok {
		t.Fatalf("attachments = %v", cell.Attachments)
	}
	out, err := json.Marshal(cell)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "attachment:image.png") {
		t.Fatalf("attachment source lost: %s", out)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["attachments"]; !ok {
		t.Fatalf("attachments lost: %s", out)
	}
}

func TestNewNotebookDefaults(t *testing.T) {
	n := NewNotebook()
	out, err := Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["nbformat"] != float64(4) || got["nbformat_minor"] != float64(5) {
		t.Fatalf("defaults = %s", out)
	}
	if _, ok := got["cells"].([]any); !ok {
		t.Fatalf("cells must be an array: %s", out)
	}
}
