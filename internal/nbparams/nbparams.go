// Package nbparams implements the papermill-style parameter injection used by
// `golab run -p`. Values become Python literals (JSON string escaping is a
// subset of Python's), never spliced source, and assignments are inserted into
// a new code cell immediately after the cell tagged `parameters`.
package nbparams

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/20Youssef10/golab/internal/nb"
	"go.yaml.in/yaml/v3"
)

var (
	// ErrNoParametersCell means -p was used on a notebook without a tagged cell.
	ErrNoParametersCell = errors.New("nbparams: notebook has no cell tagged 'parameters'")
	// ErrInvalidKey means a parameter name is not a Python identifier.
	ErrInvalidKey = errors.New("nbparams: invalid parameter name")
	// ErrUnsupportedFormat means a params file is neither JSON nor YAML.
	ErrUnsupportedFormat = errors.New("nbparams: unsupported params file format")
)

var keyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Assignment is one `NAME = <python literal>` line.
type Assignment struct {
	Name    string
	Literal string
}

// quotePython returns a round-trippable Python/JSON string literal. Disabling
// HTML escaping keeps it identical to the CLI's `pyRepr` helper.
func quotePython(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func checkKey(name string) error {
	if !keyRE.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidKey, name)
	}
	return nil
}

// ParseFlags parses repeatable `NAME=VALUE` options. Like `run --env`, every
// value is a string literal so user text can never become executable source.
func ParseFlags(items []string) ([]Assignment, error) {
	out := make([]Assignment, 0, len(items))
	for _, item := range items {
		i := strings.IndexByte(item, '=')
		if i < 0 {
			return nil, fmt.Errorf("%w: expected NAME=VALUE", ErrInvalidKey)
		}
		name := item[:i]
		if err := checkKey(name); err != nil {
			return nil, err
		}
		out = append(out, Assignment{Name: name, Literal: quotePython(item[i+1:])})
	}
	return out, nil
}

// pyLiteral maps a JSON/YAML value to a Python literal. Depth is bounded so a
// hostile params file cannot produce pathological recursion.
func pyLiteral(v any, depth int) (string, error) {
	if depth > 32 {
		return "", errors.New("nbparams: value nested too deeply")
	}
	switch x := v.(type) {
	case nil:
		return "None", nil
	case bool:
		if x {
			return "True", nil
		}
		return "False", nil
	case string:
		return quotePython(x), nil
	case json.Number:
		return string(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			lit, err := pyLiteral(item, depth+1)
			if err != nil {
				return "", err
			}
			parts = append(parts, lit)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			if err := checkKey(k); err != nil {
				return "", err
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			lit, err := pyLiteral(x[k], depth+1)
			if err != nil {
				return "", err
			}
			parts = append(parts, quotePython(k)+": "+lit)
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	default:
		return "", fmt.Errorf("nbparams: unsupported value type %T", v)
	}
}

// ParseFile reads a JSON or YAML params file using parsers already present in
// go.mod. No new dependency is introduced for this feature.
func ParseFile(data []byte, ext string) ([]Assignment, error) {
	var raw map[string]any
	switch strings.ToLower(ext) {
	case ".json":
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("nbparams: decode JSON params: %w", err)
		}
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("nbparams: decode YAML params: %w", err)
		}
	default:
		return nil, fmt.Errorf("%w: %q (use .json, .yaml or .yml)", ErrUnsupportedFormat, ext)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		if err := checkKey(k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Assignment, 0, len(keys))
	for _, k := range keys {
		lit, err := pyLiteral(raw[k], 0)
		if err != nil {
			return nil, err
		}
		out = append(out, Assignment{Name: k, Literal: lit})
	}
	return out, nil
}

// Merge combines file values with CLI values. CLI flags win, including the
// last repeated -p; the result is sorted by name for deterministic injection.
func Merge(file, flags []Assignment) []Assignment {
	byName := make(map[string]string, len(file)+len(flags))
	for _, a := range file {
		byName[a.Name] = a.Literal
	}
	for _, a := range flags {
		byName[a.Name] = a.Literal
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Assignment, 0, len(names))
	for _, name := range names {
		out = append(out, Assignment{Name: name, Literal: byName[name]})
	}
	return out
}

func hasParameterTag(cell nb.Cell) bool {
	tags, ok := cell.Metadata["tags"]
	if !ok {
		return false
	}
	switch t := tags.(type) {
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok && s == "parameters" {
				return true
			}
		}
	case []string:
		for _, s := range t {
			if s == "parameters" {
				return true
			}
		}
	}
	return false
}

// Inject inserts a deterministic parameter cell directly after the first code
// cell tagged `parameters`. Without that tag it returns ErrNoParametersCell so
// the CLI can fail as a usage error before any billable VM is allocated.
func Inject(n *nb.Notebook, as []Assignment) error {
	if n == nil || len(as) == 0 {
		return nil
	}
	idx := -1
	for i := range n.Cells {
		if n.Cells[i].CellType == "code" && hasParameterTag(n.Cells[i]) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNoParametersCell
	}
	ordered := Merge(nil, as)
	var b strings.Builder
	b.WriteString("# injected by golab run -p\n")
	for _, a := range ordered {
		fmt.Fprintf(&b, "%s = %s\n", a.Name, a.Literal)
	}
	cell := nb.NewCodeCell(b.String())
	cell.ID = "golab-injected-params"
	for taken := true; taken; {
		taken = false
		for _, existing := range n.Cells {
			if existing.ID == cell.ID {
				cell.ID += "-x"
				taken = true
				break
			}
		}
	}
	n.Cells = append(n.Cells, nb.Cell{})
	copy(n.Cells[idx+2:], n.Cells[idx+1:])
	n.Cells[idx+1] = cell
	return nil
}
