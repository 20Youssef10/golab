package render

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func imageEnv(values map[string]string) func(string) string {
	return func(k string) string { return values[k] }
}

// TestImageProtocolNegotiation is the table test for both inline protocols:
// iTerm2 gets OSC 1337, Kitty gets the graphics escape, and a non-TTY gets no
// escape at all (the piped-output contract).
func TestImageProtocolNegotiation(t *testing.T) {
	data := []byte("fake-png-bytes")
	for _, tc := range []struct {
		name     string
		tty      bool
		env      map[string]string
		want     string
		wantNone bool
	}{
		{"kitty-window-id", true, map[string]string{"KITTY_WINDOW_ID": "1"}, "_Ga=T,f=100;", false},
		{"iterm2", true, map[string]string{"TERM_PROGRAM": "iTerm.app"}, "\x1b]1337;File=", false},
		{"wezterm-iterm-fallback", true, map[string]string{"TERM_PROGRAM": "WezTerm"}, "\x1b]1337;File=", false},
		{"unknown-tty-defaults-kitty", true, nil, "_Ga=T,f=100;", false},
		{"non-tty", false, nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			cfg := ImageConfig{
				TargetPath: filepath.Join(t.TempDir(), "plot.png"),
				IsTTY:      func() bool { return tc.tty },
				Env:        imageEnv(tc.env),
				Stderr:     &errOut,
			}
			if err := ImageWithConfig(&out, data, "image/png", cfg); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			if tc.wantNone {
				if strings.Contains(got, "\x1b_") || strings.Contains(got, "\x1b]") {
					t.Fatalf("escape leaked to a pipe: %q", got)
				}
			} else if !strings.Contains(got, tc.want) {
				t.Fatalf("missing %q in %q", tc.want, got)
			}
			if tc.name == "iterm2" {
				payload := base64.StdEncoding.EncodeToString(data)
				if !strings.Contains(got, payload) {
					t.Fatalf("missing iTerm2 base64 payload: %q", got)
				}
				if strings.Contains(got, "_Ga=T") {
					t.Fatalf("iTerm2 profile emitted Kitty escape: %q", got)
				}
			}
			if !strings.Contains(got, "[Image saved to:") {
				t.Errorf("file fallback message missing: %q", got)
			}
		})
	}
}

// TestImageByteCap suppresses any escape above the byte cap while keeping the
// file fallback and emitting one notice on stderr.
func TestImageByteCap(t *testing.T) {
	var out, errOut bytes.Buffer
	data := bytes.Repeat([]byte("x"), 9<<20)
	cfg := ImageConfig{
		TargetPath: filepath.Join(t.TempDir(), "big.png"),
		IsTTY:      func() bool { return true },
		Env:        imageEnv(nil),
		Stderr:     &errOut,
	}
	if err := ImageWithConfig(&out, data, "image/png", cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b_") || strings.Contains(out.String(), "\x1b]") {
		t.Fatal("oversize image was emitted inline")
	}
	if !strings.Contains(errOut.String(), "cap") {
		t.Fatalf("no stderr notice: %q", errOut.String())
	}
	info, err := os.Stat(cfg.TargetPath)
	if err != nil || info.Size() != int64(len(data)) {
		t.Fatalf("file fallback wrong: info=%v err=%v", info, err)
	}
}

// TestImagePixelCap uses a real PNG header so the dimension cap is exercised
// even when the compressed byte size is small.
func TestImagePixelCap(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1001, 1001))
	for x := 0; x < 1001; x += 100 {
		for y := 0; y < 1001; y += 100 {
			img.Set(x, y, color.Black)
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	cfg := ImageConfig{
		TargetPath: filepath.Join(t.TempDir(), "large.png"),
		IsTTY:      func() bool { return true },
		Env:        imageEnv(map[string]string{"TERM_PROGRAM": "iTerm.app"}),
		Stderr:     &errOut,
		MaxWidth:   100, MaxHeight: 100,
	}
	if err := ImageWithConfig(&out, raw.Bytes(), "image/png", cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b]1337") || strings.Contains(out.String(), "\x1b_") {
		t.Fatal("over-dimension image was emitted inline")
	}
	if !strings.Contains(errOut.String(), "cap") {
		t.Fatalf("no stderr notice: %q", errOut.String())
	}
}

// TestImageNonTTYOutputIsByteExact pins the existing --output-image behaviour:
// no escapes, no stderr chatter, exactly one fallback line on stdout.
func TestImageNonTTYOutputIsByteExact(t *testing.T) {
	var out, errOut bytes.Buffer
	target := filepath.Join(t.TempDir(), "plot.png")
	cfg := ImageConfig{
		TargetPath: target,
		IsTTY:      func() bool { return false },
		Stderr:     &errOut,
		MaxBytes:   1, // would be "oversize", but off TTY there is no escape to suppress
	}
	if err := ImageWithConfig(&out, []byte("data"), "image/png", cfg); err != nil {
		t.Fatal(err)
	}
	want := "\n[Image saved to: " + target + "]\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

// TestImageSanitisesPath prevents a remote-controlled filename from injecting
// a new OSC sequence into our own fallback message.
func TestImageSanitisesPath(t *testing.T) {
	// Windows forbids control characters in filenames, so the malicious path
	// cannot be created there; the sanitisation itself is platform-independent
	// and is covered by the Sanitize tests.
	if goruntime.GOOS == "windows" {
		t.Skip("Windows rejects control characters in filenames")
	}
	var out, errOut bytes.Buffer
	evil := filepath.Join(t.TempDir(), "plot\x1b]52;c;c2VjcmV0\x07.png")
	cfg := ImageConfig{
		TargetPath: evil, IsTTY: func() bool { return false },
		Stderr: &errOut,
	}
	if err := ImageWithConfig(&out, []byte("data"), "image/png", cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b]") {
		t.Fatalf("path injection survived: %q", out.String())
	}
}
