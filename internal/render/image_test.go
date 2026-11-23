package render

import (
	"bytes"
	"encoding/base64"
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
