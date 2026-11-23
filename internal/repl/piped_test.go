package repl

// Ports of upstream tests/test_repl.py::test_cli_repl_piped,
// ::test_cli_repl_piped_empty, and ::test_repl_execute (streaming output hook).

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func runPiped(t *testing.T, input string, k Kernel, opts ...Option) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	f := NewPiped(strings.NewReader(input), &out, &errOut, opts...)
	err := f.Run(context.Background(), k)
	return out.String(), errOut.String(), err
}

func TestPipedEmptyEOF(t *testing.T) {
	k := &fakeKernel{}
	_, _, err := runPiped(t, "", k)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(k.calls) != 0 {
		t.Fatalf("calls = %v, want none", k.calls)
	}
}

func TestPipedWhitespaceOnlyEOF(t *testing.T) {
	// Upstream: test_cli_repl_piped_empty (input="   \n  ") exits 0.
	k := &fakeKernel{}
	_, _, err := runPiped(t, "   \n  ", k)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(k.calls) != 0 {
		t.Fatalf("calls = %v, want none", k.calls)
	}
}

func TestPipedSingleLine(t *testing.T) {
	// Upstream: test_cli_repl_piped (assert execute_code called with "print(1)").
	k := &fakeKernel{}
	_, _, err := runPiped(t, "print(1)", k)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(k.calls) != 1 || k.calls[0] != "print(1)" {
		t.Fatalf("calls = %q, want [print(1)]", k.calls)
	}
}

func TestPipedMultiLineBlock(t *testing.T) {
	k := &fakeKernel{}
	in := "for i in range(3):\n    print(i)\n"
	_, _, err := runPiped(t, in, k)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(k.calls) != 1 || k.calls[0] != "for i in range(3):\n    print(i)" {
		t.Fatalf("calls = %q, want one multi-line block", k.calls)
	}
}

func TestPipedMultipleBlocks(t *testing.T) {
	k := &fakeKernel{}
	_, _, err := runPiped(t, "a = 1\n\nb = 2\n", k)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"a = 1", "b = 2"}
	if len(k.calls) != len(want) {
		t.Fatalf("calls = %q, want %q", k.calls, want)
	}
	for i := range want {
		if k.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, k.calls[i], want[i])
		}
	}
}

func TestPipedOutputRouting(t *testing.T) {
	k := &fakeKernel{fn: func(ctx context.Context, code string, opts ExecOptions) (Result, error) {
		opts.OutputHook(Output{OutputType: "stream", Name: "stdout", Text: "OUT"})
		opts.OutputHook(Output{OutputType: "stream", Name: "stderr", Text: "ERR"})
		opts.OutputHook(Output{OutputType: "error", Ename: "E", Evalue: "v"})
		return Result{Outputs: []Output{
			{OutputType: "stream", Text: "OUT"},
			{OutputType: "stream", Name: "stderr", Text: "ERR"},
			{OutputType: "error", Ename: "E", Evalue: "v"},
		}}, nil
	}}
	out, errOut, err := runPiped(t, "x", k)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "OUT" {
		t.Errorf("stdout = %q, want OUT (hook must not double-print)", out)
	}
	if want := "ERRE: v\n"; errOut != want {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
}

func TestPipedResultFallbackWithoutHook(t *testing.T) {
	// A Kernel that ignores OutputHook still gets its Result rendered once.
	k := &fakeKernel{fn: func(ctx context.Context, code string, opts ExecOptions) (Result, error) {
		return Result{Outputs: []Output{{Text: "from-result"}}}, nil
	}}
	out, _, err := runPiped(t, "x", k)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "from-result" {
		t.Errorf("stdout = %q, want from-result", out)
	}
}

func TestPipedExecutionErrorReturned(t *testing.T) {
	k := &fakeKernel{fn: func(ctx context.Context, code string, opts ExecOptions) (Result, error) {
		return Result{}, errBoom
	}}
	_, _, err := runPiped(t, "x", k)
	if !errors.Is(err, errBoom) {
		t.Fatalf("Run error = %v, want wrapped errBoom", err)
	}
}

func TestPipedContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	k := &fakeKernel{}
	f := NewPiped(strings.NewReader("x"), &out, &errOut)
	if err := f.Run(ctx, k); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if len(k.calls) != 0 {
		t.Fatalf("calls = %v, want none", k.calls)
	}
}
