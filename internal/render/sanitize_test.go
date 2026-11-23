package render

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// esc builds an ESC-prefixed sequence literal for readability in the table below.
func esc(s string) string { return "\x1b" + s }

// TestSanitizeTable ports the intent of upstream tests/test_utils.py (there is
// no direct Python sanitiser test — R-15 requires one): untrusted remote text
// must lose every terminal-control sequence except SGR colour.
func TestSanitizeTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"empty", "", ""},
		{"whitespace", "a\tb\nc\rd", "a\tb\nc\rd"},
		{"c0-controls-stripped", "a\x00b\x07c\x08d\x0be\x0cf", "abcdef"},
		{"del-stripped", "a\x7fb", "ab"},
		{"sgr-bold", "a" + esc("[1m") + "b" + esc("[0m"), "a" + esc("[1m") + "b" + esc("[0m")},
		{"sgr-colour", "a" + esc("[31m") + "red" + esc("[0m"), "a" + esc("[31m") + "red" + esc("[0m")},
		{"sgr-256", "a" + esc("[38;5;200m") + "x", "a" + esc("[38;5;200m") + "x"},
		{"sgr-rgb", "a" + esc("[38;2;255;0;128m") + "x", "a" + esc("[38;2;255;0;128m") + "x"},
		{"sgr-empty", esc("[m") + "x", esc("[m") + "x"},
		{"osc-title-bel", "a" + esc("]0;my title\x07") + "b", "ab"},
		{"osc52-clipboard-bel", "secret" + esc("]52;c;aGVsbG8=\x07") + "end", "secretend"},
		{"osc8-link-st", "before" + esc("]8;;https://example.com\x1b\\") + "link" + esc("]8;;\x1b\\") + "after", "beforelinkafter"},
		{"dcs-stripped", "a" + esc("P+q;payload\x1b\\") + "b", "ab"},
		{"apc-stripped", "a" + esc("_kitty;payload\x1b\\") + "b", "ab"},
		{"pm-stripped", "a" + esc("^payload\x1b\\") + "b", "ab"},
		{"csi-query-dsr", "a" + esc("[6n") + "b", "ab"},
		{"csi-query-decrpm", "a" + esc("[?1;2$y") + "b", "ab"},
		{"csi-query-decrqm", "a" + esc("[?25$p") + "b", "ab"},
		{"csi-clear-screen", "a" + esc("[2J") + "b", "ab"},
		{"csi-cursor-home", "a" + esc("[H") + "b", "ab"},
		{"csi-cursor-position", "a" + esc("[1;1H") + "b", "ab"},
		{"charset-escape", "a" + esc("(B") + "b", "ab"},
		{"charset-dec-special", "a" + esc("(0") + "b", "ab"},
		{"two-byte-escape", "a" + esc("c") + "b", "ab"},
		{"st-alone", "a" + esc("\\") + "b", "ab"},
		{"truncated-esc", "a" + esc(""), "a"},
		{"truncated-csi", "a" + esc("[31"), "a"},
		{"truncated-osc", "a" + esc("]52;abc"), "a"},
		{"truncated-dcs", "a" + esc("Ppayload"), "a"},
		{"sgr-then-osc-query", esc("[32m") + "ok" + esc("]52;c;eA==\x07") + esc("[0m"), esc("[32m") + "ok" + esc("[0m")},
		{"utf8-preserved", "héllo → 🌍", "héllo → 🌍"},
		{"c1-utf8-csi-stripped", "a\u009b2Jb", "ab"},
		{"c1-utf8-sgr", "a\u009b31mb", "a" + esc("[31m") + "b"},
		{"c1-utf8-osc-stripped", "a\u009d0;title\x07b", "ab"},
		{"newline-inside-truncated-csi", "a" + esc("[31\n") + "b", "a\nb"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in)
			if got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeHugeCSI verifies the bounded CSI buffer: a malicious, never-ending
// SGR parameter list must not be echoed back or grow memory without limit.
func TestSanitizeHugeCSI(t *testing.T) {
	huge := strings.Repeat("1;", 100000)
	got := Sanitize("a" + esc("[") + huge + "mb")
	if len(got) > 32 {
		t.Fatalf("huge CSI produced %d bytes of output: %q", len(got), got[:min(len(got), 80)])
	}
	if strings.Contains(got, huge[:100]) {
		t.Fatal("huge CSI parameters were echoed to output")
	}
	if !strings.HasSuffix(got, "b") {
		t.Fatalf("trailing text lost: %q", got)
	}
}

// TestSanitizeWriterStreaming ports the intent of test_print_kitty_silent_when_stdout_not_tty
// at the byte level: escape state must survive arbitrary chunk boundaries.
func TestSanitizeWriterStreaming(t *testing.T) {
	input := "pre" + esc("]52;c;c2VjcmV0\x07") + "mid" + esc("[38;2;1;2;3m") + "colour" + esc("[0m") + esc("[6n") + "post"
	want := Sanitize(input)

	chunkSizes := []int{1, 2, 3, 5, 7, len(input)}
	for _, size := range chunkSizes {
		t.Run(fmt.Sprintf("chunk=%d", size), func(t *testing.T) {
			var buf bytes.Buffer
			w := SanitizeWriter(&buf)
			for i := 0; i < len(input); i += size {
				end := min(i+size, len(input))
				n, err := w.Write([]byte(input[i:end]))
				if err != nil || n != end-i {
					t.Fatalf("write(%d:%d) = (%d, %v)", i, end, n, err)
				}
			}
			if buf.String() != want {
				t.Errorf("streamed = %q, want %q", buf.String(), want)
			}
		})
	}
}

// TestSanitizeWriterUTF8AcrossChunks verifies that a multi-byte rune split over
// two writes is preserved (and a C1 control split over two writes is stripped).
func TestSanitizeWriterUTF8AcrossChunks(t *testing.T) {
	input := "a🌍b\u009b2Jc"
	want := Sanitize(input)
	var buf bytes.Buffer
	w := SanitizeWriter(&buf)
	for _, chunk := range [][]byte{[]byte("a\xf0\x9f"), []byte("\x8c\x8db\xc2"), []byte("\x9b2Jc")} {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if buf.String() != want {
		t.Errorf("streamed = %q, want %q", buf.String(), want)
	}
}

// TestSanitizeWriterErrorPropagation checks that a failing underlying writer
// surfaces its error and that a subsequent write is still possible.
func TestSanitizeWriterErrorPropagation(t *testing.T) {
	w := SanitizeWriter(failWriter{})
	if _, err := io.WriteString(w, "hello"); err == nil {
		t.Fatal("expected error from underlying writer")
	}
}

type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) { return 0, errTest }

var errTest = fmt.Errorf("test write failure")

// TestSanitizeIdempotentAndClean asserts the fuzz invariants on hand-picked
// inputs (the Fuzz target below uses the same checker).
func TestSanitizeIdempotentAndClean(t *testing.T) {
	seeds := []string{
		"plain", "\x1b", "\x1b[", "\x1b[31m", "\x1b]52;c;AA==\x07", "\x1bP\x1b\\",
		"\x1b[6n", "\x1b[?1;2$y", "a\x00b\x7fc", "héllo 🌍", "\u009b", "a\x1b[2Jb",
		"\x1b[38;2;255;0;0mred\x1b[0m",
	}
	for _, s := range seeds {
		got := Sanitize(s)
		if err := verifySanitized(got); err != nil {
			t.Errorf("Sanitize(%q) = %q failed invariant: %v", s, got, err)
		}
		if again := Sanitize(got); again != got {
			t.Errorf("not idempotent for %q: %q -> %q", s, got, again)
		}
	}
}

// verifySanitized rejects any control byte that Sanitize must have removed.
// ESC is only legal as part of a complete SGR sequence (ESC [ ... m).
func verifySanitized(s string) error {
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b == '\t' || b == '\n' || b == '\r':
			continue
		case b == 0x1b:
			if i+1 >= len(s) || s[i+1] != '[' {
				return fmt.Errorf("non-SGR ESC at byte %d", i)
			}
			i += 2
			for i < len(s) {
				c := s[i]
				if c >= 0x40 && c <= 0x7e {
					if c != 'm' {
						return fmt.Errorf("non-SGR CSI final %q at byte %d", c, i)
					}
					break
				}
				i++
			}
			if i >= len(s) {
				return fmt.Errorf("unterminated SGR at byte %d", i)
			}
		case b < 0x20:
			return fmt.Errorf("raw control byte %#x at byte %d", b, i)
		case b == 0x7f:
			return fmt.Errorf("DEL at byte %d", i)
		}
	}
	return nil
}

// FuzzSanitize asserts that no arbitrary byte string can make Sanitize emit a
// dangerous escape sequence, and that sanitising is idempotent.
func FuzzSanitize(f *testing.F) {
	for _, seed := range []string{
		"plain text", "\x1b[31mred\x1b[0m", "\x1b]52;c;AA==\x07", "\x1b[6n",
		"\x1bPfoo\x1b\\", "\x1b", "\x1b[", "a\x00\x7fb", "héllo 🌍",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := Sanitize(in)
		if err := verifySanitized(got); err != nil {
			t.Fatalf("Sanitize(%q) = %q: %v", in, got, err)
		}
		if again := Sanitize(got); again != got {
			t.Fatalf("not idempotent for %q: %q -> %q", in, got, again)
		}
	})
}
