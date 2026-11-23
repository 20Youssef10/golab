package repl

// Port of upstream tests/test_repl.py::test_repl_display_output (ANSI text is
// printed via Text.from_ansi; the Go port sanitises untrusted remote text first).

import (
	"bytes"
	"io"
	"testing"
	"unicode/utf8"
)

func TestSanitizeKeepsSGR(t *testing.T) {
	in := "\x1b[31mred\x1b[0m normal"
	if got := sanitize(in); got != in {
		t.Fatalf("sanitize(%q) = %q, want it unchanged", in, got)
	}
}

func TestSanitizeStripsOSC(t *testing.T) {
	cases := map[string]string{
		"\x1b]0;my title\x07hello": "hello",
		"\x1b]11;?\x1b\\hello":     "hello",
		"\x9d0;title\x07hello":     "hello", // 8-bit OSC
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeStripsDCSAPC(t *testing.T) {
	cases := map[string]string{
		"\x1bP+q544e\x1b\\ok": "ok", // DCS
		"\x1b_ignore\x1b\\ok": "ok", // APC
		"\x90dcs\x9cok":       "ok", // 8-bit DCS + ST
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeStripsQueryCSI(t *testing.T) {
	cases := map[string]string{
		"\x1b[?u":      "",
		"\x1b[?25lX":   "X",
		"\x1b[>4;2mY":  "Y",
		"\x1b[<1;2;3Z": "",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizePlainUnchanged(t *testing.T) {
	in := "a\nb\t✓ 你好"
	if got := sanitize(in); got != in {
		t.Fatalf("sanitize(%q) = %q, want unchanged", in, got)
	}
}

// AUDIT-02 P2-4: only SGR (CSI ... m) may survive. Cursor movement, erase,
// reporting/query and private-mode CSI must all be dropped, even when the final
// byte happens to be 'm'. Table is expressed as full input/output pairs.
func TestSanitizeDropsNonSGRCSI(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"sgr-colour", "\x1b[31mred\x1b[0m", "\x1b[31mred\x1b[0m"},
		{"sgr-256-rgb", "\x1b[38;2;1;2;3mX\x1b[39m", "\x1b[38;2;1;2;3mX\x1b[39m"},
		{"erase-screen", "A\x1b[2JB", "AB"},
		{"erase-line", "A\x1b[2KB", "AB"},
		{"cursor-up", "A\x1b[1AB", "AB"},
		{"cursor-position", "A\x1b[1;1HB", "AB"},
		{"device-status-report", "A\x1b[6nB", "AB"},
		{"device-attrs", "A\x1b[0cB", "AB"},
		{"private-mode-set", "A\x1b[?25hB", "AB"},
		{"private-mode-sgr-final", "A\x1b[>4;2mB", "AB"},
		{"mouse-tracking", "A\x1b[?1000hB", "AB"},
		{"sgr-with-intermediate", "A\x1b[31$ mB", "AB"},
		{"8bit-non-sgr", "A\x9b2JB", "AB"},
		{"8bit-sgr", "A\x9b31mB", "A\x1b[31mB"},
		{"charset-selector", "A\x1b(BB", "AB"},
		{"two-byte-escape", "A\x1bcB", "AB"},
		{"stray-c0", "A\x07\x08\x00B", "AB"},
		{"c1-controls", "A\x9d0;t\x07B", "AB"},
		{"truncated-csi", "A\x1b[31", "A"},
		{"malformed-csi-control", "A\x1b[31\nB", "A\nB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitize(tc.in); got != tc.want {
				t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A streaming writer must keep scanner state across Write calls: a CSI split
// anywhere (including inside a multi-byte UTF-8 rune) is parsed exactly once.
func TestSanitizeWriterStreamingState(t *testing.T) {
	for _, split := range []int{1, 2, 3, 4, 5, 6} {
		in := "✓\x1b[31mred\x1b[0m\x1b[2J\x1b[38;2;1;2;3m✓\x1b[6n"
		want := sanitize(in)
		var b bytes.Buffer
		w := SanitizeWriter(&b)
		for i := 0; i < len(in); {
			j := i + split
			if j > len(in) {
				j = len(in)
			}
			n, err := w.Write([]byte(in[i:j]))
			if err != nil || n != j-i {
				t.Fatalf("split %d: Write(%d) = %d, %v", split, j-i, n, err)
			}
			i = j
		}
		if got := b.String(); got != want {
			t.Errorf("split %d: wrote %q, want %q", split, got, want)
		}
	}
	if _, err := SanitizeWriter(io.Discard).Write(nil); err != nil {
		t.Fatalf("empty write: %v", err)
	}
}

// FuzzSanitize checks the security invariants that hold for every input:
// idempotence, no C0/C1 controls, no OSC/DCS/APC strings and no invalid UTF-8.
func FuzzSanitize(f *testing.F) {
	for _, seed := range []string{
		"plain", "\x1b[31mred\x1b[0m", "\x1b]0;title\x07x", "\x1bPq\x1b\\x",
		"\x1b[?25l\x1b[6n\x1b[2J", "\x9b31m✓", "\x1b[", "\x00\x7f\x9d",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := sanitize(in)
		if again := sanitize(out); again != out {
			t.Fatalf("not idempotent: sanitize(%q)=%q -> %q", in, out, again)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 in output %q", out)
		}
		// Decode runes so that UTF-8 continuation bytes are not mistaken for
		// C1 controls. ESC is allowed only as part of a retained SGR sequence;
		// the loop below checks that it is always followed by '['.
		for _, r := range out {
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != 0x1b {
				t.Fatalf("C0 control %#x in output %q", r, out)
			}
			if r >= 0x7f && r <= 0x9f {
				t.Fatalf("C1 control %#x in output %q", r, out)
			}
		}
		// No ESC followed by anything other than '[' may survive.
		for i := 0; i < len(out); i++ {
			if out[i] == 0x1b && (i+1 >= len(out) || out[i+1] != '[') {
				t.Fatalf("non-CSI escape in output %q", out)
			}
		}
	})
}
