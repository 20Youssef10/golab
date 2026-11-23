package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/20Youssef10/golab/internal/platform"
)

const (
	ttyPrompt = ">>> "
)

// ttyFrontend implements P1: a raw-mode, multi-line Python REPL with history,
// chroma highlighting and interrupt handling. It is kept entirely behind the
// Frontend interface; nothing outside this package depends on it.
type ttyFrontend struct {
	baseFrontend
	banner      bool
	rawEnabled  bool
	rawFD       int
	orig        *term.State
	onRestore   func() // test hook, invoked after restoring the console mode
	restoreOnce sync.Once
}

// NewTTY builds an interactive frontend. Raw mode is only armed when stdin is
// detected as a TTY (mockable through internal/platform's COLAB_TEST_* hooks)
// and the reader is an *os.File; otherwise the frontend degrades to a line
// reader over the supplied io.Reader. This keeps tests from blocking on a real
// TTY and never fails construction because no terminal is present.
func NewTTY(in io.Reader, out, errOut io.Writer, opts ...Option) (Frontend, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	f := &ttyFrontend{
		baseFrontend: baseFrontend{
			in:          in,
			out:         lockWriter(out),
			errOut:      lockWriter(errOut),
			renderer:    o.rendererOrDefault(),
			outputImage: o.outputImage,
			log:         o.logger(),
		},
		banner:     o.banner,
		rawEnabled: platform.IsStdinTTY(),
		rawFD:      -1,
	}
	return f, nil
}

type execOutcome struct{ err error }

// Run drives the interactive loop. Raw mode is enabled on entry and restored on
// every exit path, including SIGINT/SIGTERM (via signal.NotifyContext) and
// context cancellation.
func (f *ttyFrontend) Run(ctx context.Context, k Kernel) error {
	f.armRawMode()
	defer f.disarmRawMode()

	// SIGINT/SIGTERM cancel the session and unwind the deferred restore. Ctrl-C
	// typed in raw mode arrives as a byte and is handled by the line editor.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	color := f.rawEnabled && platform.IsStdoutTTY()
	if f.banner {
		fmt.Fprint(f.out, "Python 3 (Google Colab Runtime)\nType /quit to exit.\n")
	}

	ed := &lineEditor{
		r:      f.in,
		raw:    f.rawEnabled,
		redraw: f.makeRedraw(color),
	}
	events := ed.Events(sigCtx)

	var (
		running    bool
		execCancel context.CancelFunc
		execDone   = make(chan execOutcome, 1)
		pendingEOF bool
	)
	f.prompt()

	for {
		select {
		case <-sigCtx.Done():
			if running && execCancel != nil {
				execCancel()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		case ev, ok := <-events:
			if !ok {
				if !running {
					fmt.Fprintln(f.out, "[*] Goodbye!")
					return nil
				}
				pendingEOF = true
				continue
			}
			switch ev.kind {
			case eventError:
				fmt.Fprintf(f.errOut, "[colab] REPL Error: %v\n", ev.err)
			case eventEOF:
				if !running {
					fmt.Fprintln(f.out, "[*] Goodbye!")
					return nil
				}
				pendingEOF = true
			case eventInterrupt:
				fmt.Fprintln(f.out)
				if running && execCancel != nil {
					execCancel()
				} else {
					f.prompt()
				}
			case eventSubmit:
				code := strings.TrimSpace(ev.code)
				if code == "" {
					f.prompt()
					continue
				}
				switch strings.ToLower(code) {
				case "/quit", "quit()", "exit()":
					if !running {
						fmt.Fprintln(f.out, "[*] Goodbye!")
						return nil
					}
				}
				if running {
					continue // extra input while executing is ignored
				}
				ec, cancel := context.WithCancel(sigCtx)
				execCancel, running = cancel, true
				go func(code string) {
					_, err := f.execAndRender(ec, k, code)
					execDone <- execOutcome{err: err}
				}(code)
			}
		case res := <-execDone:
			running, execCancel = false, nil
			fmt.Fprintln(f.out) // upstream prints() so the next prompt starts on a new line
			f.prompt()
			if res.err != nil {
				fmt.Fprintf(f.errOut, "Execution failed: %v\n", res.err)
			}
			if pendingEOF {
				fmt.Fprintln(f.out, "[*] Goodbye!")
				return nil
			}
		}
	}
}

func (f *ttyFrontend) armRawMode() {
	if !f.rawEnabled || f.rawFD >= 0 {
		return
	}
	file, ok := f.in.(*os.File)
	if !ok {
		// Synthetic reader (tests): keep raw key parsing but there is no fd to restore.
		return
	}
	fd := int(file.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		if f.log != nil {
			f.log.Debug("raw mode unavailable", "err", err)
		}
		f.rawEnabled = false
		return
	}
	f.rawFD, f.orig = fd, state
}

func (f *ttyFrontend) disarmRawMode() {
	if f.rawFD < 0 || f.orig == nil {
		return
	}
	f.restoreOnce.Do(func() {
		_ = term.Restore(f.rawFD, f.orig)
		if f.onRestore != nil {
			f.onRestore()
		}
	})
}

func (f *ttyFrontend) prompt() {
	fmt.Fprint(f.out, ttyPrompt)
}

// makeRedraw rebuilds the current input line in place, colourising it when the
// terminal supports colour. Enter/Alt-Enter/Ctrl-J and history navigation all
// route through this callback.
func (f *ttyFrontend) makeRedraw(color bool) func(string) {
	return func(line string) {
		shown := line
		if color {
			shown = HighlightPython(line)
		}
		first := shown
		if i := strings.IndexByte(shown, '\n'); i >= 0 {
			first = shown[:i]
		}
		col := len(ttyPrompt) + visibleRunes(first) + 1
		fmt.Fprintf(f.out, "\r\x1b[2K%s%s\r\x1b[%dC", ttyPrompt, shown, col)
	}
}

// editorEvent is one input event produced by a lineEditor.
type editorEventKind int

const (
	eventSubmit editorEventKind = iota
	eventEOF
	eventInterrupt
	eventError
)

type editorEvent struct {
	kind editorEventKind
	code string
	err  error
}

// lineEditor turns bytes from a reader into submit/interrupt/EOF events. In raw
// mode Enter submits, Ctrl-J and Alt-Enter insert a newline (multi-line
// editing), Ctrl-C interrupts, Ctrl-D at an empty line is EOF, and Up/Down walk
// the in-memory history. Non-raw mode reads one line at a time.
type lineEditor struct {
	r       io.Reader
	raw     bool
	history []string
	histIdx int
	redraw  func(string)
}

// Events streams editor events on a buffered channel. The channel is closed
// when input is exhausted or ctx is cancelled.
func (e *lineEditor) Events(ctx context.Context) <-chan editorEvent {
	ch := make(chan editorEvent, 8)
	go func() {
		defer close(ch)
		if !e.raw {
			e.lineEvents(ctx, ch)
			return
		}
		e.rawEvents(ctx, ch)
	}()
	return ch
}

func (e *lineEditor) lineEvents(ctx context.Context, ch chan<- editorEvent) {
	br := bufio.NewReader(e.r)
	for {
		if ctx.Err() != nil {
			return
		}
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if ctx.Err() != nil {
				return
			}
			ch <- editorEvent{kind: eventSubmit, code: strings.TrimRight(line, "\r\n")}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				ch <- editorEvent{kind: eventEOF}
			} else {
				ch <- editorEvent{kind: eventError, err: err}
			}
			return
		}
	}
}

func (e *lineEditor) rawEvents(ctx context.Context, ch chan<- editorEvent) {
	br := bufio.NewReader(e.r)
	var buf []rune
	redraw := func() {
		if e.redraw != nil {
			e.redraw(string(buf))
		}
	}
	clear := func() {
		buf = buf[:0]
		e.histIdx = len(e.history)
	}
	replace := func(s string) {
		buf = []rune(s)
		redraw()
	}

	for {
		if ctx.Err() != nil {
			return
		}
		b, err := br.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				ch <- editorEvent{kind: eventEOF}
			} else {
				ch <- editorEvent{kind: eventError, err: err}
			}
			return
		}

		switch b {
		case 0x03: // Ctrl-C / KeyboardInterrupt
			clear()
			redraw()
			ch <- editorEvent{kind: eventInterrupt}
		case 0x04: // Ctrl-D
			if len(buf) == 0 {
				ch <- editorEvent{kind: eventEOF}
				return
			}
		case '\r': // Enter submits (upstream key binding "enter")
			code := string(buf)
			if code != "" && (len(e.history) == 0 || e.history[len(e.history)-1] != code) {
				e.history = append(e.history, code)
			}
			clear()
			ch <- editorEvent{kind: eventSubmit, code: code}
		case '\n': // Ctrl-J inserts a newline (upstream binding "c-j")
			buf = append(buf, '\n')
			redraw()
		case 0x7f, 0x08: // Backspace
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				redraw()
			}
		case 0x1b: // ESC sequence
			n, err := br.ReadByte()
			if err != nil {
				ch <- editorEvent{kind: eventEOF}
				return
			}
			switch n {
			case '\r', '\n', 'e', 'E': // Alt-Enter inserts a newline
				buf = append(buf, '\n')
				redraw()
			case '[': // CSI: arrow keys
				final := byte(0)
				for {
					c, err := br.ReadByte()
					if err != nil {
						ch <- editorEvent{kind: eventEOF}
						return
					}
					if c >= 0x40 && c <= 0x7e {
						final = c
						break
					}
				}
				switch final {
				case 'A': // Up
					if len(e.history) > 0 {
						if e.histIdx > 0 {
							e.histIdx--
						}
						replace(e.history[e.histIdx])
					}
				case 'B': // Down
					if e.histIdx < len(e.history)-1 {
						e.histIdx++
						replace(e.history[e.histIdx])
					} else {
						e.histIdx = len(e.history)
						replace("")
					}
				}
			case 'O': // application cursor mode: O A / O B
				final, err := br.ReadByte()
				if err != nil {
					ch <- editorEvent{kind: eventEOF}
					return
				}
				switch final {
				case 'A':
					if len(e.history) > 0 {
						if e.histIdx > 0 {
							e.histIdx--
						}
						replace(e.history[e.histIdx])
					}
				case 'B':
					if e.histIdx < len(e.history)-1 {
						e.histIdx++
						replace(e.history[e.histIdx])
					}
				}
			}
		default:
			if b < 0x20 { // ignore other control bytes
				continue
			}
			buf = append(buf, decodeRune(br, b))
			redraw()
		}
	}
}

// decodeRune reads the remaining bytes of a UTF-8 rune whose first byte was
// already consumed from br.
func decodeRune(br *bufio.Reader, first byte) rune {
	if first < utf8.RuneSelf {
		return rune(first)
	}
	peeked, _ := br.Peek(utf8.UTFMax - 1)
	buf := [utf8.UTFMax]byte{first}
	n := 1
	for _, b := range peeked {
		if n >= utf8.UTFMax || b&0xc0 != 0x80 {
			break
		}
		buf[n] = b
		n++
	}
	r, size := utf8.DecodeRune(buf[:n])
	if size <= 1 {
		return utf8.RuneError
	}
	for i := 1; i < size; i++ {
		_, _ = br.ReadByte()
	}
	return r
}
