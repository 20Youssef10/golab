package repl

// Port of upstream tests/test_repl.py::test_repl_display_output (ANSI text is
// printed via Text.from_ansi; the Go port sanitises untrusted remote text first).

import "testing"

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
