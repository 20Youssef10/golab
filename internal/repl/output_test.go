package repl

// Port of upstream tests/test_repl.py::test_repl_display_output and
// ::test_repl_display_output_image_suppress_text. The Go port routes streams by
// name, errors to stderr, and exposes a pluggable Renderer for display_data.

import (
	"bytes"
	"encoding/base64"
	"errors"
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

// Regression: the fallback temp image is an internal artifact. The save notice
// is still printed, but no file may be left behind. A user-requested
// --output-image save is covered by TestWriteOutputImageToFile and stays.
func TestWriteOutputImageTempFallbackCleanedUp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	pf, out, errOut := newTestPiped(t)
	png := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	pf.writeOutput(Output{OutputType: "display_data", Data: map[string]string{"image/png": png}})

	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
	const prefix = "[Image saved to: "
	i := strings.Index(out.String(), prefix)
	if i < 0 {
		t.Fatalf("save notice missing: %q", out.String())
	}
	rest := out.String()[i+len(prefix):]
	j := strings.LastIndexByte(rest, ']')
	if j < 0 {
		t.Fatalf("malformed save notice: %q", out.String())
	}
	reported := rest[:j]
	if filepath.Dir(reported) != dir {
		t.Fatalf("reported path %q outside TMPDIR %q", reported, dir)
	}
	if _, err := os.Stat(reported); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp image left behind at %q (stat err %v)", reported, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp dir not empty after render: %v", entries)
	}
}

func TestWriteOutputSanitizesRemoteText(t *testing.T) {
	pf, out, _ := newTestPiped(t)
	pf.writeOutput(Output{Text: "\x1b]0;title\x07safe\x1b[32mOK\x1b[0m"})
	if got := out.String(); got != "safe\x1b[32mOK\x1b[0m" {
		t.Errorf("stdout = %q, want sanitised text", got)
	}
}

// Cross-check: a malformed image payload must not be echoed into a diagnostic.
// The REPL never handles credentials itself, so this is the only error path
// that can be fed untrusted remote bytes.
func TestSaveImageErrorOmitsPayload(t *testing.T) {
	pf, _, errOut := newTestPiped(t)
	const canary = "PASSWORD_SHOULD_NOT_APPEAR_7f3a"
	pf.writeOutput(Output{OutputType: "display_data", Data: map[string]string{
		"image/png": "!!!not-base64!!!" + canary,
	}})
	if got := errOut.String(); strings.Contains(got, canary) {
		t.Fatalf("image error leaked the payload: %q", got)
	}
}
