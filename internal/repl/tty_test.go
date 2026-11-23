package repl

// Ports of upstream tests/test_repl.py::test_cli_repl_interactive,
// ::test_repl_run_quit_aliases, ::test_repl_run_misc_inputs,
// ::test_repl_run_exceptions, ::test_repl_key_bindings and
// ::test_repl_run_executes_code. TTY behaviour is mocked with the
// COLAB_TEST_TTY / COLAB_TEST_STDIN_TTY hooks from internal/platform; no real
// TTY is ever required.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"golang.org/x/term"
)

func drainEvents(ch <-chan editorEvent) []editorEvent {
	var out []editorEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestTTYMockingNeverBlocks(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	t.Setenv("COLAB_TEST_STDIN_TTY", "0")
	var out, errOut bytes.Buffer
	f, err := NewTTY(strings.NewReader("print(1)\n/quit\n"), &out, &errOut)
	if err != nil {
		t.Fatalf("NewTTY: %v", err)
	}
	k := &fakeKernel{}
	if err := f.Run(context.Background(), k); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(k.calls) != 1 || k.calls[0] != "print(1)" {
		t.Fatalf("calls = %q, want [print(1)]", k.calls)
	}
	if !strings.Contains(out.String(), "Python 3 (Google Colab Runtime)") {
		t.Errorf("stdout = %q, want banner", out.String())
	}
	if !strings.Contains(out.String(), "Goodbye!") {
		t.Errorf("stdout = %q, want goodbye", out.String())
	}
}

func TestTTYQuitAliases(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	t.Setenv("COLAB_TEST_STDIN_TTY", "0")
	for _, cmd := range []string{"/quit", "quit()", "exit()"} {
		var out, errOut bytes.Buffer
		f, err := NewTTY(strings.NewReader(cmd+"\nprint(1)\n"), &out, &errOut)
		if err != nil {
			t.Fatalf("NewTTY: %v", err)
		}
		k := &fakeKernel{}
		if err := f.Run(context.Background(), k); err != nil {
			t.Fatalf("Run(%q): %v", cmd, err)
		}
		if len(k.calls) != 0 {
			t.Errorf("quit alias %q executed %q, want none", cmd, k.calls)
		}
	}
}

func TestTTYMiscInputs(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	t.Setenv("COLAB_TEST_STDIN_TTY", "0")
	var out, errOut bytes.Buffer
	f, _ := NewTTY(strings.NewReader("\n   \nexit()\n"), &out, &errOut)
	k := &fakeKernel{}
	if err := f.Run(context.Background(), k); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(k.calls) != 0 {
		t.Errorf("calls = %q, want none for blank input", k.calls)
	}
}

func TestEditorEnterSubmits(t *testing.T) {
	// Upstream ::test_repl_key_bindings: "enter" validates/handles the buffer.
	e := &lineEditor{r: bytes.NewReader([]byte("print(1)\r")), raw: true}
	evs := drainEvents(e.Events(context.Background()))
	if len(evs) < 1 || evs[0].kind != eventSubmit || evs[0].code != "print(1)" {
		t.Fatalf("events = %+v, want submit print(1)", evs)
	}
}

func TestEditorCtrlJAndAltEnterInsertNewline(t *testing.T) {
	// Upstream ::test_repl_key_bindings: "c-j" and "escape enter" insert "\n".
	for _, in := range []string{
		"for i in range(2):\n    print(i)\r", // Ctrl-J
		"x = 1\x1b\rY = 2\r",                 // Alt-Enter
	} {
		e := &lineEditor{r: bytes.NewReader([]byte(in)), raw: true}
		evs := drainEvents(e.Events(context.Background()))
		if len(evs) < 1 || evs[0].kind != eventSubmit {
			t.Fatalf("input %q: events = %+v, want submit", in, evs)
		}
		if !strings.Contains(evs[0].code, "\n") {
			t.Errorf("input %q: code = %q, want embedded newline", in, evs[0].code)
		}
	}
}

func TestEditorCtrlCInterrupts(t *testing.T) {
	// Upstream ::test_repl_run_exceptions: KeyboardInterrupt prints newline.
	e := &lineEditor{r: bytes.NewReader([]byte("abc\x03")), raw: true}
	evs := drainEvents(e.Events(context.Background()))
	var sawInterrupt bool
	for _, ev := range evs {
		if ev.kind == eventInterrupt {
			sawInterrupt = true
		}
		if ev.kind == eventSubmit {
			t.Fatalf("unexpected submit %q after Ctrl-C", ev.code)
		}
	}
	if !sawInterrupt {
		t.Fatalf("events = %+v, want interrupt", evs)
	}
}

func TestEditorHistoryUpDown(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	e := &lineEditor{
		r:       bytes.NewReader([]byte("\x1b[A\r")),
		raw:     true,
		history: []string{"previous()"},
	}
	evs := drainEvents(e.Events(context.Background()))
	if len(evs) < 1 || evs[0].kind != eventSubmit || evs[0].code != "previous()" {
		t.Fatalf("events = %+v, want recalled previous()", evs)
	}
}

func TestTTYRestoresConsoleOnExit(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	t.Setenv("COLAB_TEST_STDIN_TTY", "0")
	f, err := NewTTY(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("NewTTY: %v", err)
	}
	tf := f.(*ttyFrontend)
	restored := 0
	tf.onRestore = func() { restored++ }
	tf.rawFD, tf.orig = 99999, &term.State{} // closed fd: Restore fails harmlessly
	if err := f.Run(context.Background(), &fakeKernel{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if restored != 1 {
		t.Fatalf("restore called %d times, want 1", restored)
	}
}

func TestTTYCtrlCCancelsExecution(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	t.Setenv("COLAB_TEST_STDIN_TTY", "0")
	var out, errOut bytes.Buffer
	f, err := NewTTY(bytes.NewReader([]byte("x\r\x03")), &out, &errOut)
	if err != nil {
		t.Fatalf("NewTTY: %v", err)
	}
	tf := f.(*ttyFrontend)
	tf.rawEnabled = true // synthetic raw input; no real TTY involved
	k := &fakeKernel{fn: func(ctx context.Context, code string, opts ExecOptions) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}}
	if err := f.Run(context.Background(), k); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(errOut.String(), "Execution failed:") {
		t.Errorf("stderr = %q, want execution failure diagnostic", errOut.String())
	}
}

func TestEditorUTF8Input(t *testing.T) {
	e := &lineEditor{r: bytes.NewReader([]byte("你好\r")), raw: true}
	evs := drainEvents(e.Events(context.Background()))
	if len(evs) < 1 || evs[0].kind != eventSubmit || evs[0].code != "你好" {
		t.Fatalf("events = %+v, want submit 你好", evs)
	}
}

func TestVisibleRunesIgnoresANSI(t *testing.T) {
	if got := visibleRunes("\x1b[38;5;196mred\x1b[0m"); got != 3 {
		t.Fatalf("visibleRunes = %d, want 3", got)
	}
}

func TestHighlightPython(t *testing.T) {
	got := HighlightPython("def f():\n    return True\n")
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("HighlightPython = %q, want ANSI colour codes", got)
	}
	if !strings.Contains(got, "return") {
		t.Fatalf("HighlightPython = %q, want token text", got)
	}
}
