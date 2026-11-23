package repl

import (
	"context"
	"log/slog"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

// Output is a single Jupyter output message, reduced to what the REPL needs to
// render. internal/adapters translate the runtime's richer output type into
// this value before executing. Images in Data are base64-encoded (an optional
// "data:<mime>;base64," prefix is accepted).
type Output struct {
	// OutputType is "stream", "execute_result", "display_data", "error", or
	// empty for the REPL's plain {"text": ...} form.
	OutputType string
	// Name is the stream name; "stderr" routes to the error writer.
	Name string
	// Text is the stream/plain text payload.
	Text string
	// Data is a MIME bundle for display_data / execute_result.
	Data      map[string]string
	Ename     string
	Evalue    string
	Traceback []string
}

// ExecOptions are the per-execution options the REPL passes to a Kernel.
// OutputHook, when non-nil, is called once for each output as it is produced.
type ExecOptions struct {
	OutputHook func(Output)
	Timeout    time.Duration // 0 means no limit
}

// Result is the aggregate of an execution. It is only rendered when the Kernel
// did not call ExecOptions.OutputHook, so output is never printed twice.
type Result struct {
	Outputs []Output
}

// Kernel is the minimal execution surface required by a Frontend. It is
// intentionally declared here rather than importing internal/runtime, keeping
// the protocol layer free of REPL concerns.
type Kernel interface {
	Execute(ctx context.Context, code string, opts ExecOptions) (Result, error)
}

// Renderer converts a display payload into text. kind is the MIME type key
// (for example "text/markdown"); data is the raw value. It is pluggable so
// internal/render can be injected later without changing this package.
type Renderer func(kind, data string) string

// Frontend owns the input loop for a REPL session.
type Frontend interface {
	Run(ctx context.Context, k Kernel) error
}

type options struct {
	renderer    Renderer
	outputImage string
	banner      bool
	log         *slog.Logger
}

func defaultOptions() options { return options{banner: true} }

// Option customises a Frontend constructed by NewPiped or NewTTY.
type Option func(*options)

// WithRenderer injects a display-data renderer.
func WithRenderer(r Renderer) Option { return func(o *options) { o.renderer = r } }

// WithOutputImage sets the path where displayed images are saved. When empty,
// images are written to a temporary file (matching upstream).
func WithOutputImage(path string) Option { return func(o *options) { o.outputImage = path } }

// WithBanner enables or disables the interactive startup banner.
func WithBanner(on bool) Option { return func(o *options) { o.banner = on } }

// WithLogger sets the logger used for non-fatal diagnostics; nil uses logx.Logger().
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.log = l } }

func (o options) rendererOrDefault() Renderer {
	if o.renderer != nil {
		return o.renderer
	}
	// Default is plain text: return the payload unchanged.
	return func(_, data string) string { return data }
}

func (o options) logger() *slog.Logger {
	if o.log != nil {
		return o.log
	}
	return logx.Logger()
}
