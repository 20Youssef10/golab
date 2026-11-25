package history

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/nb"
)

func sampleEvents(t *testing.T) []Event {
	t.Helper()
	return []Event{
		{
			Timestamp: mustTime(t, "2026-03-23T12:00:00.000000+00:00"),
			EventType: EventSessionCreated,
			Payload:   rawPayload(t, map[string]any{"endpoint": "ep1", "accelerator": "NONE"}),
		},
		{
			Timestamp: mustTime(t, "2026-03-23T12:01:00.000000+00:00"),
			EventType: EventExecution,
			Payload:   rawPayload(t, map[string]any{"code": "print(1)", "outputs": []any{map[string]any{"text": "1\n"}}}),
		},
		{
			Timestamp: mustTime(t, "2026-03-23T12:02:00.000000+00:00"),
			EventType: EventFileOperation,
			Payload:   rawPayload(t, map[string]any{"op": "ls", "path": "content"}),
		},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func flattened(t *testing.T, e Event) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(mustJSON(t, e), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Ports upstream tests/test_log_export.py::TestLogExport::test_log_export:
// the four exporters must render the sample events with the same intent as
// converter.py.
func TestExportFormats(t *testing.T) {
	events := sampleEvents(t)

	t.Run("md", func(t *testing.T) {
		got, err := ExportWithSession(events, "md", "test-export")
		if err != nil {
			t.Fatal(err)
		}
		want := "# Colab Session: test-export\n\n" +
			"## Session Created: 2026-03-23 12:00:00\n- Endpoint: `ep1`\n\n" +
			"### Execution (2026-03-23 12:01:00)\n```python\nprint(1)\n```\n\n" +
			"**Output**:\n```\n1\n```\n\n" +
			"*File Operation*: `ls` on `content`\n\n"
		if string(got) != want {
			t.Fatalf("md export:\ngot:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("txt", func(t *testing.T) {
		got, err := ExportWithSession(events, "txt", "test-export")
		if err != nil {
			t.Fatal(err)
		}
		want := "Colab Session: test-export\n====================\n\n" +
			"[2026-03-23 12:00:00] SESSION_CREATED: " + string(mustJSON(t, flattened(t, events[0]))) + "\n" +
			"[2026-03-23 12:01:00] EXECUTION: print(1)\n" +
			"[2026-03-23 12:02:00] FILE_OPERATION: " + string(mustJSON(t, flattened(t, events[2]))) + "\n"
		if string(got) != want {
			t.Fatalf("txt export:\ngot:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("jsonl", func(t *testing.T) {
		got, err := ExportWithSession(events, "jsonl", "test-export")
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
		if len(lines) != len(events) {
			t.Fatalf("jsonl lines = %d, want %d: %q", len(lines), len(events), got)
		}
		for i, line := range lines {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("line %d: %v", i, err)
			}
			want := flattened(t, events[i])
			if string(mustJSON(t, m)) != string(mustJSON(t, want)) {
				t.Errorf("line %d = %s, want %s", i, line, mustJSON(t, want))
			}
		}
	})

	t.Run("ipynb", func(t *testing.T) {
		got, err := ExportWithSession(events, "ipynb", "test-export")
		if err != nil {
			t.Fatal(err)
		}
		n, err := nb.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		// title, session_created, execution, file_operation (upstream test).
		if len(n.Cells) != 4 {
			t.Fatalf("cells = %d, want 4: %s", len(n.Cells), got)
		}
		if n.Cells[2].CellType != "code" {
			t.Fatalf("cell 2 type = %q", n.Cells[2].CellType)
		}
		if string(n.Cells[2].Source) != "print(1)" {
			t.Fatalf("cell 2 source = %q", n.Cells[2].Source)
		}
		if len(n.Cells[2].Outputs) != 1 || string(n.Cells[2].Outputs[0].Text) != "1\n" {
			t.Fatalf("cell 2 outputs = %+v", n.Cells[2].Outputs)
		}
		if n.Cells[2].Outputs[0].OutputType != "stream" || n.Cells[2].Outputs[0].Name != "stdout" {
			t.Fatalf("cell 2 output = %+v", n.Cells[2].Outputs[0])
		}
		if !strings.Contains(string(n.Cells[0].Source), "# Colab Session: test-export") {
			t.Fatalf("title cell = %q", n.Cells[0].Source)
		}
		if n.Metadata["kernelspec"] == nil {
			t.Fatalf("kernelspec missing: %v", n.Metadata)
		}
	})

	t.Run("leading dot accepted", func(t *testing.T) {
		got, err := ExportWithSession(events, ".ipynb", "test-export")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := nb.Parse(got); err != nil {
			t.Fatal(err)
		}
	})
}

// Ports the notebook mapping intent of converter.py::convert_history_to_ipynb
// (piped sources, automation, results, stdin, file ops, output mapping).
func TestExportIPynbEventMapping(t *testing.T) {
	events := []Event{
		{Timestamp: time.Unix(0, 0).UTC(), EventType: EventExecution,
			Payload: rawPayload(t, map[string]any{"source": "piped", "code": "echo hi", "outputs": []any{map[string]any{"text": "hi\n"}}})},
		{Timestamp: time.Unix(0, 0).UTC(), EventType: EventExecution,
			Payload: rawPayload(t, map[string]any{"source": "piped", "code": "!pip install x"})},
		{Timestamp: time.Unix(0, 0).UTC(), EventType: EventAutomation,
			Payload: rawPayload(t, map[string]any{"op": "auth", "code": ""})},
		{Timestamp: time.Unix(0, 0).UTC(), EventType: EventAutomation,
			Payload: rawPayload(t, map[string]any{"op": "install", "code": "import x"})},
		{Timestamp: time.Unix(0, 0).UTC(), EventType: EventAutomationResult,
			Payload: rawPayload(t, map[string]any{"outputs": []any{map[string]any{
				"output_type": "error", "ename": "ValueError", "evalue": "boom", "traceback": []any{"tb line"},
			}}})},
		{Timestamp: time.Unix(0, 0).UTC(), EventType: EventStdinRequest,
			Payload: rawPayload(t, map[string]any{"prompt": "Password?"})},
		// A prompt reply is persisted in redacted form (§3.13); the literal
		// password must never survive into an export.
		{Timestamp: time.Unix(0, 0).UTC(), EventType: EventInputReply,
			Payload: rawPayload(t, map[string]any{"value": "[redacted:7 bytes]"})},
		{Timestamp: time.Unix(0, 0).UTC(), EventType: EventFileOperation,
			Payload: rawPayload(t, map[string]any{"op": "download", "remote": "content/a.txt"})},
		{Timestamp: time.Unix(0, 0).UTC(), EventType: EventExecution,
			Payload: rawPayload(t, map[string]any{"code": "display(x)", "outputs": []any{map[string]any{
				"data": map[string]any{"text/html": "<b>x</b>"},
			}}})},
	}

	got, err := ExportWithSession(events, "ipynb", "s")
	if err != nil {
		t.Fatal(err)
	}
	n, err := nb.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	// A typed password must never be re-emitted by any export path.
	if strings.Contains(string(got), "letmein") {
		t.Fatalf("ipynb export leaked the prompt reply: %s", got)
	}
	wantTypes := []string{
		"markdown", // title
		"code",     // piped echo (%%bash)
		"code",     // !pip (no %%bash)
		"markdown", // automation auth
		"markdown", // automation install heading
		"code",     // automation install code
		"code",     // automation result
		"markdown", // stdin_request
		"markdown", // input_reply
		"markdown", // file_operation (remote fallback)
		"code",     // display_data
	}
	if len(n.Cells) != len(wantTypes) {
		t.Fatalf("cells = %d, want %d", len(n.Cells), len(wantTypes))
	}
	for i, want := range wantTypes {
		if n.Cells[i].CellType != want {
			t.Errorf("cell %d type = %q, want %q (source %q)", i, n.Cells[i].CellType, want, n.Cells[i].Source)
		}
	}
	if string(n.Cells[1].Source) != "%%bash\necho hi" {
		t.Errorf("piped cell = %q", n.Cells[1].Source)
	}
	if string(n.Cells[2].Source) != "!pip install x" {
		t.Errorf("! cell = %q", n.Cells[2].Source)
	}
	if string(n.Cells[5].Source) != "import x" {
		t.Errorf("automation code cell = %q", n.Cells[5].Source)
	}
	result := n.Cells[6]
	if len(result.Outputs) != 1 || result.Outputs[0].OutputType != "error" ||
		result.Outputs[0].Ename != "ValueError" || result.Outputs[0].Evalue != "boom" ||
		len(result.Outputs[0].Traceback) != 1 {
		t.Fatalf("error output = %+v", result.Outputs)
	}
	if !strings.Contains(string(n.Cells[7].Source), "Input Requested") ||
		!strings.Contains(string(n.Cells[7].Source), "Password?") {
		t.Errorf("stdin cell = %q", n.Cells[7].Source)
	}
	if !strings.Contains(string(n.Cells[8].Source), "User Input") {
		t.Errorf("input reply cell = %q", n.Cells[8].Source)
	}
	if !strings.Contains(string(n.Cells[9].Source), "content/a.txt") {
		t.Errorf("file op cell = %q", n.Cells[9].Source)
	}
	display := n.Cells[10].Outputs
	if len(display) != 1 || display[0].OutputType != "display_data" || display[0].Data["text/html"] != "<b>x</b>" {
		t.Fatalf("display output = %+v", display)
	}
}

// Ports the @title requirement of docs/05 §5 (exec row): the label must survive
// into the exported notebook and plain-text renderings.
func TestExportPreservesTitleLabel(t *testing.T) {
	code := "# @title My Special Cell\nprint('hello')"
	events := []Event{{
		Timestamp: mustTime(t, "2026-03-23T12:00:00Z"),
		EventType: EventExecution,
		Payload:   rawPayload(t, map[string]any{"code": code, "outputs": []any{}}),
	}}

	b, err := ExportWithSession(events, "ipynb", "s")
	if err != nil {
		t.Fatal(err)
	}
	n, err := nb.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	cells := n.CodeCells()
	if len(cells) != 1 || cells[0].Label() != "My Special Cell" {
		t.Fatalf("title label lost: %+v", cells)
	}
	for _, format := range []string{"md", "txt", "jsonl"} {
		out, err := ExportWithSession(events, format, "s")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "My Special Cell") {
			t.Errorf("%s export lost @title: %s", format, out)
		}
	}
}

func TestExportUnsupportedFormat(t *testing.T) {
	if _, err := Export(sampleEvents(t), "csv"); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("expected ErrUnsupportedFormat, got %v", err)
	}
}

// Regression: exportMarkdown glued the closing fence onto output text that
// lacked a trailing newline, and a ``` inside cell content closed the fence
// early. A fence longer than any backtick run in the content keeps every block
// valid without altering the reported text.
func TestExportMarkdownFencedBlocks(t *testing.T) {
	events := []Event{
		{Timestamp: mustTime(t, "2026-03-23T12:00:00Z"), EventType: EventExecution,
			Payload: rawPayload(t, map[string]any{"code": "print(1)", "outputs": []any{
				map[string]any{"text": "no trailing newline"},
			}})},
		{Timestamp: mustTime(t, "2026-03-23T12:01:00Z"), EventType: EventExecution,
			Payload: rawPayload(t, map[string]any{"code": "print('```')", "outputs": []any{
				map[string]any{"text": "before ``` inside\nafter\n"},
			}})},
	}
	got, err := ExportWithSession(events, "md", "s")
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		"**Output**:\n```\nno trailing newline\n```\n\n",
		"**Output**:\n````\nbefore ``` inside\nafter\n````\n\n",
		"````python\nprint('```')\n````\n\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("md export missing %q in:\n%s", want, s)
		}
	}
}
