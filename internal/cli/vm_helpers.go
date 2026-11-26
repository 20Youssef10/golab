package cli

// Tier 2 VM-inspection helpers. These commands attach one kernel, run bounded
// snippets built by internal/vmstat, and stream sanitized/redacted output.
// Kept in new files so the shared root/run/session/state/register files stay
// untouched (only registerExtraUtilityCommands gains one registration line).

import (
	"context"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/render"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
)

// vmRunner is one attached kernel reused across a command's executions (a
// `tail -f` poll loop issues one cheap execution per interval).
type vmRunner struct {
	d      *app.Deps
	s      *state.Session
	kernel runtime.Kernel
	red    *execRedactor
}

func newVMRunner(ctx context.Context, d *app.Deps, name string) (*vmRunner, error) {
	s, kernel, err := attachSession(ctx, d, name)
	if err != nil {
		return nil, err
	}
	setRunning(d, s, running("vm-inspect"))
	setLastExecution(d, s, "vm-inspect", nil)
	return &vmRunner{d: d, s: s, kernel: kernel, red: newExecRedactor()}, nil
}

func (r *vmRunner) Close() {
	if r == nil {
		return
	}
	setRunning(r.d, r.s, nil)
	_ = r.kernel.Close()
}

// Exec runs one snippet and returns its stdout after the shared render/redaction
// path. Kernel error outputs become a non-zero exit; they never reach stdout.
func (r *vmRunner) Exec(ctx context.Context, code string, timeout time.Duration) (string, error) {
	var stdout strings.Builder
	res, err := r.kernel.Execute(ctx, code, runtime.ExecOptions{
		IdleTimeout: timeout,
		OnOutput: func(o runtime.Output) {
			if o.OutputType == "error" {
				return
			}
			safe := r.red.Output(o)
			if safe.Text != "" {
				stdout.WriteString(render.Sanitize(safe.Text))
			}
		},
	})
	if err != nil {
		return "", err
	}
	if code := exitCodeFromOutputs(res.Outputs); code != 0 {
		return "", exitf(code, "[colab] VM inspection failed on the kernel.")
	}
	return stdout.String(), nil
}
