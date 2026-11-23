package repl

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
)

// pipedFrontend implements P0: read blank-line-separated code blocks from
// stdin, execute each on the kernel, render its outputs, and exit cleanly on
// EOF. It never probes a real TTY, so it is safe in CI.
type pipedFrontend struct {
	baseFrontend
}

// NewPiped returns a Frontend that reads from in and writes program output to
// out and diagnostics/errors to errOut. Optional configuration can inject a
// Renderer or an --output-image path.
func NewPiped(in io.Reader, out, errOut io.Writer, opts ...Option) Frontend {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	return &pipedFrontend{baseFrontend: baseFrontend{
		in:          in,
		out:         lockWriter(out),
		errOut:      lockWriter(errOut),
		renderer:    o.rendererOrDefault(),
		outputImage: o.outputImage,
		log:         o.logger(),
	}}
}

// Run executes blocks until EOF or context cancellation. A blank line ends the
// current block; a trailing block at EOF is executed as well. Whitespace-only
// input exits without executing anything (upstream: empty piped repl → exit 0).
func (f *pipedFrontend) Run(ctx context.Context, k Kernel) error {
	br := bufio.NewReader(f.in)
	var block []string

	flush := func() error {
		code := strings.Join(block, "\n")
		block = nil
		if strings.TrimSpace(code) == "" {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := f.execAndRender(ctx, k, code)
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			if strings.TrimSpace(trimmed) == "" {
				if e := flush(); e != nil {
					return e
				}
			} else {
				block = append(block, trimmed)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return flush()
			}
			return err
		}
	}
}
