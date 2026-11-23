package render

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
)

// Output is a protocol-agnostic execution output. Field names mirror Jupyter's
// output messages (stream / display_data / execute_result / error) so callers
// can map their runtime type into it without this package importing
// internal/runtime. Mime payloads are plain strings or []byte; image payloads
// may be base64-encoded strings as upstream sends them.
type Output struct {
	Type      string         // "stream", "display_data", "execute_result", "error"
	Name      string         // stream name: "stdout" or "stderr"
	Text      string         // stream payload
	Data      map[string]any // mime bundle for display_data / execute_result
	Metadata  map[string]any
	Ename     string
	Evalue    string
	Traceback []string
}

// Config controls the consolidated renderer.
type Config struct {
	// Raw skips Sanitize (the --raw opt-out for advanced users).
	Raw bool
	// Markdown enables glamour rendering of text/markdown and text/html.
	Markdown bool
	// Width and Theme configure glamour when Markdown is true.
	Width int
	Theme string
	// Image is forwarded to ImageWithConfig.
	Image ImageConfig
	// IsTTY overrides TTY detection for markdown rendering (nil => platform).
	IsTTY func() bool
}

// Render writes one output to stdout or stderr. It is the Go counterpart of
// display_output in upstream src/colab_cli/commands/execution.py, with the
// R-15 sanitiser applied to every untrusted string.
func Render(stdout, stderr io.Writer, out Output, cfg Config) error {
	san := func(s string) string {
		if cfg.Raw {
			return s
		}
		return Sanitize(s)
	}

	switch out.Type {
	case "stream":
		w := stdout
		if out.Name == "stderr" {
			w = stderr
		}
		_, err := io.WriteString(w, san(out.Text))
		return err

	case "display_data", "execute_result", "data":
		data := out.Data
		if text := renderTextMime(data, cfg); text != "" {
			if _, err := io.WriteString(stdout, san(text)); err != nil {
				return err
			}
		}
		for _, img := range []struct{ mime, format string }{
			{"image/png", "image/png"},
			{"image/jpeg", "image/jpeg"},
		} {
			v, ok := data[img.mime]
			if !ok {
				continue
			}
			b, err := mimeBytes(v)
			if err != nil {
				return err
			}
			if err := ImageWithConfig(stdout, b, img.format, cfg.Image); err != nil {
				return err
			}
		}
		return nil

	case "error":
		if len(out.Traceback) > 0 {
			var b strings.Builder
			for _, line := range out.Traceback {
				b.WriteString(line)
			}
			b.WriteByte('\n')
			_, err := io.WriteString(stderr, san(b.String()))
			return err
		}
		_, err := io.WriteString(stderr, san(out.Ename+": "+out.Evalue+"\n"))
		return err

	default:
		// Ignore metadata, clear_output and other silent messages, like upstream.
		return nil
	}
}

// RenderAll renders a batch of outputs in order.
func RenderAll(stdout, stderr io.Writer, outs []Output, cfg Config) error {
	for _, out := range outs {
		if err := Render(stdout, stderr, out, cfg); err != nil {
			return err
		}
	}
	return nil
}

// renderTextMime applies upstream's text mime priority:
// text/markdown > text/html (via HTMLToMarkdown) > text/plain.
func renderTextMime(data map[string]any, cfg Config) string {
	var raw string
	switch {
	case mimeString(data["text/markdown"]) != "":
		raw = mimeString(data["text/markdown"])
	case mimeString(data["text/html"]) != "":
		converted, err := HTMLToMarkdown(mimeString(data["text/html"]))
		if err != nil {
			return ""
		}
		raw = converted
	default:
		return mimeString(data["text/plain"])
	}

	if !cfg.Markdown || cfg.Raw {
		return raw
	}
	color := UseColor()
	if cfg.IsTTY != nil {
		color = cfg.IsTTY() && os.Getenv("NO_COLOR") == ""
	}
	rendered, err := RenderMarkdownWithConfig(raw, MarkdownConfig{
		Width: cfg.Width,
		Theme: cfg.Theme,
		Color: color,
	})
	if err != nil {
		return raw
	}
	return rendered
}

func mimeString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return ""
	}
}

// mimeBytes decodes an image payload. Upstream sends base64 strings; raw
// []byte and non-base64 strings are accepted as a convenience.
func mimeBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case []byte:
		return x, nil
	case string:
		if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(x)); err == nil {
			return b, nil
		}
		return []byte(x), nil
	default:
		return nil, fmt.Errorf("render: unsupported image payload %T", v)
	}
}
