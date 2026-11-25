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

// MaxRequestBytes is Colab's request-body ceiling (100 MiB). Content is sent
// base64-encoded inside JSON (4/3 of the file size), so a "small" upload is
// one that fits under this budget after encoding — roughly 75 MiB.
const MaxRequestBytes = 100 << 20 // 100 MiB

// UploadChunkSize is the chunk size for files too large for one request. A
// 64 MiB chunk is ~85 MiB encoded, comfortably inside MaxRequestBytes.
const UploadChunkSize = 64 << 20 // 64 MiB

// MaxRecursionDepth bounds recursive directory walks (upstream PR #267).
const MaxRecursionDepth = 64

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
	// ErrIncompleteUpload means a chunked upload failed after data may already
	// have been written remotely. Callers must not treat the remote file as
	// valid; see IncompleteUploadError for the number of chunks sent.
	ErrIncompleteUpload = errors.New("contents: incomplete upload")
	// ErrNotDirectory means UploadDir was given a non-directory source.
	ErrNotDirectory = errors.New("contents: source is not a directory")
	// ErrUnsafePath means a remote path or an untrusted remote entry name
	// contained traversal characters (.., /, \\, NUL).
	ErrUnsafePath = errors.New("contents: unsafe path")
	// ErrSymlink means a local symlink was found on a recursive transfer path.
	ErrSymlink = errors.New("contents: local symlink not allowed")
	// ErrMaxDepth means a recursive walk exceeded MaxRecursionDepth.
	ErrMaxDepth = errors.New("contents: maximum recursion depth exceeded")
	// ErrMissingContent means a remote file model had no content field.
	ErrMissingContent = errors.New("contents: missing file content")
)

// IncompleteUploadError is returned when a chunked upload fails after the
// first chunk was accepted. The remote file may hold only part of the local
// data — even a failed chunk 1 may have been stored if only the response was
// lost. Only a redacted, token-free detail string is retained: the underlying
// transport error's text can embed the tokenised request URL.
type IncompleteUploadError struct {
	RemotePath string
	ChunksSent int
	Detail     string
}

func (e *IncompleteUploadError) Error() string {
	return fmt.Sprintf("contents: upload of %s incomplete after %d chunk(s): %s",
		logx.Redact(e.RemotePath), e.ChunksSent, e.Detail)
}

// Unwrap deliberately exposes only the sentinel, never the token-bearing
// cause, so errors.Is works without leaking a secret through the chain.
func (e *IncompleteUploadError) Unwrap() error { return ErrIncompleteUpload }

func describeUploadError(err error) string {
	if err == nil {
		return ""
	}
	// Only typed, token-free details from our own errors are rendered
	// verbatim. Anything else gets a defensive redaction pass; a refresh
	// callback's plain errors (e.g. "credential refresh denied") survive.
	var se *StatusError
	var te *TransportError
	switch {
	case errors.As(err, &se):
		return fmt.Sprintf("HTTP %d", se.Status)
	case errors.As(err, &te):
		return te.Error()
	case errors.Is(err, ErrNotFound):
		return ErrNotFound.Error()
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	}
	return logx.Redact(err.Error())
}

// RefreshInfo carries a renewed data-plane credential for a long upload.
// An empty BaseURL leaves the current base URL unchanged.
type RefreshInfo struct {
	Token   logx.Secret
	BaseURL string
}

// TokenRefresher is called before every chunk after the first so an upload
// that outlives the runtime proxy token can pick up a renewed one (upstream
// PR #272). It must never return a token in an error message.
type TokenRefresher func(ctx context.Context) (RefreshInfo, error)

// Config holds the dependencies of a Contents API client. Token is the data-
// plane runtime-proxy token, never the control-plane OAuth bearer.
type Config struct {
	BaseURL    string
	Token      logx.Secret
	HTTPClient *http.Client
	// ClientAgent is accepted for API compatibility with the other planes but
	// is deliberately NOT sent: upstream's Contents API uses query-only
	// credentials and no client-agent header (AUDIT-02 F-1 / S-1).
	ClientAgent string
	Logger      *slog.Logger
	// RefreshToken, if set, is called before each chunk after the first.
	RefreshToken TokenRefresher
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

	// Write uploads data. A file that fits in one request is one atomic
	// chunk:1 PUT; larger files use the Jupyter chunked protocol and surface
	// *IncompleteUploadError if any chunk fails (AUDIT-02 S-2).
	Write(ctx context.Context, remotePath string, data []byte, chunkSize int) error

	// Delete removes a file or directory.
	Delete(ctx context.Context, remotePath string) error

	// ReadStream is Read but streams decoded content into dst instead of
	// retaining it in Model.Content. A nil dst behaves like Read.
	ReadStream(ctx context.Context, remotePath string, dst io.Writer) (*Model, error)

	// WriteStream is Write but takes an io.Reader instead of a byte slice.
	WriteStream(ctx context.Context, remotePath string, src io.Reader, chunkSize int) error

	// Mkdir creates a remote directory with a model PUT.
	Mkdir(ctx context.Context, remotePath string) error

	// UploadDir recursively uploads a local directory tree, preserving empty
	// directories and skipping local symlinks; it returns the file count.
	UploadDir(ctx context.Context, localDir, remoteDir string) (int, error)

	// DownloadDir recursively downloads a remote directory tree into a local
	// directory (including empty directories), or downloads a single remote file
	// when the target is not a directory. It returns the file count.
	DownloadDir(ctx context.Context, remotePath, localPath string) (int, error)
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
	base            string
	token           logx.Secret
	http            *http.Client
	log             *slog.Logger
	refreshToken    TokenRefresher
	maxRequestBytes int // tests may lower this to exercise chunking
	maxDepth        int // tests may lower this to exercise the depth guard
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
	logger := cfg.Logger
	if logger == nil {
		logger = logx.Logger()
	}
	return &client{
		base:            strings.TrimRight(base, "/"),
		token:           cfg.Token,
		http:            cloneClient(cfg.HTTPClient),
		log:             logger,
		refreshToken:    cfg.RefreshToken,
		maxRequestBytes: MaxRequestBytes,
		maxDepth:        MaxRecursionDepth,
	}, nil
}

func (c *client) List(ctx context.Context, remotePath string) (*Model, error) {
	return c.getModel(ctx, remotePath, false)
}

// getModel fetches a model, optionally asking for content. It is shared by
// List, the recursive walk and upload-size verification.
func (c *client) getModel(ctx context.Context, remotePath string, withContent bool) (*Model, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var extra url.Values
	if withContent {
		extra = url.Values{"content": {"1"}}
	}
	resp, err := c.send(ctx, "list", http.MethodGet, remotePath, extra, nil)
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

// WriteStream implements the safe upload policy of AUDIT-02 S-2 / upstream
// PR #272:
//
//   - A file that fits in one request is sent as ONE `chunk:1` PUT. That is the
//     upstream-proven behaviour and it is atomic: the remote file is either
//     replaced completely or left as it was. The unproven empty `chunk:-1`
//     finalisation is never used for small files.
//   - A larger file streams chunk:1, chunk:2, … with the last DATA chunk
//     marked -1 (no empty trailing PUT). Failed chunks after data was sent
//     produce *IncompleteUploadError, never a silent truncation.
//   - Every chunked upload ends with a verification GET: a failed GET or a
//     reported size that disagrees with the local length (or, for a source of
//     unknown length, with the bytes actually PUT) is a typed incomplete
//     upload. A server that reports no size cannot be verified from metadata.
//   - `Config.RefreshToken` is called before every chunk after the first so a
//     long upload survives proxy-token expiry.
func (c *client) WriteStream(ctx context.Context, remotePath string, src io.Reader, chunkSize int) error {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize > UploadChunkSize {
		chunkSize = UploadChunkSize
	}
	if !c.fitsOneRequest(remotePath, int64(chunkSize), -1) {
		return fmt.Errorf("contents: chunk size %d does not fit in one request", chunkSize)
	}
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	if size, ok := sourceSize(src); ok {
		if c.fitsOneRequest(remotePath, size, 1) {
			data, err := io.ReadAll(io.LimitReader(src, size+1))
			if err != nil {
				return fmt.Errorf("contents: read upload data: %w", err)
			}
			return c.singleShot(ctx, remotePath, data)
		}
		return c.writeChunks(ctx, remotePath, src, chunkSize, size)
	}

	// Non-seekable source: buffer up to one request's worth. If EOF arrives
	// inside the budget it is a small, atomic single-shot upload; otherwise
	// continue with the streaming path (the buffer becomes the first chunks).
	budget := c.maxRequestBytes - uploadEnvelopeSize(remotePath, 1)
	if budget < 1 {
		budget = 1
	}
	prefix, err := io.ReadAll(io.LimitReader(src, int64(budget)+1))
	if err != nil {
		return fmt.Errorf("contents: read upload data: %w", err)
	}
	if int64(len(prefix)) <= int64(budget) {
		return c.singleShot(ctx, remotePath, prefix)
	}
	return c.writeChunks(ctx, remotePath, io.MultiReader(bytes.NewReader(prefix), src), chunkSize, -1)
}

func (c *client) singleShot(ctx context.Context, remotePath string, data []byte) error {
	return c.putChunk(ctx, remotePath, data, 1)
}

// writeChunks streams src in chunkSize-sized PUTs. The last data chunk carries
// chunk:-1; "size" is the known content length or -1 when unknown.
func (c *client) writeChunks(ctx context.Context, remotePath string, src io.Reader, chunkSize int, size int64) error {
	buf := make([]byte, chunkSize)
	read := func() ([]byte, error) {
		n, err := io.ReadFull(src, buf)
		if n == 0 {
			return nil, err
		}
		return append([]byte(nil), buf[:n]...), err
	}
	isEOF := func(err error) bool {
		return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
	}
	hardError := func(err error) bool { return err != nil && !isEOF(err) }

	current, readErr := read()
	if len(current) == 0 {
		if hardError(readErr) {
			return fmt.Errorf("contents: read upload data: %w", readErr)
		}
		// Empty file: one atomic chunk:1 PUT (never a lone chunk:-1).
		return c.singleShot(ctx, remotePath, nil)
	}
	if hardError(readErr) {
		return fmt.Errorf("contents: read upload data: %w", readErr)
	}

	// One-chunk lookahead: `current` is final exactly when the next read
	// produced no data. This marks the last DATA chunk -1 (no empty tail) and
	// also handles exact multiples of chunkSize correctly.
	next, nextErr := []byte(nil), error(nil)
	if !isEOF(readErr) {
		next, nextErr = read()
	}
	sent := 0
	var bytesSent int64
	for {
		readFailed := hardError(nextErr)
		// A hard read error means we cannot know whether more data followed,
		// so never finalise on it: send what we have and report it incomplete.
		final := len(next) == 0 && !readFailed
		if sent > 0 {
			if err := c.refreshTokenForChunk(ctx); err != nil {
				return c.incomplete(remotePath, sent, "token refresh", err)
			}
		}
		marker := sent + 1
		if final {
			marker = -1
		}
		if err := c.putChunk(ctx, remotePath, current, marker); err != nil {
			return c.chunkFailure(remotePath, sent, err)
		}
		sent++
		bytesSent += int64(len(current))
		if final {
			break
		}
		if readFailed {
			return c.incomplete(remotePath, sent, "read upload data", nextErr)
		}
		current = next
		if isEOF(nextErr) {
			next, nextErr = nil, io.EOF // final data chunk; no further lookahead
		} else {
			next, nextErr = read()
		}
	}
	// A source of unknown length carries size = -1; the bytes actually PUT are
	// the only local measurement available for the final verification.
	expected := size
	if expected < 0 {
		expected = bytesSent
	}
	if err := c.verifyRemoteSize(ctx, remotePath, expected, sent); err != nil {
		return err
	}
	return nil
}

func (c *client) refreshTokenForChunk(ctx context.Context) error {
	if c.refreshToken == nil {
		return nil
	}
	info, err := c.refreshToken(ctx)
	if err != nil {
		return err
	}
	if info.Token.Reveal() != "" {
		c.token = info.Token
	}
	if base := strings.TrimSpace(info.BaseURL); base != "" {
		c.base = strings.TrimRight(base, "/")
	}
	return nil
}

// chunkFailure keeps the original error only when the very first chunk was
// explicitly refused (nothing can have been written). Every other failure is
// reported as a typed incomplete upload.
func (c *client) chunkFailure(remotePath string, sent int, err error) error {
	var se *StatusError
	rejected := errors.Is(err, ErrNotFound) ||
		(errors.As(err, &se) && se.Status >= 400 && se.Status < 500)
	if sent == 0 && rejected {
		return err
	}
	return c.incomplete(remotePath, sent, "chunk", err)
}

func (c *client) incomplete(remotePath string, sent int, op string, err error) error {
	return &IncompleteUploadError{
		RemotePath: remotePath,
		ChunksSent: sent,
		Detail:     op + ": " + describeUploadError(err),
	}
}

// verifyRemoteSize compares the server-reported model size with the expected
// local length (known from the source, or the bytes actually PUT for a source
// of unknown length). A failed verification GET or a reported size that
// disagrees with the expected length is a loud incomplete upload; only a
// server that reports no size stays optimistic, because it cannot be verified
// from metadata. A live GET/SHA-256 probe (AUDIT-02 §2 item 4) remains HUMAN
// REQUIRED before declaring D-4 proven. Caveat (also noted in upstream PR
// #272): Jupyter reports os.lstat sizes, so an upload through a symlinked
// remote destination can look short.
func (c *client) verifyRemoteSize(ctx context.Context, remotePath string, expected int64, sent int) error {
	m, err := c.getModel(ctx, remotePath, false)
	if err != nil {
		return &IncompleteUploadError{
			RemotePath: remotePath,
			ChunksSent: sent,
			Detail:     "verify: " + describeUploadError(err),
		}
	}
	if m.Size == nil {
		return nil // no size reported: cannot verify; the live probe is HUMAN REQUIRED
	}
	if *m.Size != expected {
		return &IncompleteUploadError{
			RemotePath: remotePath,
			ChunksSent: sent,
			Detail:     fmt.Sprintf("verify: remote size %d does not match local size %d", *m.Size, expected),
		}
	}
	return nil
}

// sourceSize reports the remaining length of a seekable reader without moving
// its read offset.
func sourceSize(src io.Reader) (int64, bool) {
	s, ok := src.(io.Seeker)
	if !ok {
		return 0, false
	}
	cur, err := s.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, false
	}
	end, err := s.Seek(0, io.SeekEnd)
	if err != nil {
		_, _ = s.Seek(cur, io.SeekStart)
		return 0, false
	}
	if _, err := s.Seek(cur, io.SeekStart); err != nil {
		return 0, false
	}
	return end - cur, true
}

// uploadEnvelopeSize is the exact JSON body size of an empty upload payload
// for the given remote path and chunk marker.
func uploadEnvelopeSize(remotePath string, chunk int) int {
	clean := strings.Trim(remotePath, "/")
	body, err := json.Marshal(struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Type    string `json:"type"`
		Format  string `json:"format"`
		Content string `json:"content"`
		Chunk   int    `json:"chunk"`
	}{Name: path.Base(clean), Path: clean, Type: "file", Format: "base64", Content: "", Chunk: chunk})
	if err != nil {
		return 0
	}
	return len(body)
}

// fitsOneRequest mirrors upstream's fits_in_one_request: base64 expands data
// to 4*ceil(n/3) bytes inside the JSON envelope.
func (c *client) fitsOneRequest(remotePath string, size int64, chunk int) bool {
	if c.maxRequestBytes <= 0 {
		return true
	}
	if size < 0 {
		return false
	}
	encoded := 4 * ((size + 2) / 3)
	return int64(uploadEnvelopeSize(remotePath, chunk))+encoded <= int64(c.maxRequestBytes)
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
	// AUDIT-02 F-1/S-1: upstream sends NO client-agent header here; the token
	// is query-only. Do not reintroduce one without a live GET probe.
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
	// Size is the server-reported byte size (Jupyter reports os.lstat sizes).
	Size *int64 `json:"size,omitempty"`

	Content []byte          `json:"-"`
	Entries []Model         `json:"-"`
	Raw     json.RawMessage `json:"-"`

	contentPresent bool
}

// ContentPresent reports whether the server actually supplied a content field.
// A file model with no content must not be written as an empty local file.
func (m Model) ContentPresent() bool { return m.contentPresent }

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
		Size         *int64     `json:"size,omitempty"`
		Content      any        `json:"content,omitempty"`
	}{
		Name: m.Name, Path: m.Path, Type: m.Type, Format: m.Format,
		MimeType: m.MimeType, Writable: m.Writable, Created: m.Created,
		LastModified: m.LastModified, Size: m.Size, Content: content,
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
		case "size":
			err = dec.Decode(&m.Size)
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
	m.contentPresent = true
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
