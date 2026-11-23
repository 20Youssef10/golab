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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

const testToken = "secret-ptoken-DO-NOT-LEAK-123456"

type recordedRequest struct {
	method      string
	escapedPath string
	query       url.Values
	rawQuery    string
	header      http.Header
	body        []byte
}

type fakeContents struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recordedRequest
}

func newFakeContents(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *fakeContents {
	t.Helper()
	f := &fakeContents{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		q, _ := url.ParseQuery(r.URL.RawQuery)
		f.mu.Lock()
		f.reqs = append(f.reqs, recordedRequest{
			method:      r.Method,
			escapedPath: r.URL.EscapedPath(),
			query:       q,
			rawQuery:    r.URL.RawQuery,
			header:      r.Header.Clone(),
			body:        body,
		})
		f.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeContents) requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.reqs...)
}

func testConfig(baseURL string) Config {
	return Config{
		BaseURL:     baseURL,
		Token:       logx.NewSecret(testToken),
		ClientAgent: "colab-go-test",
	}
}

func mustClient(t *testing.T, cfg Config) Client {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func assertWire(t *testing.T, req recordedRequest, wantParams int) {
	t.Helper()
	if !strings.HasPrefix(req.escapedPath, "/api/contents/") {
		t.Errorf("path = %q, want /api/contents/…", req.escapedPath)
	}
	if got := req.query.Get("authuser"); got != "0" {
		t.Errorf("authuser = %q, want 0 (raw query %q)", got, req.rawQuery)
	}
	if got := req.query.Get("colab-runtime-proxy-token"); got != testToken {
		t.Errorf("proxy token query param = %q, want %q", got, testToken)
	}
	if len(req.query) != wantParams {
		t.Errorf("query params = %v, want exactly %d", req.query, wantParams)
	}
	// Doc 02 §3.1: for the Contents API the token goes in the query ONLY.
	for _, h := range []string{"X-Colab-Runtime-Proxy-Token", "Authorization", "X-Goog-Colab-Token"} {
		if got := req.header.Get(h); got != "" {
			t.Errorf("unexpected %s header: %q", h, got)
		}
	}
	if got := req.header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json", got)
	}
	if got := req.header.Get("X-Colab-Client-Agent"); got != "colab-go-test" {
		t.Errorf("X-Colab-Client-Agent = %q, want colab-go-test", got)
	}
}

func chunkPayload(t *testing.T, body []byte) chunkModel {
	t.Helper()
	var p chunkModel
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode chunk payload %q: %v", body, err)
	}
	return p
}

type chunkModel struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Format  string `json:"format"`
	Content string `json:"content"`
	Chunk   int    `json:"chunk"`
}

// TestList ports upstream tests/test_contents.py::test_list_dir.
func TestList(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"name": "content",
			"type": "directory",
			"content": []any{
				map[string]any{"name": "file.txt", "type": "file"},
				map[string]any{"name": "dir", "type": "directory"},
			},
		})
	})
	c := mustClient(t, testConfig(srv.URL))

	m, err := c.List(context.Background(), "content")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if m.Type != "directory" || m.Name != "content" {
		t.Fatalf("model = %+v, want directory named content", m)
	}
	if len(m.Entries) != 2 || m.Entries[0].Name != "file.txt" || m.Entries[1].Type != "directory" {
		t.Fatalf("entries = %+v", m.Entries)
	}

	reqs := srv.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if reqs[0].method != http.MethodGet || reqs[0].escapedPath != "/api/contents/content" {
		t.Errorf("request = %s %s", reqs[0].method, reqs[0].escapedPath)
	}
	assertWire(t, reqs[0], 2)
}

// TestListTrailingSlashAndLeadingSlash checks remote paths are stripped on both
// ends and are relative to the VM root (content == /content).
func TestListTrailingSlashAndLeadingSlash(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"name": "content", "type": "directory"})
	})
	c := mustClient(t, testConfig(srv.URL+"/"))

	if _, err := c.List(context.Background(), "/content/"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := srv.requests()[0].escapedPath; got != "/api/contents/content" {
		t.Errorf("path = %q, want /api/contents/content", got)
	}
}

// TestDelete ports upstream tests/test_contents.py::test_rm_file.
func TestDelete(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := mustClient(t, testConfig(srv.URL))

	if err := c.Delete(context.Background(), "content/file.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reqs := srv.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodDelete || reqs[0].escapedPath != "/api/contents/content/file.txt" {
		t.Fatalf("requests = %+v", reqs)
	}
	if len(reqs[0].body) != 0 {
		t.Errorf("DELETE body = %q, want empty", reqs[0].body)
	}
	assertWire(t, reqs[0], 2)
}

// TestNotFoundIsTyped ports upstream tests/test_contents.py::test_404_error.
func TestNotFoundIsTyped(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "not found"})
	})
	c := mustClient(t, testConfig(srv.URL))

	_, err := c.List(context.Background(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error leaked token: %q", err.Error())
	}
	if err := c.Delete(context.Background(), "nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete err = %v, want ErrNotFound", err)
	}
}

// TestReadBase64 ports upstream tests/test_contents.py::test_download_file.
func TestReadBase64(t *testing.T) {
	want := []byte("Hello world!")
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"name":    "test.txt",
			"type":    "file",
			"format":  "base64",
			"content": base64.StdEncoding.EncodeToString(want),
		})
	})
	c := mustClient(t, testConfig(srv.URL))

	m, err := c.Read(context.Background(), "content/test.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.Format != "base64" || m.Type != "file" {
		t.Fatalf("model = %+v", m)
	}
	if !bytes.Equal(m.Content, want) {
		t.Fatalf("content = %q, want %q", m.Content, want)
	}
	reqs := srv.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].escapedPath != "/api/contents/content/test.txt" {
		t.Fatalf("requests = %+v", reqs)
	}
	assertWire(t, reqs[0], 3)
	if reqs[0].query.Get("content") != "1" {
		t.Errorf("content param = %q, want 1", reqs[0].query.Get("content"))
	}
}

// TestReadJSONFormat ports upstream
// tests/test_contents.py::test_download_notebook_json_format.
func TestReadJSONFormat(t *testing.T) {
	doc := map[string]any{
		"cells": []any{
			map[string]any{"cell_type": "markdown", "metadata": map[string]any{}, "source": []any{"# Demo"}},
			map[string]any{"cell_type": "code", "execution_count": 1, "metadata": map[string]any{}, "outputs": []any{}, "source": []any{"x = 42\n", "print(x)"}},
		},
		"metadata":       map[string]any{"kernelspec": map[string]any{"display_name": "Python 3", "name": "python3"}},
		"nbformat":       4,
		"nbformat_minor": 5,
	}
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"name": "demo.ipynb", "type": "notebook", "format": "json", "content": doc,
		})
	})
	c := mustClient(t, testConfig(srv.URL))

	m, err := c.Read(context.Background(), "content/demo.ipynb")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(m.Content) != string(want) {
		t.Fatalf("json content =\n%s\nwant\n%s", m.Content, want)
	}
	var decoded any
	if err := json.Unmarshal(m.Content, &decoded); err != nil {
		t.Fatalf("content is not valid JSON: %v", err)
	}
}

// TestReadTextFormat covers the "else assume text" branch of upstream
// contents.py::download.
func TestReadTextFormat(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"name": "notes.txt", "type": "file", "format": "text", "content": "héllo\nworld",
		})
	})
	c := mustClient(t, testConfig(srv.URL))

	m, err := c.Read(context.Background(), "content/notes.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(m.Content) != "héllo\nworld" {
		t.Fatalf("content = %q", m.Content)
	}
}

// TestReadDirectoryIsTyped covers upstream's IsADirectoryError branch with a
// typed Go error.
func TestReadDirectoryIsTyped(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"name": "content", "type": "directory", "content": []any{}})
	})
	c := mustClient(t, testConfig(srv.URL))

	if _, err := c.Read(context.Background(), "content"); !errors.Is(err, ErrIsDirectory) {
		t.Fatalf("err = %v, want ErrIsDirectory", err)
	}
}

// TestWriteChunked extends upstream tests/test_contents.py::test_upload_file to
// the streaming, chunked upload required by D-4 / Δ5.
func TestWriteChunked(t *testing.T) {
	data := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	var got bytes.Buffer
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"name": "test.txt", "type": "file"})
	})
	c := mustClient(t, testConfig(srv.URL))

	if err := c.Write(context.Background(), "content/test.txt", data, 10); err != nil {
		t.Fatalf("Write: %v", err)
	}

	reqs := srv.requests()
	if len(reqs) != 5 { // 36 bytes / 10 => 4 data chunks + final chunk:-1
		t.Fatalf("requests = %d, want 5", len(reqs))
	}
	var chunks []int
	for _, req := range reqs {
		if req.method != http.MethodPut || req.escapedPath != "/api/contents/content/test.txt" {
			t.Fatalf("request = %s %s", req.method, req.escapedPath)
		}
		assertWire(t, req, 2)
		if ct := req.header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		p := chunkPayload(t, req.body)
		if p.Name != "test.txt" || p.Path != "content/test.txt" || p.Type != "file" || p.Format != "base64" {
			t.Errorf("payload metadata = %+v", p)
		}
		chunks = append(chunks, p.Chunk)
		if p.Chunk == -1 {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(p.Content)
		if err != nil {
			t.Fatalf("bad base64 in chunk %d: %v", p.Chunk, err)
		}
		got.Write(b)
	}
	wantChunks := []int{1, 2, 3, 4, -1} // 35 bytes / 10 => 4 chunks
	if fmt.Sprint(chunks) != fmt.Sprint(wantChunks) {
		t.Errorf("chunks = %v, want %v", chunks, wantChunks)
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Errorf("uploaded = %q, want %q", got.Bytes(), data)
	}
}

// TestWriteFallbackSingleShot verifies the single-shot fallback when a server
// rejects chunked uploads (Δ5). The fake rejects the first chunk with 400 and
// accepts a whole-file PUT with chunk:1, exactly like upstream uploads.
func TestWriteFallbackSingleShot(t *testing.T) {
	data := []byte("Hello upload! - a payload longer than a tiny chunk size")
	calls := 0
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "chunked upload unsupported"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": "test.txt"})
	})
	c := mustClient(t, testConfig(srv.URL))

	if err := c.Write(context.Background(), "content/test.txt", data, 8); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if calls != 2 {
		t.Fatalf("server calls = %d, want 2 (chunk rejected, single-shot accepted)", calls)
	}
	reqs := srv.requests()
	fallback := chunkPayload(t, reqs[1].body)
	if fallback.Chunk != 1 {
		t.Errorf("fallback chunk = %d, want 1", fallback.Chunk)
	}
	b, err := base64.StdEncoding.DecodeString(fallback.Content)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, data) {
		t.Errorf("fallback content = %q, want %q", b, data)
	}
	for _, req := range reqs {
		assertWire(t, req, 2)
	}
}

// TestWriteStream verifies the io.Reader upload helper (no whole-file buffer in
// the public API).
func TestWriteStream(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 25)
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	c := mustClient(t, testConfig(srv.URL))

	if err := c.WriteStream(context.Background(), "content/blob.bin", bytes.NewReader(data), 7); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}
	var got bytes.Buffer
	var seen []int
	for _, req := range srv.requests() {
		p := chunkPayload(t, req.body)
		seen = append(seen, p.Chunk)
		if p.Chunk > 0 {
			b, _ := base64.StdEncoding.DecodeString(p.Content)
			got.Write(b)
		}
	}
	if fmt.Sprint(seen) != fmt.Sprint([]int{1, 2, 3, 4, -1}) {
		t.Errorf("chunks = %v", seen)
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Errorf("uploaded %d bytes, want %d", got.Len(), len(data))
	}
}

// TestReadStream verifies the io.Writer download helper: content is written to
// dst and not retained in the model.
func TestReadStream(t *testing.T) {
	want := bytes.Repeat([]byte("streamed"), 16)
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"name": "blob.bin", "type": "file", "format": "base64",
			"content": base64.StdEncoding.EncodeToString(want),
		})
	})
	c := mustClient(t, testConfig(srv.URL))

	var dst bytes.Buffer
	m, err := c.ReadStream(context.Background(), "content/blob.bin", &dst)
	if err != nil {
		t.Fatalf("ReadStream: %v", err)
	}
	if !bytes.Equal(dst.Bytes(), want) {
		t.Fatalf("streamed %d bytes, want %d", dst.Len(), len(want))
	}
	if len(m.Content) != 0 {
		t.Errorf("model retained %d content bytes in streaming mode", len(m.Content))
	}
}

// TestPathEncoding guards doc 02 §3.8 path handling: strip outer slashes,
// percent-encode each segment, keep internal '/' characters.
func TestPathEncoding(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := mustClient(t, testConfig(srv.URL))

	remote := "/content/dir with space/has#hash?q=1/naïve.txt/"
	if _, err := c.List(context.Background(), remote); err != nil {
		t.Fatalf("List: %v", err)
	}
	got := srv.requests()[0].escapedPath
	want := "/api/contents/content/dir%20with%20space/has%23hash%3Fq=1/na%C3%AFve.txt"
	if got != want {
		t.Errorf("escaped path = %q, want %q", got, want)
	}
}

// TestStatusError checks the typed error for non-404 failures.
func TestStatusError(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "boom"})
	})
	c := mustClient(t, testConfig(srv.URL))

	_, err := c.List(context.Background(), "content")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v (%T), want StatusError 500", err, err)
	}
	if se.Op != "list" {
		t.Errorf("op = %q, want list", se.Op)
	}
}

// TestSecretsNeverLeak checks errors and logs across all failure paths.
func TestSecretsNeverLeak(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "boom") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("token=" + testToken))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	var logs bytes.Buffer
	cfg := testConfig(srv.URL)
	cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	c := mustClient(t, cfg)

	_, err := c.List(context.Background(), "boom")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, s := range []string{err.Error(), logs.String()} {
		if strings.Contains(s, testToken) {
			t.Errorf("secret leaked: %q", s)
		}
	}

	// Network failures from net/http embed the URL (including the token query
	// parameter) in *url.Error; our error must not forward that string.
	srv.Close()
	_, err = c.List(context.Background(), "content")
	if err == nil {
		t.Fatal("expected transport error after server close")
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "colab-runtime-proxy-token") {
		t.Errorf("transport error leaked token: %q", err.Error())
	}
}

// TestContextTimeout verifies explicit, context-driven deadlines.
func TestContextTimeout(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	c := mustClient(t, testConfig(srv.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.List(ctx, "content")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("timeout error leaked token: %q", err.Error())
	}
}

// TestNewValidation checks constructor validation and defaults.
func TestNewValidation(t *testing.T) {
	for _, bad := range []string{"", "://bad", "ftp://host", "http://"} {
		if _, err := New(Config{BaseURL: bad}); err == nil {
			t.Errorf("New(%q) succeeded, want error", bad)
		}
	}
	c, err := New(Config{BaseURL: "https://example.invalid/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
}

// TestModelJSONRoundTrip checks the model stays compatible with Jupyter's
// contents API JSON shape.
func TestModelJSONRoundTrip(t *testing.T) {
	raw := []byte(`{"name":"content","type":"directory","content":[{"name":"a.txt","type":"file","format":"text","content":"hello"}]}`)
	var m Model
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(m.Entries) != 1 || m.Entries[0].Name != "a.txt" || string(m.Entries[0].Content) != "hello" {
		t.Fatalf("model = %+v", m)
	}
	out, err := json.Marshal(&m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back Model
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("round-trip Unmarshal: %v", err)
	}
	if len(back.Entries) != 1 || string(back.Entries[0].Content) != "hello" {
		t.Fatalf("round-trip model = %+v", back)
	}
}
