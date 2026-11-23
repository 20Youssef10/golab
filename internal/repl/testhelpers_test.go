package repl

// Shared test doubles. Upstream ports tests/test_repl.py by replacing the
// runtime via MagicMock; here we substitute a small Kernel implementation.

import (
	"context"
	"errors"
)

type fakeKernel struct {
	calls []string
	fn    func(ctx context.Context, code string, opts ExecOptions) (Result, error)
}

func (k *fakeKernel) Execute(ctx context.Context, code string, opts ExecOptions) (Result, error) {
	k.calls = append(k.calls, code)
	if k.fn != nil {
		return k.fn(ctx, code, opts)
	}
	return Result{}, nil
}

var errBoom = errors.New("boom")
