package repl

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/20Youssef10/golab/internal/platform"
)

// lockedWriter serialises writes from the REPL loop and the input editor
// goroutine. fmt.Fprint/Fprintf issue a single Write per call, so whole lines
// stay intact. This matters for non-thread-safe writers such as bytes.Buffer.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}

func lockWriter(w io.Writer) io.Writer { return &lockedWriter{w: w} }

// baseFrontend holds the I/O and rendering state shared by the piped and TTY
// frontends. It is embedded rather than shared globally so each frontend can
// be used concurrently without mutable package state.
type baseFrontend struct {
	in          io.Reader
	out         io.Writer
	errOut      io.Writer
	renderer    Renderer
	outputImage string
	log         *slog.Logger
}

// writeOutput renders one output. Streams go to stdout (or to stderr when the
// stream name is "stderr"), error outputs and their tracebacks go to stderr,
// and display data goes through the pluggable Renderer to stdout. All remote
// text is sanitised first (port of upstream ::test_repl_display_output).
func (f *baseFrontend) writeOutput(o Output) {
	switch {
	case o.OutputType == "error":
		tb := sanitize(strings.Join(o.Traceback, ""))
		if strings.TrimSpace(tb) != "" {
			fmt.Fprint(f.errOut, tb)
			if !strings.HasSuffix(tb, "\n") {
				fmt.Fprintln(f.errOut)
			}
			return
		}
		ename := o.Ename
		if ename == "" {
			ename = "Error"
		}
		fmt.Fprintf(f.errOut, "%s: %s\n", sanitize(ename), sanitize(o.Evalue))
	case o.OutputType == "stream":
		w := f.out
		if strings.EqualFold(o.Name, "stderr") {
			w = f.errOut
		}
		fmt.Fprint(w, sanitize(o.Text))
	case len(o.Data) > 0:
		f.writeDisplayData(o.Data)
	case o.Text != "":
		// The Python REPL's output_hook uses {"text": ...}.
		fmt.Fprint(f.out, sanitize(o.Text))
	}
}

// writeDisplayData mirrors upstream display_output / render_display_data:
// images are handled first (suppressing generic IPython/Figure reprs), then
// the best text MIME type is rendered.
func (f *baseFrontend) writeDisplayData(data map[string]string) {
	imageShown := false
	for _, mime := range []string{"image/png", "image/jpeg"} {
		b64, ok := data[mime]
		if !ok || b64 == "" {
			continue
		}
		if err := f.saveImage(mime, b64); err != nil {
			fmt.Fprintf(f.errOut, "[colab] image error: %v\n", err)
		} else {
			imageShown = true
		}
		break
	}

	kind, value, ok := pickText(data)
	if !ok {
		return
	}
	text := f.renderer(kind, value)
	if text == "" {
		return
	}
	if imageShown && (strings.Contains(text, "<IPython.core.display.Image") ||
		strings.Contains(text, "<Figure size")) {
		return
	}
	fmt.Fprintln(f.out, sanitize(text))
}

// pickText follows upstream priority: markdown > html > plain.
func pickText(data map[string]string) (kind, value string, ok bool) {
	for _, mime := range []string{"text/markdown", "text/html", "text/plain"} {
		if v, present := data[mime]; present {
			return mime, v, true
		}
	}
	return "", "", false
}

// saveImage decodes a base64 image and writes it to the configured path or a
// temporary file, printing the same "[Image saved to: ...]" notice as upstream.
func (f *baseFrontend) saveImage(mime, b64 string) error {
	b64 = strings.TrimPrefix(b64, "data:"+mime+";base64,")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err // never include the payload in the error
	}

	path := f.outputImage
	if path == "" {
		ext := "png"
		if mime == "image/jpeg" {
			ext = "jpeg"
		}
		tmp, err := os.CreateTemp(platform.TempDir(), "colab-*."+ext)
		if err != nil {
			return err
		}
		tempPath := tmp.Name()
		path = tempPath
		// The fallback file is an internal render artifact: the save notice
		// is printed before this deferred removal runs. A user-requested
		// --output-image save (the other branch) is never removed.
		defer func() { _ = os.Remove(tempPath) }()
		if _, err := tmp.Write(raw); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := platform.MakePrivate(path); err != nil {
			if f.log != nil {
				f.log.Debug("image chmod failed", "err", err)
			}
		}
	} else if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}

	fmt.Fprintf(f.out, "\n[Image saved to: %s]\n", sanitize(path))
	return nil
}

// execAndRender executes one block, streaming outputs through the hook. If the
// Kernel ignores the hook, the aggregated Result is rendered once instead.
func (f *baseFrontend) execAndRender(ctx context.Context, k Kernel, code string) (Result, error) {
	hooked := 0
	opts := ExecOptions{OutputHook: func(o Output) {
		hooked++
		f.writeOutput(o)
	}}
	res, err := k.Execute(ctx, code, opts)
	if hooked == 0 {
		for _, o := range res.Outputs {
			f.writeOutput(o)
		}
	}
	return res, err
}
