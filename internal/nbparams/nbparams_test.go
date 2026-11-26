package nbparams

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/nb"
)

// Papermill's -p convention: every value is quoted as a Python literal the
// same way `run --env` quotes values, so no user text can become source code.
func TestParseFlagsQuotesLiterals(t *testing.T) {
	got, err := ParseFlags([]string{`EPOCHS=10`, `LR=0.01`, `MSG=it's "quoted"` + "\nsecond"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"EPOCHS": `"10"`,
		"LR":     `"0.01"`,
		"MSG":    `"it's \"quoted\"\nsecond"`,
	}
	for _, a := range got {
		if a.Literal != want[a.Name] {
			t.Errorf("%s literal = %s, want %s", a.Name, a.Literal, want[a.Name])
		}
		var s string
		if err := json.Unmarshal([]byte(a.Literal), &s); err != nil {
			t.Errorf("%s is not a JSON/Python string literal: %v", a.Name, err)
		}
	}
	if _, err := ParseFlags([]string{"BAD"}); err == nil {
		t.Fatal("ParseFlags accepted a value without '='")
	}
	if _, err := ParseFlags([]string{"1BAD=1"}); err == nil {
		t.Fatal("ParseFlags accepted an invalid parameter name")
	}
}

// Params files use the parsers already in go.mod (JSON and go.yaml.in/yaml/v3)
// and map typed values to Python literals, never to shell or Python source.
func TestParseFileTypedLiterals(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "p.yaml")
	yamlSrc := "epochs: 10\nlr: 0.01\nflag: true\nname: \"x'y\"\nnested:\n  b: 2\n  a: 1\n"
	if err := os.WriteFile(yamlPath, []byte(yamlSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseFile(b, ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"epochs": "10", "lr": "0.01", "flag": "True",
		"name": `"x'y"`, "nested": `{"a": 1, "b": 2}`,
	}
	for _, a := range got {
		if a.Literal != want[a.Name] {
			t.Errorf("%s literal = %s, want %s", a.Name, a.Literal, want[a.Name])
		}
	}
	jsonPath := filepath.Join(dir, "p.json")
	if err := os.WriteFile(jsonPath, []byte(`{"epochs":3,"dropout":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	jb, _ := os.ReadFile(jsonPath)
	got, err = ParseFile(jb, ".json")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, a := range got {
		m[a.Name] = a.Literal
	}
	if m["epochs"] != "3" || m["dropout"] != "None" {
		t.Fatalf("json params = %v", m)
	}
	if _, err := ParseFile([]byte("x: 1"), ".toml"); err == nil {
		t.Fatal("ParseFile accepted an unsupported extension")
	}
}

func TestMergeFlagsOverrideFile(t *testing.T) {
	file := []Assignment{{Name: "a", Literal: "1"}, {Name: "b", Literal: "2"}}
	flags := []Assignment{{Name: "b", Literal: `"override"`}, {Name: "c", Literal: "3"}}
	got := Merge(file, flags)
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
		if a.Name == "b" && a.Literal != `"override"` {
			t.Fatalf("flag did not override file: %s", a.Literal)
		}
	}
	if strings.Join(names, ",") != "a,b,c" {
		t.Fatalf("merge order = %v", names)
	}
}

// The injected assignments must land immediately after the parameters-tagged
// cell so a user cell that reads EPOCHS sees the injected value. No tag means
// a usage error, never a silently ignored -p.
func TestInjectAfterParametersCell(t *testing.T) {
	n := nb.NewNotebook()
	n.Cells = []nb.Cell{
		nb.NewMarkdownCell("# notes"),
		nb.NewCodeCell("EPOCHS = 1"),
		nb.NewCodeCell("print(EPOCHS)"),
	}
	n.Cells[1].Metadata["tags"] = []any{"parameters"}

	as := []Assignment{{Name: "EPOCHS", Literal: `"10"`}, {Name: "LR", Literal: `"0.01"`}}
	if err := Inject(n, as); err != nil {
		t.Fatal(err)
	}
	if len(n.Cells) != 4 {
		t.Fatalf("cells = %d, want 4", len(n.Cells))
	}
	src := string(n.Cells[2].Source)
	if !strings.Contains(src, `EPOCHS = "10"`) || !strings.Contains(src, `LR = "0.01"`) {
		t.Fatalf("injected source = %q", src)
	}
	if strings.Index(src, `EPOCHS`) > strings.Index(src, `LR`) {
		t.Fatalf("injected assignments not in name order: %q", src)
	}
	if n.Cells[3].CellType != "code" || !strings.Contains(string(n.Cells[3].Source), "print(EPOCHS)") {
		t.Fatalf("user cell moved: %+v", n.Cells[3])
	}

	plain := nb.NewNotebook()
	plain.Cells = []nb.Cell{nb.NewCodeCell("print(1)")}
	if err := Inject(plain, as); !errors.Is(err, ErrNoParametersCell) {
		t.Fatalf("Inject without tagged cell = %v, want ErrNoParametersCell", err)
	}
}
