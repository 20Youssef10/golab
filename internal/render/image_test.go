package render

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImageKittyOnTTY ports test_print_kitty_emits_escape_sequence_on_tty from
// upstream tests/test_utils.py.
func TestImageKittyOnTTY(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "1")
	data := []byte("fake-png-bytes")
	var out bytes.Buffer
	if err := ImageWithConfig(&out, data, "image/png", ImageConfig{TargetPath: filepath.Join(t.TempDir(), "plot.png")}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "_Ga=T,f=100;") {
		t.Errorf("missing kitty PNG introducer: %q", got)
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	if !strings.Contains(got, b64) {
		t.Errorf("missing base64 payload: %q", got)
	}
}

// TestImageSilentWhenNotTTY ports test_print_kitty_silent_when_stdout_not_tty:
// the escape sequence is useless and corrupts redirected output, but the file
// fallback message must still be printed.
func TestImageSilentWhenNotTTY(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	data := []byte("fake-png-bytes")
	var out bytes.Buffer
	if err := ImageWithConfig(&out, data, "image/png", ImageConfig{TargetPath: filepath.Join(t.TempDir(), "plot.png")}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "_Ga=T,f=100;") || strings.Contains(got, "\x1b_") {
		t.Errorf("kitty sequence must be suppressed off TTY: %q", got)
	}
	if !strings.Contains(got, "[Image saved to:") {
		t.Errorf("file fallback message missing: %q", got)
	}
}

// TestImageToFile ports test_handle_image: the decoded bytes are written to the
// requested --output-image path and the path is reported.
func TestImageToFile(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	data := []byte("test")
	target := filepath.Join(t.TempDir(), "plot.png")
	var out bytes.Buffer
	if err := ImageWithConfig(&out, data, "image/png", ImageConfig{TargetPath: target}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("image file not written: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("file contents = %q, want %q", got, data)
	}
	if !strings.Contains(out.String(), target) {
		t.Errorf("saved path not reported: %q", out.String())
	}
}

// TestImageJPEGFormat verifies the JPEG kitty format code.
func TestImageJPEGFormat(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "1")
	var out bytes.Buffer
	if err := ImageWithConfig(&out, []byte("jpeg"), "image/jpeg", ImageConfig{TargetPath: filepath.Join(t.TempDir(), "p.jpeg")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "_Ga=T,f=99;") {
		t.Errorf("missing kitty JPEG introducer: %q", out.String())
	}
}

// TestImageFallbackTempFile verifies the temp-file fallback used when no
// --output-image path is supplied.
func TestImageFallbackTempFile(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	var out bytes.Buffer
	if err := ImageWithConfig(&out, []byte("tmp"), "image/png", ImageConfig{TempDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), ".png") {
		t.Errorf("temp fallback message missing extension: %q", out.String())
	}
}

// TestImageDefault exercises the public Image entry point and its temp-file
// fallback using platform.TempDir (redirected with TMPDIR).
func TestImageDefault(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	t.Setenv("TMPDIR", t.TempDir())
	var out bytes.Buffer
	if err := Image(&out, []byte("default"), "png"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[Image saved to:") {
		t.Errorf("missing fallback message: %q", out.String())
	}
}

func TestImageUnsupportedFormat(t *testing.T) {
	var out bytes.Buffer
	if err := ImageWithConfig(&out, []byte("x"), "image/gif", ImageConfig{TargetPath: filepath.Join(t.TempDir(), "p.gif")}); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}

// Regression: the temp image created when no --output-image is given is an
// internal render artifact. The inline escape carries the displayed image, so
// the file must be removed once its path has been reported; a user-requested
// --output-image save must never be removed.
func TestImageTempArtifactCleanup(t *testing.T) {
	t.Run("temp file removed after inline display", func(t *testing.T) {
		tmpDir := t.TempDir()
		var out, errOut bytes.Buffer
		cfg := ImageConfig{
			TempDir: tmpDir,
			IsTTY:   func() bool { return true },
			Env:     imageEnv(nil),
			Stderr:  &errOut,
		}
		if err := ImageWithConfig(&out, []byte("png-data"), "image/png", cfg); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "_Ga=T,f=100;") {
			t.Fatalf("inline image broken by cleanup: %q", out.String())
		}
		const prefix = "[Image saved to: "
		i := strings.Index(out.String(), prefix)
		if i < 0 {
			t.Fatalf("image path not reported: %q", out.String())
		}
		rest := out.String()[i+len(prefix):]
		j := strings.LastIndexByte(rest, ']')
		if j < 0 {
			t.Fatalf("malformed path report: %q", out.String())
		}
		reported := rest[:j]
		if filepath.Dir(reported) != tmpDir {
			t.Fatalf("reported path %q outside temp dir %q", reported, tmpDir)
		}
		if _, err := os.Stat(reported); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temp image left behind at %q (stat err %v)", reported, err)
		}
		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("temp dir not empty after render: %v", entries)
		}
	})
	t.Run("output-image save is kept", func(t *testing.T) {
		var out bytes.Buffer
		target := filepath.Join(t.TempDir(), "plot.png")
		if err := ImageWithConfig(&out, []byte("png-data"), "image/png", ImageConfig{TargetPath: target}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("--output-image save was removed: %v", err)
		}
	})
}
