package contents

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

// DefaultChunkSize is used when Write or WriteStream is called with a
// non-positive chunk size. A smaller value sends more PUTs; a larger one uses
// more memory per chunk.
const DefaultChunkSize = 1 << 20 // 1 MiB

const (
	requestTimeout = 2 * time.Minute
	uploadTimeout  = 10 * time.Minute

	connectTimeout = 10 * time.Second
	tlsTimeout     = 10 * time.Second
	headerTimeout  = 30 * time.Second
	idleTimeout    = 90 * time.Second
)

var (
	// ErrNotFound means the remote file or directory does not exist (HTTP 404).
	ErrNotFound = errors.New("contents: file or directory not found")
	// ErrIsDirectory means a file-only operation was attempted on a directory.
	ErrIsDirectory = errors.New("contents: path is a directory")
)

// Config holds the dependencies of a Contents API client. Token is the data-
// plane runtime-proxy token, never the control-plane OAuth bearer.
type Config struct {
	BaseURL     string
	Token       logx.Secret
	HTTPClient  *http.Client
	ClientAgent string
	Logger      *slog.Logger
}

// Client is the subset of Jupyter's contents API needed by the CLI.
type Client interface {
	// List returns the model at remotePath. A directory model carries its
	// children in Model.Entries.
	List(ctx context.Context, remotePath string) (*Model, error)

	// Read returns the model at remotePath with decoded content: base64 is
	// decoded to bytes, json is re-encoded with two-space indentation, and all
	// other formats are treated as UTF-8 text.
	Read(ctx context.Context, remotePath string) (*Model, error)

	// Write uploads data in chunkSize-sized chunks, followed by a final
	// chunk:-1. Servers that reject chunking get one single-shot PUT instead.
	Write(ctx context.Context, remotePath string, data []byte, chunkSize int) error

	// Delete removes a file or directory.
	Delete(ctx context.Context, remotePath string) error

	// ReadStream is Read but streams decoded content into dst instead of
	// retaining it in Model.Content. A nil dst behaves like Read.
	ReadStream(ctx context.Context, remotePath string, dst io.Writer) (*Model, error)

	// WriteStream is Write but takes an io.Reader instead of a byte slice.
	WriteStream(ctx context.Context, remotePath string, src io.Reader, chunkSize int) error
}

// StatusError reports a non-404, non-2xx response. It deliberately omits the
// response body and request URL: the URL carries the proxy token.
type StatusError struct {
	Op     string
	Status int
	Path   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("contents: %s: HTTP %d", e.Op, e.Status)
}

// TransportError reports a failure to send a request or read its response.
// The underlying net/http error is available through Unwrap for errors.Is,
// but is never rendered into this error's message (it embeds the tokenised
// URL).
type TransportError struct {
	Op  string
	Err error
}

func (e *TransportError) Error() string { return fmt.Sprintf("contents: %s: request failed", e.Op) }
func (e *TransportError) Unwrap() error { return e.Err }

type client struct {
	base  string
	token logx.Secret
	http  *http.Client
	agent string
	log   *slog.Logger
}

// New validates cfg and returns a ready client. A nil HTTPClient gets one with
// explicit connect/TLS/header/idle timeouts; a supplied *http.Client is copied
// and given the same defaults for any unset timeout.
func New(cfg Config) (Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return nil, errors.New("contents: BaseURL is required")
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("contents: invalid BaseURL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("contents: BaseURL must be an http(s) URL with a host and no credentials, query or fragment")
	}
	agent := strings.TrimSpace(cfg.ClientAgent)
	if agent == "" {
		agent = "colab-go"
	}
	logger := cfg.Logger
	if logger == nil {
		logger = logx.Logger()
	}
	return &client{
		base:  strings.TrimRight(base, "/"),
		token: cfg.Token,
		http:  cloneClient(cfg.HTTPClient),
		agent: agent,
		log:   logger,
	}, nil
}

func (c *client) List(ctx context.Context, remotePath string) (*Model, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.send(ctx, "list", http.MethodGet, remotePath, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeModel(newJSONDecoder(resp.Body), nil)
}

func (c *client) Read(ctx context.Context, remotePath string) (*Model, error) {
	return c.ReadStream(ctx, remotePath, nil)
}

func (c *client) ReadStream(ctx context.Context, remotePath string, dst io.Writer) (*Model, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.send(ctx, "read", http.MethodGet, remotePath,
		url.Values{"content": {"1"}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	m, err := decodeModel(newJSONDecoder(resp.Body), dst)
	if err != nil {
		return nil, fmt.Errorf("contents: decode %s: %w", logx.Redact(remotePath), err)
	}
	if m.Type == "directory" {
		return nil, fmt.Errorf("%w: %s", ErrIsDirectory, logx.Redact(remotePath))
	}
	return m, nil
}

func (c *client) Delete(ctx context.Context, remotePath string) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.send(ctx, "delete", http.MethodDelete, remotePath, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return &TransportError{Op: "delete", Err: err}
	}
	return nil
}

func (c *client) Write(ctx context.Context, remotePath string, data []byte, chunkSize int) error {
	return c.WriteStream(ctx, remotePath, bytes.NewReader(data), chunkSize)
}

// WriteStream sends chunk:1, chunk:2, … then chunk:-1 as documented by
// Jupyter's chunked save contract. If the very first chunk is rejected with a
// client error (or 501), the server is assumed not to support chunking and the
// whole file is retried as a single chunk:1 PUT, matching upstream's upload.
func (c *client) WriteStream(ctx context.Context, remotePath string, src io.Reader, chunkSize int) error {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	buf := make([]byte, chunkSize)
	chunk := 0
	for {
		n, readErr := io.ReadFull(src, buf)
		if n > 0 {
			chunk++
			if err := c.putChunk(ctx, remotePath, buf[:n], chunk); err != nil {
				if chunk == 1 && chunkRejected(err) {
					// Only the first chunk has been consumed from src; replay it
					// followed by the unread remainder in the single-shot PUT.
					return c.singleShot(ctx, remotePath, buf[:n], src)
				}
				return err
			}
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("contents: read upload data: %w", readErr)
		}
	}
	// Jupyter requires the content key on every chunk, including the last;
	// an empty string appends nothing when finalising the upload.
	return c.putChunk(ctx, remotePath, nil, -1)
}

func (c *client) singleShot(ctx context.Context, remotePath string, first []byte, rest io.Reader) error {
	// This path is only taken by servers that reject chunking. We cannot rewind
	// an arbitrary io.Reader, so this exceptional retry buffers the remainder
	// in memory; the normal streaming path never does.
	var all bytes.Buffer
	all.Grow(len(first))
	all.Write(first)
	if _, err := io.Copy(&all, rest); err != nil {
		return fmt.Errorf("contents: buffer fallback upload: %w", err)
	}
	return c.putChunk(ctx, remotePath, all.Bytes(), 1)
}

func chunkRejected(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	return (se.Status >= 400 && se.Status < 500) || se.Status == http.StatusNotImplemented
}

func (c *client) putChunk(ctx context.Context, remotePath string, data []byte, chunk int) error {
	clean := strings.Trim(remotePath, "/")
	payload := struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Type    string `json:"type"`
		Format  string `json:"format"`
		Content string `json:"content"`
		Chunk   int    `json:"chunk"`
	}{
		Name:    path.Base(clean),
		Path:    clean,
		Type:    "file",
		Format:  "base64",
		Content: base64.StdEncoding.EncodeToString(data),
		Chunk:   chunk,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("contents: encode upload chunk: %w", err)
	}
	resp, err := c.send(ctx, "write", http.MethodPut, remotePath, nil, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return &TransportError{Op: "write", Err: err}
	}
	return nil
}

// send builds the exact wire request for the Contents API: paths are trimmed
// and percent-encoded segment by segment, and the only credential is the
// colab-runtime-proxy-token query parameter. authuser=0 is always present.
func (c *client) send(ctx context.Context, op, method, remotePath string, extra url.Values, body io.Reader) (*http.Response, error) {
	q := url.Values{
		"authuser":                  {"0"},
		"colab-runtime-proxy-token": {c.token.Reveal()},
	}
	for key, values := range extra {
		q[key] = append([]string(nil), values...)
	}
	target := c.base + "/api/contents/" + encodeRemotePath(remotePath) + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, &TransportError{Op: op, Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Colab-Client-Agent", c.agent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.log.Debug("contents request", "op", op, "method", method, "path", logx.Redact(remotePath))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &TransportError{Op: op, Err: err}
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotFound, logx.Redact(remotePath))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		_ = resp.Body.Close()
		return nil, &StatusError{Op: op, Status: resp.StatusCode, Path: logx.Redact(remotePath)}
	}
	return resp, nil
}

func encodeRemotePath(remote string) string {
	clean := strings.Trim(remote, "/")
	if clean == "" {
		return ""
	}
	segments := strings.Split(clean, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// Model mirrors Jupyter's contents API model. Content holds decoded file bytes;
// Entries holds a directory's children; Raw holds an unmodified structured
// (JSON) content value so the model can be marshalled back to Jupyter's shape.
type Model struct {
	Name         string     `json:"name,omitempty"`
	Path         string     `json:"path,omitempty"`
	Type         string     `json:"type,omitempty"`
	Format       string     `json:"format,omitempty"`
	MimeType     string     `json:"mimetype,omitempty"`
	Writable     *bool      `json:"writable,omitempty"`
	Created      *time.Time `json:"created,omitempty"`
	LastModified *time.Time `json:"last_modified,omitempty"`

	Content []byte          `json:"-"`
	Entries []Model         `json:"-"`
	Raw     json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a Jupyter contents model, including the polymorphic
// content field (string for files, array of models for directories, and a JSON
// value for notebooks).
func (m *Model) UnmarshalJSON(data []byte) error {
	parsed, err := decodeModel(newJSONDecoder(bytes.NewReader(data)), nil)
	if err != nil {
		return err
	}
	*m = *parsed
	return nil
}

// MarshalJSON writes a Jupyter-compatible contents model.
func (m Model) MarshalJSON() ([]byte, error) {
	var content any
	switch {
	case m.Entries != nil:
		content = m.Entries
	case len(m.Raw) != 0:
		content = json.RawMessage(m.Raw)
	case m.Content != nil:
		switch m.Format {
		case "base64":
			content = base64.StdEncoding.EncodeToString(m.Content)
		case "json":
			content = json.RawMessage(m.Content)
		default:
			content = string(m.Content)
		}
	}
	return json.Marshal(struct {
		Name         string     `json:"name,omitempty"`
		Path         string     `json:"path,omitempty"`
		Type         string     `json:"type,omitempty"`
		Format       string     `json:"format,omitempty"`
		MimeType     string     `json:"mimetype,omitempty"`
		Writable     *bool      `json:"writable,omitempty"`
		Created      *time.Time `json:"created,omitempty"`
		LastModified *time.Time `json:"last_modified,omitempty"`
		Content      any        `json:"content,omitempty"`
	}{
		Name: m.Name, Path: m.Path, Type: m.Type, Format: m.Format,
		MimeType: m.MimeType, Writable: m.Writable, Created: m.Created,
		LastModified: m.LastModified, Content: content,
	})
}

func newJSONDecoder(r io.Reader) *json.Decoder {
	d := json.NewDecoder(r)
	d.UseNumber()
	return d
}

// decodeModel consumes one JSON object from dec. When dst is non-nil, decoded
// content is written there instead of being retained in Model.Content.
func decodeModel(dec *json.Decoder, dst io.Writer) (*Model, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("contents: expected a JSON object")
	}
	m := &Model{}
	var rawContent json.RawMessage
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, errors.New("contents: bad model key")
		}
		switch key {
		case "name":
			err = dec.Decode(&m.Name)
		case "path":
			err = dec.Decode(&m.Path)
		case "type":
			err = dec.Decode(&m.Type)
		case "format":
			err = dec.Decode(&m.Format)
		case "mimetype":
			err = dec.Decode(&m.MimeType)
		case "writable":
			err = dec.Decode(&m.Writable)
		case "created":
			err = decodeTime(dec, &m.Created)
		case "last_modified":
			err = decodeTime(dec, &m.LastModified)
		case "content":
			err = dec.Decode(&rawContent)
		default:
			var ignore json.RawMessage
			err = dec.Decode(&ignore)
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return nil, err
	}
	if err := m.applyContent(rawContent, dst); err != nil {
		return nil, err
	}
	return m, nil
}

func decodeTime(dec *json.Decoder, dst **time.Time) error {
	var s string
	if err := dec.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil // tolerate unexpected server formats
	}
	*dst = &t
	return nil
}

func (m *Model) applyContent(raw json.RawMessage, dst io.Writer) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	switch raw[0] {
	case '[':
		entries := make([]Model, 0)
		if err := newJSONDecoder(bytes.NewReader(raw)).Decode(&entries); err != nil {
			return err
		}
		m.Entries = entries
		return nil
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		return m.applyStringContent(s, dst)
	default:
		m.Raw = append(json.RawMessage(nil), raw...)
		return m.applyJSONContent(raw, dst)
	}
}

func (m *Model) applyStringContent(s string, dst io.Writer) error {
	if m.Format == "base64" {
		decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(s))
		if dst != nil {
			_, err := io.Copy(dst, decoder)
			return err
		}
		b, err := io.ReadAll(decoder)
		if err != nil {
			return err
		}
		m.Content = b
		return nil
	}
	if dst != nil {
		_, err := io.WriteString(dst, s)
		return err
	}
	m.Content = []byte(s)
	return nil
}

func (m *Model) applyJSONContent(raw json.RawMessage, dst io.Writer) error {
	var value any
	if err := newJSONDecoder(bytes.NewReader(raw)).Decode(&value); err != nil {
		return err
	}
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if dst != nil {
		_, err := dst.Write(out)
		return err
	}
	m.Content = out
	return nil
}

func cloneClient(src *http.Client) *http.Client {
	var cloned http.Client
	if src != nil {
		cloned = *src
	}
	switch t := cloned.Transport.(type) {
	case nil:
		cloned.Transport = defaultTransport()
	case *http.Transport:
		t2 := t.Clone()
		applyTransportDefaults(t2)
		cloned.Transport = t2
	default:
		// A caller-supplied RoundTripper (e.g. a test fake) is left untouched.
	}
	return &cloned
}

func defaultTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	applyTransportDefaults(t)
	return t
}

func applyTransportDefaults(t *http.Transport) {
	if t.DialContext == nil {
		t.DialContext = (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext
	}
	if t.TLSHandshakeTimeout == 0 {
		t.TLSHandshakeTimeout = tlsTimeout
	}
	if t.ResponseHeaderTimeout == 0 {
		t.ResponseHeaderTimeout = headerTimeout
	}
	if t.IdleConnTimeout == 0 {
		t.IdleConnTimeout = idleTimeout
	}
	if t.ExpectContinueTimeout == 0 {
		t.ExpectContinueTimeout = time.Second
	}
}
