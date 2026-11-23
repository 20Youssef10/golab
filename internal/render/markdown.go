package render

import (
	"fmt"
	"os"
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/charmbracelet/glamour"
	"github.com/muesli/termenv"

	"github.com/20Youssef10/golab/internal/platform"
)

// MarkdownConfig controls glamour markdown rendering.
type MarkdownConfig struct {
	// Width is the word-wrap width. Values <= 0 select the default (80).
	Width int
	// Theme is a built-in glamour style ("dark", "light", "notty", ...).
	// Empty selects "dark".
	Theme string
	// Color enables ANSI styling. When false the output is plain text.
	Color bool
}

// IsTTY reports whether stdout is a terminal (ADR-012 §7). It delegates to
// internal/platform so tests can mock the answer with COLAB_TEST_TTY.
func IsTTY() bool { return platform.IsStdoutTTY() }

// UseColor reports whether rich ANSI output is allowed: stdout must be a TTY
// and NO_COLOR must be unset (any non-empty NO_COLOR value disables colour).
func UseColor() bool { return platform.IsStdoutTTY() && os.Getenv("NO_COLOR") == "" }

func defaultMarkdownConfig() MarkdownConfig {
	return MarkdownConfig{Width: 80, Theme: "dark", Color: UseColor()}
}

// RenderMarkdown renders Markdown to ANSI using the default width/theme, but
// only emits colour when UseColor() allows it. The rendered text is sanitised
// so markup that expands into an unsafe sequence cannot affect the terminal.
func RenderMarkdown(s string) (string, error) {
	return RenderMarkdownWithConfig(s, defaultMarkdownConfig())
}

// RenderMarkdownWithConfig is RenderMarkdown with an explicit configuration.
func RenderMarkdownWithConfig(s string, cfg MarkdownConfig) (string, error) {
	width := cfg.Width
	if width <= 0 {
		width = 80
	}
	theme := cfg.Theme
	if theme == "" {
		theme = "dark"
	}
	profile := termenv.Ascii
	if cfg.Color {
		profile = termenv.TrueColor
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(width),
		glamour.WithStylePath(theme),
		glamour.WithColorProfile(profile),
	)
	if err != nil {
		return "", fmt.Errorf("render: create markdown renderer: %w", err)
	}
	out, err := r.Render(s)
	if err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}
	// Glamour renders bold/italic with SGR even on the Ascii profile, so strip
	// the styling ourselves when colour is not allowed (NO_COLOR / non-TTY).
	clean := Sanitize(out)
	if !cfg.Color {
		clean = stripSGR(clean)
	}
	return clean, nil
}

// stripSGR removes SGR CSI sequences while leaving all other text intact.
func stripSGR(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				if c >= 0x40 && c <= 0x7e {
					break
				}
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// HTMLToMarkdown converts an HTML fragment to Markdown. It is the Go
// counterpart of upstream html2text.html2text in render_display_data.
func HTMLToMarkdown(s string) (string, error) {
	converter := md.NewConverter("", true, nil)
	out, err := converter.ConvertString(s)
	if err != nil {
		return "", fmt.Errorf("render: html to markdown: %w", err)
	}
	return out, nil
}
