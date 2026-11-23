package render

import (
	"strings"
	"testing"
)

// TestHTMLToMarkdown ports test_render_display_data_markdown from upstream
// tests/test_utils.py: an HTML mime payload is converted to Markdown before it
// reaches the renderer.
func TestHTMLToMarkdown(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bold", "<b>hi</b>", "**hi**"},
		{"strong", "<strong>important</strong>", "**important**"},
		{"italic", "<i>hi</i>", "_hi_"},
		{"paragraph", "<p>one</p><p>two</p>", "one\n\ntwo"},
		{"link", `<a href="https://example.com">link</a>`, "[link](https://example.com)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HTMLToMarkdown(tc.in)
			if err != nil {
				t.Fatalf("HTMLToMarkdown(%q): %v", tc.in, err)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("HTMLToMarkdown(%q) = %q, want it to contain %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestUseColor verifies ADR-012 §7: colour is only enabled on a TTY with
// NO_COLOR unset. TTY detection is delegated to internal/platform, which tests
// mock through COLAB_TEST_TTY.
func TestUseColor(t *testing.T) {
	cases := []struct {
		name    string
		tty     string
		noColor string
		want    bool
	}{
		{"tty-no-nocolor", "1", "", true},
		{"tty-with-nocolor", "1", "1", false},
		{"tty-with-empty-nocolor", "1", "", true},
		{"no-tty", "0", "", false},
		{"no-tty-no-nocolor", "0", "1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COLAB_TEST_TTY", tc.tty)
			t.Setenv("NO_COLOR", tc.noColor)
			if got := UseColor(); got != tc.want {
				t.Errorf("UseColor() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRenderMarkdownGating checks that RenderMarkdown emits ANSI only when
// colour is enabled; otherwise it returns plain text (AGENTS.md #4, ADR-012).
func TestRenderMarkdownGating(t *testing.T) {
	in := "# Heading\n\n**bold** and `code`\n"

	t.Run("tty-colour", func(t *testing.T) {
		t.Setenv("COLAB_TEST_TTY", "1")
		t.Setenv("NO_COLOR", "")
		got, err := RenderMarkdown(in)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("expected ANSI on a colour terminal, got %q", got)
		}
		if err := verifySanitized(got); err != nil {
			t.Errorf("glamour output must be SGR-only: %v", err)
		}
	})

	t.Run("no-color", func(t *testing.T) {
		t.Setenv("COLAB_TEST_TTY", "1")
		t.Setenv("NO_COLOR", "1")
		got, err := RenderMarkdown(in)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "\x1b[") {
			t.Errorf("NO_COLOR must suppress ANSI, got %q", got)
		}
		if !strings.Contains(got, "Heading") || !strings.Contains(got, "bold") {
			t.Errorf("plain text lost content: %q", got)
		}
	})

	t.Run("non-tty", func(t *testing.T) {
		t.Setenv("COLAB_TEST_TTY", "0")
		t.Setenv("NO_COLOR", "")
		got, err := RenderMarkdown(in)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "\x1b[") {
			t.Errorf("non-TTY must suppress ANSI, got %q", got)
		}
		if !strings.Contains(got, "Heading") {
			t.Errorf("non-TTY lost content: %q", got)
		}
	})
}

// TestIsTTY confirms TTY detection is delegated to internal/platform.
func TestIsTTY(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "1")
	if !IsTTY() {
		t.Error("IsTTY() = false with COLAB_TEST_TTY=1")
	}
	t.Setenv("COLAB_TEST_TTY", "0")
	if IsTTY() {
		t.Error("IsTTY() = true with COLAB_TEST_TTY=0")
	}
}

// TestRenderMarkdownConfig exercises the width/theme configuration surface.
func TestRenderMarkdownConfig(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "1")
	t.Setenv("NO_COLOR", "")
	cfg := MarkdownConfig{Width: 40, Theme: "notty"}
	got, err := RenderMarkdownWithConfig("**hello**", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("content lost: %q", got)
	}
}
