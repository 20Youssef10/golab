package render

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// TestRenderOutputStream ports display_output from upstream
// src/colab_cli/commands/execution.py: stream payloads go to the matching
// stream and remote escapes are sanitised unless --raw is set.
func TestRenderOutputStream(t *testing.T) {
	tests := []struct {
		name    string
		out     Output
		raw     bool
		wantOut string
		wantErr string
	}{
		{"stdout", Output{Type: "stream", Name: "stdout", Text: "hello"}, false, "hello", ""},
		{"stderr", Output{Type: "stream", Name: "stderr", Text: "warn"}, false, "", "warn"},
		{"no-name-is-stdout", Output{Type: "stream", Text: "hi"}, false, "hi", ""},
		{"sanitised", Output{Type: "stream", Name: "stdout", Text: "a" + esc("]52;c;eA==\x07") + "b"}, false, "ab", ""},
		{"raw-opt-out", Output{Type: "stream", Name: "stdout", Text: "a" + esc("]52;c;eA==\x07") + "b"}, true, "a" + esc("]52;c;eA==\x07") + "b", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cfg := Config{Raw: tc.raw}
			if err := Render(&stdout, &stderr, tc.out, cfg); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != tc.wantOut {
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.wantOut)
			}
			if stderr.String() != tc.wantErr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantErr)
			}
		})
	}
}

// TestRenderOutputDisplayData ports render_display_data's mime priority:
// text/markdown > text/html (via html-to-markdown) > text/plain.
func TestRenderOutputDisplayData(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	t.Setenv("NO_COLOR", "")
	tests := []struct {
		name string
		out  Output
		want string
	}{
		{"plain", Output{Type: "display_data", Data: map[string]any{"text/plain": "plain"}}, "plain"},
		{"html", Output{Type: "display_data", Data: map[string]any{"text/html": "<b>hi</b>"}}, "**hi**"},
		{"markdown-wins", Output{Type: "display_data", Data: map[string]any{"text/markdown": "**md**", "text/html": "<b>hi</b>"}}, "**md**"},
		{"html-wins-over-plain", Output{Type: "display_data", Data: map[string]any{"text/html": "<b>hi</b>", "text/plain": "plain"}}, "**hi**"},
		{"no-text", Output{Type: "display_data", Data: map[string]any{"image/png": "..."}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := Render(&stdout, &stderr, tc.out, Config{}); err != nil {
				t.Fatal(err)
			}
			if stderr.Len() != 0 {
				t.Errorf("unexpected stderr: %q", stderr.String())
			}
			got := strings.ReplaceAll(stdout.String(), "\n", "")
			if !strings.Contains(got, tc.want) {
				t.Errorf("stdout = %q, want to contain %q", stdout.String(), tc.want)
			}
		})
	}
}

// TestRenderOutputError ports the error branch of display_output.
func TestRenderOutputError(t *testing.T) {
	tests := []struct {
		name string
		out  Output
		want string
	}{
		{"traceback", Output{Type: "error", Traceback: []string{"line1\n", "line2\n"}}, "line1\nline2\n"},
		{"ename-evalue", Output{Type: "error", Ename: "ValueError", Evalue: "bad"}, "ValueError: bad"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := Render(&stdout, &stderr, tc.out, Config{}); err != nil {
				t.Fatal(err)
			}
			if stdout.Len() != 0 {
				t.Errorf("unexpected stdout: %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want to contain %q", stderr.String(), tc.want)
			}
		})
	}
}

// TestRenderOutputMarkdownAndImage exercises the rich paths: glamour rendering
// of text/markdown and base64-decoded image payloads.
func TestRenderOutputMarkdownAndImage(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "1")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TMPDIR", t.TempDir())
	var stdout, stderr bytes.Buffer
	cfg := Config{
		Markdown: true,
		Width:    40,
		Theme:    "dark",
	}
	out := Output{Type: "display_data", Data: map[string]any{
		"text/markdown": "**bold**",
		"image/png":     base64.StdEncoding.EncodeToString([]byte("pngbytes")),
	}}
	if err := Render(&stdout, &stderr, out, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "\x1b[") {
		t.Errorf("expected ANSI markdown on a colour TTY: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "_Ga=T,f=100;") {
		t.Errorf("expected kitty PNG: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr: %q", stderr.String())
	}
}

// TestMimeBytesRaw covers the []byte image payload path.
func TestMimeBytesRaw(t *testing.T) {
	b, err := mimeBytes([]byte("raw"))
	if err != nil || string(b) != "raw" {
		t.Fatalf("mimeBytes([]byte) = %q, %v", b, err)
	}
	if _, err := mimeBytes(42); err == nil {
		t.Fatal("expected error for unsupported payload")
	}
}

// TestRenderAll checks the batch entry point preserves order.
func TestRenderAll(t *testing.T) {
	var stdout, stderr bytes.Buffer
	outs := []Output{
		{Type: "stream", Name: "stdout", Text: "one\n"},
		{Type: "stream", Name: "stderr", Text: "two\n"},
		{Type: "stream", Name: "stdout", Text: "three\n"},
	}
	if err := RenderAll(&stdout, &stderr, outs, Config{}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "one\nthree\n" || stderr.String() != "two\n" {
		t.Errorf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
