package history

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/20Youssef10/golab/internal/nb"
)

// Export renders events in the named format ("ipynb", "md", "txt" or
// "jsonl"; a leading dot is accepted). The session name used in headers and
// the notebook title is taken from a "session_name" / "session" payload field
// when present; use ExportWithSession to set it explicitly.
func Export(events []Event, format string) ([]byte, error) {
	return ExportWithSession(events, format, sessionNameFrom(events))
}

// ExportWithSession is Export with an explicit session name, matching the
// session_name argument of upstream converter.py::export_history.
func ExportWithSession(events []Event, format, sessionName string) ([]byte, error) {
	format = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
	switch format {
	case "ipynb":
		return exportIPynb(events, sessionName)
	case "md":
		return exportMarkdown(events, sessionName), nil
	case "txt":
		return exportText(events, sessionName)
	case "jsonl":
		return exportJSONL(events)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}
}

func sessionNameFrom(events []Event) string {
	for _, e := range events {
		if v := e.stringField("session_name"); v != "" {
			return v
		}
		if v := e.stringField("session"); v != "" {
			return v
		}
	}
	return ""
}

func exportJSONL(events []Event) ([]byte, error) {
	var buf bytes.Buffer
	for _, event := range events {
		b, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func exportMarkdown(events []Event, session string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Colab Session: %s\n\n", session)
	for _, e := range events {
		ts := displayTimestamp(e)
		switch e.EventType {
		case EventExecution:
			code := e.stringField("code")
			fmt.Fprintf(&b, "### Execution (%s)\n%s\n", ts, fencedBlock(code, "python"))
			for _, out := range e.outputs() {
				if text, ok := out["text"]; ok {
					fmt.Fprintf(&b, "**Output**:\n%s\n", fencedBlock(fmt.Sprint(text), ""))
				}
			}
		case EventSessionCreated:
			fmt.Fprintf(&b, "## Session Created: %s\n- Endpoint: `%s`\n\n", ts, e.stringField("endpoint"))
		case EventFileOperation:
			fmt.Fprintf(&b, "*File Operation*: `%s` on `%s`\n\n",
				e.stringField("op"), fileTarget(e))
		}
	}
	return []byte(b.String())
}

const minMarkdownFence = 3

// fencedBlock wraps content in a backtick fence that cannot be closed early by
// a backtick run inside the content, and guarantees the closing fence starts
// on its own line even when the content has no trailing newline. The content
// itself is emitted verbatim.
func fencedBlock(content, lang string) string {
	fenceLen := minMarkdownFence
	run := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '`' {
			run++
			if run >= fenceLen {
				fenceLen = run + 1
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", fenceLen)
	var b strings.Builder
	b.WriteString(fence)
	b.WriteString(lang)
	b.WriteByte('\n')
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(fence)
	b.WriteByte('\n')
	return b.String()
}

func exportText(events []Event, session string) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Colab Session: %s\n%s\n\n", session, strings.Repeat("=", 20))
	for _, e := range events {
		fmt.Fprintf(&b, "[%s] %s: ", displayTimestamp(e), strings.ToUpper(e.EventType))
		if e.EventType == EventExecution {
			b.WriteString(strings.TrimSpace(e.stringField("code")))
			b.WriteByte('\n')
			continue
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

func fileTarget(e Event) string {
	if v := e.stringField("path"); v != "" {
		return v
	}
	return e.stringField("remote")
}

func exportIPynb(events []Event, session string) ([]byte, error) {
	n := nb.NewNotebook()
	n.Metadata["kernelspec"] = map[string]any{
		"display_name": "Python 3 (Google Colab)",
		"language":     "python",
		"name":         "python3",
	}
	title := fmt.Sprintf("# Colab Session: %s\nGenerated from colab-cli history log.", session)
	n.Cells = append(n.Cells, nb.NewMarkdownCell(title))

	for _, e := range events {
		ts := displayTimestamp(e)
		switch e.EventType {
		case EventSessionCreated:
			meta := fmt.Sprintf("**Session Created**: %s\n- Endpoint: `%s`\n- Hardware: `%s`",
				ts, e.stringField("endpoint"), e.stringField("accelerator"))
			n.Cells = append(n.Cells, nb.NewMarkdownCell(meta))

		case EventExecution:
			code := e.stringField("code")
			if e.stringField("source") == "piped" && !strings.HasPrefix(code, "!") {
				code = "%%bash\n" + code
			}
			cell := nb.NewCodeCell(code)
			cell.Outputs = mapOutputs(e.outputs())
			n.Cells = append(n.Cells, cell)

		case EventAutomation:
			op := e.stringField("op")
			n.Cells = append(n.Cells, nb.NewMarkdownCell(
				fmt.Sprintf("### Automation: %s (%s)", op, ts)))
			if code := e.stringField("code"); code != "" {
				n.Cells = append(n.Cells, nb.NewCodeCell(code))
			}

		case EventAutomationResult:
			if outs := e.outputs(); len(outs) > 0 {
				cell := nb.NewCodeCell("# Result of previous automation")
				cell.Outputs = mapOutputs(outs)
				n.Cells = append(n.Cells, cell)
			}

		case EventFileOperation:
			n.Cells = append(n.Cells, nb.NewMarkdownCell(
				fmt.Sprintf("*File Operation*: `%s` on `%s`", e.stringField("op"), fileTarget(e))))

		case EventStdinRequest:
			n.Cells = append(n.Cells, nb.NewMarkdownCell(
				fmt.Sprintf("> **Input Requested**: %s", e.stringField("prompt"))))

		case EventInputReply:
			n.Cells = append(n.Cells, nb.NewMarkdownCell(
				fmt.Sprintf("> **User Input**: `%s`", e.stringField("value"))))
		}
	}
	return nb.Marshal(n)
}

// mapOutputs ports converter.py::_map_outputs: text → stream(stdout),
// data → display_data, error → error. Unmapped outputs are skipped.
func mapOutputs(outs []map[string]any) []nb.Output {
	result := make([]nb.Output, 0, len(outs))
	for _, o := range outs {
		if text, ok := o["text"]; ok {
			result = append(result, nb.NewStreamOutput("stdout", fmt.Sprint(text)))
			continue
		}
		if data, ok := o["data"].(map[string]any); ok {
			result = append(result, nb.NewDisplayDataOutput(data))
			continue
		}
		if kind, _ := o["output_type"].(string); kind == "error" {
			ename, _ := o["ename"].(string)
			if ename == "" {
				ename = "Error"
			}
			evalue, _ := o["evalue"].(string)
			var traceback []string
			if raw, ok := o["traceback"].([]any); ok {
				for _, v := range raw {
					traceback = append(traceback, fmt.Sprint(v))
				}
			}
			result = append(result, nb.NewErrorOutput(ename, evalue, traceback))
		}
	}
	return result
}
