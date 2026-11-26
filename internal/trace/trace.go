// Package trace records a redacted wire trace for honesty tooling.
//
// It is deliberately a leaf package: it imports only the standard library and
// takes plain values, so protocol packages and the CLI can both use it without
// creating an import cycle. A trace is an audit aid, never a credential sink:
// request/response headers are not recorded at all, query VALUES are dropped,
// and bodies are reduced to a shape plus a SHA-256 digest. The optional
// --unsafe-bodies mode includes a JSON body with token-shaped values replaced,
// and is labelled "UNSAFE" in every event that carries one.
package trace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config selects the trace destination. An empty Path disables recording.
type Config struct {
	Path         string
	UnsafeBodies bool
}

// Env names. GOLAB_TRACE is the opt-in used by "any command"; the CLI's
// `trace -- <cmd>` wrapper passes the same values through context instead of
// mutating the process environment.
const (
	EnvPath       = "GOLAB_TRACE"
	EnvUnsafeBody = "GOLAB_TRACE_UNSAFE_BODIES"

	maxTraceBody = 1 << 20
)

// ConfigFromEnv reads the GOLAB_TRACE opt-in. It is called by the composition
// root, so a plain `GOLAB_TRACE=wire.ndjson golab sessions` works on any
// command without a dedicated flag.
func ConfigFromEnv() Config {
	c := Config{Path: strings.TrimSpace(os.Getenv(EnvPath))}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvUnsafeBody))) {
	case "1", "true", "yes", "on":
		c.UnsafeBodies = true
	}
	return c
}

type contextKey struct{}

// WithConfig attaches an explicit trace configuration to ctx.
func WithConfig(ctx context.Context, cfg Config) context.Context {
	return context.WithValue(ctx, contextKey{}, cfg)
}

// FromContext returns the explicit configuration, or a disabled one.
func FromContext(ctx context.Context) Config {
	if ctx == nil {
		return Config{}
	}
	if cfg, ok := ctx.Value(contextKey{}).(Config); ok {
		return cfg
	}
	return Config{}
}

// Event is one append-only NDJSON line. Field order is part of the fixture
// contract; omission keeps ordinary traces small.
type Event struct {
	TS          string         `json:"ts"`
	Kind        string         `json:"kind"` // http | ws | note
	Warning     string         `json:"warning,omitempty"`
	Method      string         `json:"method,omitempty"`
	Path        string         `json:"path,omitempty"`
	QueryKeys   []string       `json:"query_keys,omitempty"`
	Status      int            `json:"status,omitempty"`
	ContentType string         `json:"content_type,omitempty"`
	BodyShape   string         `json:"body_shape,omitempty"`
	BodySHA256  string         `json:"body_sha256,omitempty"`
	UnsafeBody  string         `json:"unsafe_body,omitempty"`
	Frames      map[string]int `json:"frames,omitempty"`
	Note        string         `json:"note,omitempty"`
}

// Recorder appends redacted events to a single file. Each write opens the file
// in append mode and re-asserts 0600, so an existing loose file is tightened
// rather than trusted and no in-process handle survives a command.
type Recorder struct {
	cfg Config

	mu     sync.Mutex
	frames map[string]int
}

// New validates cfg and returns a disabled recorder when Path is empty.
func New(cfg Config) (*Recorder, error) {
	if cfg.Path == "" {
		return nil, nil
	}
	if strings.TrimSpace(cfg.Path) != cfg.Path {
		return nil, fmt.Errorf("trace: path contains leading/trailing whitespace: %q", cfg.Path)
	}
	// Fail at startup, not silently mid-command, when the destination cannot
	// be created. The probe also asserts the restrictive mode up front.
	f, err := os.OpenFile(cfg.Path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("trace: open %s: %w", cfg.Path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("trace: chmod %s: %w", cfg.Path, err)
	}
	_ = f.Close()
	return &Recorder{cfg: cfg, frames: map[string]int{}}, nil
}

// Enabled reports whether this recorder will write anything.
func (r *Recorder) Enabled() bool { return r != nil && r.cfg.Path != "" }

// Record appends one event. It is safe for concurrent use across the command's
// HTTP transport and WebSocket reader.
func (r *Recorder) Record(e Event) error {
	if !r.Enabled() {
		return nil
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.OpenFile(r.cfg.Path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("trace: open %s: %w", r.cfg.Path, err)
	}
	defer func() { _ = f.Close() }()
	// O_CREATE's mode is masked by umask, and a pre-existing file keeps its old
	// mode: assert 0600 explicitly on every append.
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("trace: chmod %s: %w", r.cfg.Path, err)
	}
	if _, err := f.Write(b); err != nil {
		return fmt.Errorf("trace: append %s: %w", r.cfg.Path, err)
	}
	return nil
}

// Wrap returns a RoundTripper that records one event per request/response
// pair. It never touches the request's Authorization, Cookie or proxy-token
// headers and never consumes req.Body.
func (r *Recorder) Wrap(next http.RoundTripper) http.RoundTripper {
	if !r.Enabled() {
		return next
	}
	if next == nil {
		next = http.DefaultTransport
	}
	return &roundTripper{next: next, rec: r}
}

type roundTripper struct {
	next http.RoundTripper
	rec  *Recorder
}

func (t *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	e := Event{Kind: "http", Method: req.Method}
	if req.URL != nil {
		e.Path = req.URL.Path
		keys := make([]string, 0, len(req.URL.Query()))
		for k := range req.URL.Query() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		e.QueryKeys = keys
	}
	if req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			t.recordBody(body, &e)
			_ = body.Close()
		}
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		e.Status = 0
		e.Note = "transport error" // never echo an error that may carry a URL token
		_ = t.rec.Record(e)
		return resp, err
	}
	e.Status = resp.StatusCode
	e.ContentType = resp.Header.Get("Content-Type")
	if resp.Body != nil {
		b, full, readErr := readBounded(resp.Body)
		if readErr == nil {
			if full {
				// Restore the unread tail so the caller still sees the whole
				// response; only the capped prefix is inspected for shape.
				resp.Body = struct {
					io.Reader
					io.Closer
				}{io.MultiReader(bytes.NewReader(b), resp.Body), resp.Body}
			} else {
				resp.Body = io.NopCloser(bytes.NewReader(b))
			}
			t.recordBody(bytes.NewReader(b[:min(len(b), maxTraceBody)]), &e)
		}
	}
	_ = t.rec.Record(e)
	return resp, nil
}

func readBounded(r io.Reader) ([]byte, bool, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxTraceBody+1))
	if err != nil {
		return nil, false, err
	}
	return b, len(b) > maxTraceBody, nil
}

func (t *roundTripper) recordBody(r io.Reader, e *Event) {
	b, err := io.ReadAll(io.LimitReader(r, maxTraceBody))
	if err != nil || len(b) == 0 {
		return
	}
	sum := sha256.Sum256(b)
	e.BodySHA256 = fmt.Sprintf("%x", sum[:])
	e.BodyShape = shapeOf(b)
	if t.rec.cfg.UnsafeBodies {
		if redacted, ok := redactBody(b); ok {
			e.Warning = "UNSAFE: body recording enabled; verify before sharing"
			e.UnsafeBody = redacted
		}
	}
}

// shapeOf renders a schema-only description: key names and container sizes are
// useful for fixture diffs, values never are.
func shapeOf(b []byte) string {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Sprintf("bytes:%d", len(b))
	}
	var out strings.Builder
	writeShape(&out, v, 0)
	s := out.String()
	const maxShape = 4096
	if len(s) > maxShape {
		return s[:maxShape] + "…"
	}
	return s
}

func writeShape(out *strings.Builder, v any, depth int) {
	if depth > 8 {
		out.WriteString("deep")
		return
	}
	switch x := v.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString("bool")
	case float64, json.Number:
		out.WriteString("number")
	case string:
		out.WriteString("string")
	case []any:
		fmt.Fprintf(out, "array[%d]{", len(x))
		if len(x) > 0 {
			writeShape(out, x[0], depth+1)
		}
		out.WriteByte('}')
	case map[string]any:
		out.WriteString("object{")
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			out.WriteString(k)
			out.WriteByte(':')
			writeShape(out, x[k], depth+1)
		}
		out.WriteByte('}')
	default:
		out.WriteString("unknown")
	}
}

var sensitiveKey = []string{
	"token", "access_token", "refresh_token", "id_token", "client_secret",
	"authorization", "cookie", "password", "passwd", "credential",
	"code_verifier", "secret", "proxy-token", "proxy_token",
}

func isSensitiveKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range sensitiveKey {
		// Deliberately broad: in unsafe mode a token under an unforeseen
		// variant key ("proxyTokens", "x-goog-colab-token", ...) must still
		// be replaced rather than written out.
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// redactBody replaces token-shaped values before any body is written. Non-JSON
// bodies are never included: a raw proxy token is not valid JSON and would
// otherwise slip through the unsafe opt-in.
func redactBody(b []byte) (string, bool) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", false
	}
	redactValue(v, 0)
	out, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	s := string(out)
	const maxUnsafe = 4096
	if len(s) > maxUnsafe {
		return s[:maxUnsafe] + "…", true
	}
	return s, true
}

func redactValue(v any, depth int) {
	if depth > 16 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if isSensitiveKey(k) {
				x[k] = "[REDACTED]"
				continue
			}
			redactValue(child, depth+1)
		}
	case []any:
		for _, child := range x {
			redactValue(child, depth+1)
		}
	}
}

// CountFrame counts one WebSocket frame by channel/direction/type. Only the
// label and a number are retained; frame payloads are never read.
func (r *Recorder) CountFrame(channel, direction, typ string) {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames[channel+"/"+direction+"/"+typ]++
}

// FlushWS emits the accumulated frame counts as one summary event and clears
// the counters. It is safe to call when no frames were seen.
func (r *Recorder) FlushWS() error {
	if !r.Enabled() {
		return nil
	}
	r.mu.Lock()
	frames := r.frames
	r.frames = map[string]int{}
	r.mu.Unlock()
	if len(frames) == 0 {
		return nil
	}
	return r.Record(Event{Kind: "ws", Frames: frames})
}
