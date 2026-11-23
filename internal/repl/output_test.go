package repl

// Port of upstream tests/test_repl.py::test_repl_display_output and
// ::test_repl_display_output_image_suppress_text. The Go port routes streams by
// name, errors to stderr, and exposes a pluggable Renderer for display_data.

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestPiped(t *testing.T, opts ...Option) (*pipedFrontend, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	f := NewPiped(strings.NewReader(""), &out, &errOut, opts...)
	pf, ok := f.(*pipedFrontend)
	if !ok {
		t.Fatalf("NewPiped returned %T, want *pipedFrontend", f)
	}
	return pf, &out, &errOut
}

func TestWriteOutputRoutesStreams(t *testing.T) {
	pf, out, errOut := newTestPiped(t)
	pf.writeOutput(Output{OutputType: "stream", Name: "stdout", Text: "to-out"})
	pf.writeOutput(Output{OutputType: "stream", Name: "stderr", Text: "to-err"})
	pf.writeOutput(Output{OutputType: "stream", Text: "default-out"})

	if got := out.String(); got != "to-outdefault-out" {
		t.Errorf("stdout = %q, want %q", got, "to-outdefault-out")
	}
	if got := errOut.String(); got != "to-err" {
		t.Errorf("stderr = %q, want %q", got, "to-err")
	}
}

func TestWriteOutputErrorsToStderr(t *testing.T) {
	pf, out, errOut := newTestPiped(t)
	pf.writeOutput(Output{OutputType: "error", Ename: "ValueError", Evalue: "bad"})
	want := "ValueError: bad\n"
	if got := errOut.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}

	errOut.Reset()
	pf.writeOutput(Output{OutputType: "error", Traceback: []string{"line1\n", "line2\n"}})
	if got := errOut.String(); got != "line1\nline2\n" {
		t.Errorf("traceback = %q, want %q", got, "line1\nline2\n")
	}
}

func TestWriteOutputDisplayDataRenderer(t *testing.T) {
	var gotKind, gotData string
	r := func(kind, data string) string {
		gotKind, gotData = kind, data
		return "RENDERED:" + data
	}
	pf, out, _ := newTestPiped(t, WithRenderer(r))
	pf.writeOutput(Output{OutputType: "display_data", Data: map[string]string{
		"text/markdown": "# Hi",
		"text/plain":    "Hi",
	}})
	if gotKind != "text/markdown" || gotData != "# Hi" {
		t.Errorf("renderer got (%q, %q), want (%q, %q)", gotKind, gotData, "text/markdown", "# Hi")
	}
	if !strings.Contains(out.String(), "RENDERED:# Hi") {
		t.Errorf("stdout = %q, want rendered text", out.String())
	}
}

func TestWriteOutputImageToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plot.png")
	png := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	pf, out, errOut := newTestPiped(t, WithOutputImage(path))

	pf.writeOutput(Output{OutputType: "display_data", Data: map[string]string{
		"image/png":  png,
		"text/plain": "<Figure size 640x480>",
	}})

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	if string(got) != "PNGDATA" {
		t.Errorf("image = %q, want PNGDATA", got)
	}
	if !strings.Contains(out.String(), "[Image saved to: "+path+"]") {
		t.Errorf("stdout = %q, want save notice", out.String())
	}
	if strings.Contains(out.String(), "<Figure size") {
		t.Errorf("stdout = %q, want generic image repr suppressed", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
}

func TestWriteOutputImageTempFallback(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	pf, out, _ := newTestPiped(t)
	jpeg := base64.StdEncoding.EncodeToString([]byte("JPEGDATA"))
	pf.writeOutput(Output{OutputType: "display_data", Data: map[string]string{"image/jpeg": jpeg}})
	if !strings.Contains(out.String(), "[Image saved to: ") {
		t.Errorf("stdout = %q, want temp save notice", out.String())
	}
}

func TestWriteOutputSanitizesRemoteText(t *testing.T) {
	pf, out, _ := newTestPiped(t)
	pf.writeOutput(Output{Text: "\x1b]0;title\x07safe\x1b[32mOK\x1b[0m"})
	if got := out.String(); got != "safe\x1b[32mOK\x1b[0m" {
		t.Errorf("stdout = %q, want sanitised text", got)
	}
}
