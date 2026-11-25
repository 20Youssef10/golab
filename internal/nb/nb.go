package nb

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// MultilineString is an nbformat "multiline string": it is written as a JSON
// string but may be read from either a string or an array of strings. In the
// list form every line except the last must end in "\n"; see joinMultilineLines.
type MultilineString string

// UnmarshalJSON accepts both the string and the list-of-lines forms used by
// different nbformat writers.
func (m *MultilineString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*m = MultilineString(s)
		return nil
	}
	var lines []string
	if err := json.Unmarshal(b, &lines); err != nil {
		return fmt.Errorf("nb: multiline string must be a string or an array of strings: %w", err)
	}
	joined, err := joinMultilineLines(lines)
	if err != nil {
		return err
	}
	*m = MultilineString(joined)
	return nil
}

// joinMultilineLines concatenates the list form of an nbformat multiline
// string. Concatenation with no separator is correct only when every line but
// the last carries its own terminating newline, as nbformat requires; without
// this check a hand-authored ["one", "two"] would silently become "onetwo".
// The offending index is reported without echoing line content, which may be
// notebook source and must not leak into an error message.
func joinMultilineLines(lines []string) (string, error) {
	for i := 0; i+1 < len(lines); i++ {
		if !strings.HasSuffix(lines[i], "\n") {
			return "", fmt.Errorf("nb: multiline string list element %d is missing a terminating newline", i)
		}
	}
	return strings.Join(lines, ""), nil
}

// Output is one cell output. Stream, display_data, execute_result and error
// outputs share the same struct; only the relevant fields are populated.
type Output struct {
	OutputType     string          `json:"output_type"`
	Name           string          `json:"name,omitempty"`
	Data           map[string]any  `json:"data,omitempty"`
	Text           MultilineString `json:"text,omitempty"`
	Ename          string          `json:"ename,omitempty"`
	Evalue         string          `json:"evalue,omitempty"`
	Traceback      []string        `json:"traceback,omitempty"`
	ExecutionCount *int            `json:"execution_count,omitempty"`

	extra map[string]json.RawMessage
}

var outputKeys = stringSet("output_type", "name", "data", "text", "ename", "evalue", "traceback", "execution_count")

// UnmarshalJSON keeps unknown output fields for round-tripping.
func (o *Output) UnmarshalJSON(b []byte) error {
	type plain Output
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*o = Output(p)
	return captureExtra(b, outputKeys, &o.extra)
}

// MarshalJSON re-emits unknown output fields.
func (o Output) MarshalJSON() ([]byte, error) {
	return mergeExtra(plainOutput(o), o.extra)
}

func plainOutput(o Output) any {
	type plain Output
	return plain(o)
}

// NewStreamOutput builds a stream output (stdout/stderr).
func NewStreamOutput(name, text string) Output {
	return Output{OutputType: "stream", Name: name, Text: MultilineString(text)}
}

// NewDisplayDataOutput builds a display_data output.
func NewDisplayDataOutput(data map[string]any) Output {
	return Output{OutputType: "display_data", Data: data}
}

// NewErrorOutput builds an error output.
func NewErrorOutput(ename, evalue string, traceback []string) Output {
	return Output{OutputType: "error", Ename: ename, Evalue: evalue, Traceback: traceback}
}

// Cell is a notebook cell. Code cells carry outputs and an execution count;
// markdown cells may carry attachments.
type Cell struct {
	ID             string                     `json:"id"`
	CellType       string                     `json:"cell_type"`
	Source         MultilineString            `json:"source"`
	Metadata       map[string]any             `json:"metadata"`
	Outputs        []Output                   `json:"outputs,omitempty"`
	ExecutionCount *int                       `json:"execution_count,omitempty"`
	Attachments    map[string]json.RawMessage `json:"attachments,omitempty"`

	extra map[string]json.RawMessage
}

var cellKeys = stringSet("id", "cell_type", "source", "metadata", "outputs", "execution_count", "attachments")

// UnmarshalJSON keeps unknown cell fields for round-tripping.
func (c *Cell) UnmarshalJSON(b []byte) error {
	type plain Cell
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Cell(p)
	if c.Metadata == nil {
		c.Metadata = map[string]any{}
	}
	return captureExtra(b, cellKeys, &c.extra)
}

// MarshalJSON re-emits unknown cell fields and keeps the nbformat-required
// outputs/execution_count keys on code cells.
func (c Cell) MarshalJSON() ([]byte, error) {
	type plain Cell
	p := plain(c)
	if p.Metadata == nil {
		p.Metadata = map[string]any{}
	}
	b, err := mergeExtra(p, c.extra)
	if err != nil {
		return nil, err
	}
	if c.CellType != "code" {
		return b, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if _, ok := m["outputs"]; !ok {
		m["outputs"] = json.RawMessage("[]")
	}
	if _, ok := m["execution_count"]; !ok {
		m["execution_count"] = json.RawMessage("null")
	}
	return json.Marshal(m)
}

// Label returns the cell's "# @title" label, falling back to its ID. This is
// the identifier printed by `colab exec` (docs/05 §5).
func (c Cell) Label() string {
	if label := Title(string(c.Source)); label != "" {
		return label
	}
	return c.ID
}

// titleRE matches upstream's TITLE_REGEX: ^\s*#\s*@title\s+(.*) with MULTILINE.
var titleRE = regexp.MustCompile(`(?m)^\s*#\s*@title\s+(.*)$`)

// Title extracts the first "# @title …" label from source, or "" when there
// is none. Leading/trailing spaces in the label are trimmed.
func Title(source string) string {
	m := titleRE.FindStringSubmatch(source)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// NewCodeCell builds a code cell with a fresh id.
func NewCodeCell(source string) Cell {
	return Cell{
		ID:       uuid.NewString(),
		CellType: "code",
		Source:   MultilineString(source),
		Metadata: map[string]any{},
		Outputs:  []Output{},
	}
}

// NewMarkdownCell builds a markdown cell with a fresh id.
func NewMarkdownCell(source string) Cell {
	return Cell{
		ID:       uuid.NewString(),
		CellType: "markdown",
		Source:   MultilineString(source),
		Metadata: map[string]any{},
	}
}

// Notebook is an nbformat v4.5 notebook.
type Notebook struct {
	Cells         []Cell         `json:"cells"`
	Metadata      map[string]any `json:"metadata"`
	NBFormat      int            `json:"nbformat"`
	NBFormatMinor int            `json:"nbformat_minor"`

	extra map[string]json.RawMessage
}

var notebookKeys = stringSet("cells", "metadata", "nbformat", "nbformat_minor")

// UnmarshalJSON keeps unknown top-level fields for round-tripping.
func (n *Notebook) UnmarshalJSON(b []byte) error {
	type plain Notebook
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*n = Notebook(p)
	if n.Cells == nil {
		n.Cells = []Cell{}
	}
	if n.Metadata == nil {
		n.Metadata = map[string]any{}
	}
	return captureExtra(b, notebookKeys, &n.extra)
}

// MarshalJSON re-emits unknown top-level fields and fills in nbformat
// defaults.
func (n Notebook) MarshalJSON() ([]byte, error) {
	type plain Notebook
	p := plain(n)
	if p.Cells == nil {
		p.Cells = []Cell{}
	}
	if p.Metadata == nil {
		p.Metadata = map[string]any{}
	}
	if p.NBFormat == 0 {
		p.NBFormat = 4
	}
	if p.NBFormatMinor == 0 {
		p.NBFormatMinor = 5
	}
	return mergeExtra(p, n.extra)
}

// NewNotebook returns an empty v4.5 notebook.
func NewNotebook() *Notebook {
	return &Notebook{
		Cells:         []Cell{},
		Metadata:      map[string]any{},
		NBFormat:      4,
		NBFormatMinor: 5,
	}
}

// CodeCells returns code cells in their original order.
func (n *Notebook) CodeCells() []Cell {
	var out []Cell
	for _, c := range n.Cells {
		if c.CellType == "code" {
			out = append(out, c)
		}
	}
	if out == nil {
		out = []Cell{}
	}
	return out
}

// Parse reads notebook JSON.
func Parse(data []byte) (*Notebook, error) {
	var n Notebook
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// ParseCell reads a single cell from JSON (used by notebook tooling).
func ParseCell(data []byte) (*Cell, error) {
	var c Cell
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Marshal writes notebook JSON.
func Marshal(n *Notebook) ([]byte, error) {
	if n == nil {
		return nil, fmt.Errorf("nb: nil notebook")
	}
	return json.Marshal(n)
}

func stringSet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func captureExtra(b []byte, known map[string]bool, dst *map[string]json.RawMessage) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for k, v := range raw {
		if !known[k] {
			if *dst == nil {
				*dst = map[string]json.RawMessage{}
			}
			(*dst)[k] = v
		}
	}
	return nil
}

func mergeExtra(v any, extra map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return b, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, value := range extra {
		if _, exists := m[k]; !exists {
			m[k] = value
		}
	}
	return json.Marshal(m)
}
