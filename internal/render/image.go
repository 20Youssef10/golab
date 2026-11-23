package render

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/20Youssef10/golab/internal/platform"
)

// ImageConfig controls Kitty image emission and the file fallback used when
// stdout is not a TTY (the --output-image flag is TargetPath).
type ImageConfig struct {
	// TargetPath is the --output-image destination. Empty means a temp file.
	TargetPath string
	// TempDir is the directory for the fallback temp file. Empty means
	// platform.TempDir().
	TempDir string
	// IsTTY overrides TTY detection (for tests). Nil means platform.IsStdoutTTY.
	IsTTY func() bool
}

type imageFormat struct {
	kitty string // Kitty graphics protocol value for f=
	ext   string // filename extension for the fallback file
}

func imageFormatFor(format string) (imageFormat, error) {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), "image/")) {
	case "png":
		return imageFormat{kitty: "100", ext: "png"}, nil
	case "jpeg", "jpg":
		return imageFormat{kitty: "99", ext: "jpeg"}, nil
	default:
		return imageFormat{}, fmt.Errorf("render: unsupported image format %q", format)
	}
}

// Image emits data using the Kitty graphics protocol when stdout is a TTY and
// always writes the image to a file, reporting its path. When stdout is not a
// TTY the escape sequence is suppressed (it would corrupt piped output) but the
// file fallback remains available, matching upstream handle_image/print_kitty.
func Image(w io.Writer, data []byte, format string) error {
	return ImageWithConfig(w, data, format, ImageConfig{})
}

// ImageWithConfig is Image with an explicit configuration.
func ImageWithConfig(w io.Writer, data []byte, format string, cfg ImageConfig) error {
	if w == nil {
		return fmt.Errorf("render: nil image writer")
	}
	fmtInfo, err := imageFormatFor(format)
	if err != nil {
		return err
	}

	isTTY := platform.IsStdoutTTY()
	if cfg.IsTTY != nil {
		isTTY = cfg.IsTTY()
	}
	if isTTY {
		var buf bytes.Buffer
		buf.WriteString("\n\x1b_Ga=T,f=")
		buf.WriteString(fmtInfo.kitty)
		buf.WriteByte(';')
		enc := base64.NewEncoder(base64.StdEncoding, &buf)
		if _, err := enc.Write(data); err != nil {
			return fmt.Errorf("render: encode kitty image: %w", err)
		}
		if err := enc.Close(); err != nil {
			return fmt.Errorf("render: encode kitty image: %w", err)
		}
		buf.WriteString("\x1b\\\n")
		if err := writeAll(w, buf.Bytes()); err != nil {
			return fmt.Errorf("render: write kitty image: %w", err)
		}
	}

	var saved string
	if cfg.TargetPath != "" {
		saved = cfg.TargetPath
		if err := os.WriteFile(saved, data, 0o600); err != nil {
			return fmt.Errorf("render: save image to %q: %w", saved, err)
		}
	} else {
		dir := cfg.TempDir
		if dir == "" {
			dir = platform.TempDir()
		}
		f, err := os.CreateTemp(dir, "colab-image-*."+fmtInfo.ext)
		if err != nil {
			return fmt.Errorf("render: create temp image: %w", err)
		}
		saved = f.Name()
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("render: write temp image: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("render: close temp image: %w", err)
		}
	}

	if _, err := fmt.Fprintf(w, "\n[Image saved to: %s]\n", saved); err != nil {
		return fmt.Errorf("render: report image path: %w", err)
	}
	return nil
}
