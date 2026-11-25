package contents

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/logx"
)

// smallBudgetClient lowers the per-request envelope budget so a tiny payload
// exercises the *chunked* path without allocating tens of megabytes in tests.
func smallBudgetClient(t *testing.T, cfg Config, remotePath string, slack int) Client {
	t.Helper()
	c := mustClient(t, cfg).(*client)
	c.maxRequestBytes = uploadEnvelopeSize(remotePath, 1) + slack
	return c
}

// TestWriteStreamServerIgnoresChunkField is audit P0-1 (S-2): a fake server
// that accepts the chunk field but writes each body verbatim must never leave
// a corrupt remote file. With single-shot-by-default, one `chunk:1` PUT saves
// the whole file, so the final content is byte-for-byte the input.
func TestWriteStreamServerIgnoresChunkField(t *testing.T) {
	data := bytes.Repeat([]byte("chunk-corruption-canary-"), 64)
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		// The server deliberately ignores `chunk` and saves each body verbatim.
		if r.Method != http.MethodPut {
			t.Errorf("unexpected method %s", r.Method)
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := mustClient(t, testConfig(srv.URL))

	if err := c.WriteStream(context.Background(), "content/blob.bin", bytes.NewReader(data), 7); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}
	reqs := srv.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want exactly one single-shot PUT", len(reqs))
	}
	p := chunkPayload(t, reqs[0].body)
	if p.Chunk != 1 {
		t.Fatalf("chunk = %d, want 1", p.Chunk)
	}
	got, err := base64.StdEncoding.DecodeString(p.Content)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("remote content differs from local input (len %d vs %d)", len(got), len(data))
	}
}

// TestWriteExactMultipleOfChunkSize ports upstream
// tests/test_contents.py::test_upload_exact_multiple_of_chunk_size: the last
// full data chunk is marked -1 and no empty trailing PUT is sent.
func TestWriteExactMultipleOfChunkSize(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := smallBudgetClient(t, testConfig(srv.URL), "content/blob.bin", 9)
	if err := c.Write(context.Background(), "content/blob.bin", []byte("01234567"), 4); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var puts []recordedRequest
	for _, req := range srv.requests() {
		if req.method == http.MethodPut {
			puts = append(puts, req)
		}
	}
	if len(puts) != 2 {
		t.Fatalf("PUTs = %d, want 2", len(puts))
	}
	if got := chunkPayload(t, puts[0].body).Chunk; got != 1 {
		t.Errorf("first chunk = %d, want 1", got)
	}
	last := chunkPayload(t, puts[1].body)
	if last.Chunk != -1 {
		t.Errorf("last chunk = %d, want -1", last.Chunk)
	}
	b, err := base64.StdEncoding.DecodeString(last.Content)
	if err != nil || string(b) != "4567" {
		t.Errorf("last chunk content = %q, %v; want 4567", b, err)
	}
}

// TestWriteEmptyFileIsOneChunk ports the empty-file branch of upstream
// test_upload_that_fits_is_one_request: never send a lone chunk:-1.
func TestWriteEmptyFileIsOneChunk(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := smallBudgetClient(t, testConfig(srv.URL), "content/blob.bin", 9)
	if err := c.Write(context.Background(), "content/blob.bin", nil, 4); err != nil {
		t.Fatalf("Write: %v", err)
	}
	puts := 0
	for _, req := range srv.requests() {
		if req.method == http.MethodPut {
			puts++
			if got := chunkPayload(t, req.body).Chunk; got != 1 {
				t.Errorf("chunk = %d, want 1", got)
			}
		}
	}
	if puts != 1 {
		t.Fatalf("PUTs = %d, want 1", puts)
	}
}

// TestWriteChunkedTruncationIsTyped ports the safety half of audit S-2: when a
// chunked upload fails after data was sent, the caller gets a typed
// ErrIncompleteUpload carrying how many chunks made it to the server, and the
// underlying transport error (whose text can carry a tokenised URL) is never
// chained into it.
func TestWriteChunkedTruncationIsTyped(t *testing.T) {
	data := []byte("0123456789")
	failedChunk := 2
	var seen []int
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		p := chunkPayload(t, mustReadAll(r))
		seen = append(seen, p.Chunk)
		if p.Chunk == failedChunk {
			// Like requests, the body of this error would carry the token URL.
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"message": "500 Error for url: https://x/api/contents/blob?colab-runtime-proxy-token=" + testToken,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := smallBudgetClient(t, testConfig(srv.URL), "content/blob.bin", 9)

	err := c.Write(context.Background(), "content/blob.bin", data, 4)
	var incomplete *IncompleteUploadError
	if !errors.As(err, &incomplete) {
		t.Fatalf("err = %v (%T), want *IncompleteUploadError", err, err)
	}
	if !errors.Is(err, ErrIncompleteUpload) {
		t.Fatal("errors.Is(err, ErrIncompleteUpload) failed")
	}
	if incomplete.ChunksSent != failedChunk-1 {
		t.Errorf("ChunksSent = %d, want %d", incomplete.ChunksSent, failedChunk-1)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "colab-runtime-proxy-token") {
		t.Errorf("error leaked the token: %q", err.Error())
	}
	if len(seen) != failedChunk {
		t.Errorf("server saw chunks %v, want failure on chunk %d", seen, failedChunk)
	}
}

// TestWriteChunkedRefreshesTokenPerChunk ports upstream
// tests/test_contents.py::test_later_chunks_use_a_refreshed_token (PR #272):
// a long upload outlives the proxy token, so the optional callback is called
// before every chunk after the first and its token is used on the wire.
func TestWriteChunkedRefreshesTokenPerChunk(t *testing.T) {
	data := []byte("0123456789")
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	cfg := testConfig(srv.URL)
	refreshCalls := 0
	cfg.RefreshToken = func(context.Context) (RefreshInfo, error) {
		refreshCalls++
		return RefreshInfo{Token: logx.NewSecret("renewed-token")}, nil
	}
	c := smallBudgetClient(t, cfg, "content/blob.bin", 9)

	if err := c.Write(context.Background(), "content/blob.bin", data, 4); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if refreshCalls != 2 { // 3 chunks (4,4,2) -> refreshed before chunks 2 and 3
		t.Errorf("refresh calls = %d, want 2", refreshCalls)
	}
	var tokens []string
	for _, req := range srv.requests() {
		if req.method != http.MethodPut {
			continue // the trailing verification GET is not a chunk
		}
		tokens = append(tokens, req.query.Get("colab-runtime-proxy-token"))
	}
	want := []string{testToken, "renewed-token", "renewed-token"}
	if fmt.Sprint(tokens) != fmt.Sprint(want) {
		t.Errorf("tokens = %v, want %v", tokens, want)
	}
}

// TestWriteChunkedFailedRefreshIsTyped ports upstream
// tests/test_contents.py::test_failed_token_refresh_reports_possibly_incomplete_file.
func TestWriteChunkedFailedRefreshIsTyped(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	cfg := testConfig(srv.URL)
	cfg.RefreshToken = func(context.Context) (RefreshInfo, error) {
		return RefreshInfo{}, errors.New("credential refresh denied")
	}
	c := smallBudgetClient(t, cfg, "content/blob.bin", 9)

	err := c.Write(context.Background(), "content/blob.bin", []byte("0123456789"), 4)
	var incomplete *IncompleteUploadError
	if !errors.As(err, &incomplete) || incomplete.ChunksSent != 1 {
		t.Fatalf("err = %v (%T), want incomplete after 1 chunk", err, err)
	}
	if !strings.Contains(err.Error(), "credential refresh denied") {
		t.Errorf("detail missing from error: %q", err.Error())
	}
}

// TestWriteChunkedVerifiesReportedSize covers the S-2 remediation "verify the
// final remote size when the server reports one": a server that acknowledges
// every chunk but stores a truncated file is detected, not silently accepted.
func TestWriteChunkedVerifiesReportedSize(t *testing.T) {
	remoteSize := int64(4) // server claims only the first chunk was stored
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{
				"name": "blob.bin", "type": "file", "size": remoteSize,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := smallBudgetClient(t, testConfig(srv.URL), "content/blob.bin", 9)

	err := c.Write(context.Background(), "content/blob.bin", []byte("0123456789"), 4)
	var incomplete *IncompleteUploadError
	if !errors.As(err, &incomplete) {
		t.Fatalf("err = %v (%T), want *IncompleteUploadError on size mismatch", err, err)
	}
	if incomplete.ChunksSent != 3 {
		t.Errorf("ChunksSent = %d, want 3", incomplete.ChunksSent)
	}
	if !strings.Contains(err.Error(), "size") {
		t.Errorf("error should explain the size mismatch: %q", err.Error())
	}
}

// TestWriteChunkedNoSizeIsOptimistic documents the boundary: a server that
// reports no size cannot be verified from metadata, so a fully acknowledged
// chunked upload succeeds (the live GET/SHA-256 probe in AUDIT-02 §2 item 4
// remains HUMAN REQUIRED).
func TestWriteChunkedNoSizeIsOptimistic(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"name": "blob.bin", "type": "file"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := smallBudgetClient(t, testConfig(srv.URL), "content/blob.bin", 9)
	if err := c.Write(context.Background(), "content/blob.bin", []byte("0123456789"), 4); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

// TestWriteRejectsChunkTooLarge ports upstream
// tests/test_contents.py::test_upload_rejects_bad_chunk_size.
func TestWriteRejectsChunkTooLarge(t *testing.T) {
	c := smallBudgetClient(t, testConfig("https://example.invalid"), "content/blob.bin", 9)
	if err := c.Write(context.Background(), "content/blob.bin", []byte("x"), 7); err == nil {
		t.Fatal("a chunk that cannot fit in one request must be rejected")
	}
}

func mustReadAll(r *http.Request) []byte {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		panic(err)
	}
	return b
}

// nonSeekableReader intentionally exposes only io.Reader so sourceSize() cannot
// learn the source length and WriteStream takes the prefix/streaming path.
type nonSeekableReader struct{ io.Reader }

// TestWriteChunkedVerifyGETFailureIsTyped covers audit §3.14: a failure of the
// post-upload verification GET must not be reported as upload success. The body
// carries the token to prove the typed detail stays redacted.
func TestWriteChunkedVerifyGETFailureIsTyped(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"message": "verify boom " + testToken,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := smallBudgetClient(t, testConfig(srv.URL), "content/blob.bin", 9)

	err := c.Write(context.Background(), "content/blob.bin", []byte("0123456789"), 4)
	var incomplete *IncompleteUploadError
	if !errors.As(err, &incomplete) {
		t.Fatalf("err = %v (%T), want *IncompleteUploadError when verification GET fails", err, err)
	}
	if !errors.Is(err, ErrIncompleteUpload) {
		t.Fatal("errors.Is(err, ErrIncompleteUpload) failed")
	}
	if !strings.HasPrefix(incomplete.Detail, "verify: ") {
		t.Errorf("Detail = %q, want verify: prefix", incomplete.Detail)
	}
	if incomplete.ChunksSent != 3 {
		t.Errorf("ChunksSent = %d, want 3", incomplete.ChunksSent)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error leaked the token: %q", err.Error())
	}
}

// TestWriteChunkedVerifyMismatchDetail covers audit §3.14's mismatch branch:
// a server-reported size that disagrees with the local size is a typed
// incomplete upload whose detail names both sizes.
func TestWriteChunkedVerifyMismatchDetail(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{
				"name": "blob.bin", "type": "file", "size": 12,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := smallBudgetClient(t, testConfig(srv.URL), "content/blob.bin", 9)

	err := c.Write(context.Background(), "content/blob.bin", []byte("0123456789"), 4)
	var incomplete *IncompleteUploadError
	if !errors.As(err, &incomplete) {
		t.Fatalf("err = %v (%T), want *IncompleteUploadError on size mismatch", err, err)
	}
	if !strings.HasPrefix(incomplete.Detail, "verify: ") ||
		!strings.Contains(incomplete.Detail, "remote size 12") ||
		!strings.Contains(incomplete.Detail, "local size 10") {
		t.Errorf("Detail = %q, want verify: ... remote size 12 ... local size 10", incomplete.Detail)
	}
}

// TestWriteChunkedVerifyMatchingSizeSucceeds guards the happy path: a reported
// size equal to the local length must not be reported as incomplete.
func TestWriteChunkedVerifyMatchingSizeSucceeds(t *testing.T) {
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{
				"name": "blob.bin", "type": "file", "size": 10,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := smallBudgetClient(t, testConfig(srv.URL), "content/blob.bin", 9)
	if err := c.Write(context.Background(), "content/blob.bin", []byte("0123456789"), 4); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

// TestWriteStreamNonSeekableVerifiesSentBytes covers audit §3.15: for a source
// of unknown length (size = -1) verification must still run, comparing the
// server-reported size to the number of bytes actually PUT. A stream that was
// truncated remotely must fail loudly instead of silently succeeding.
func TestWriteStreamNonSeekableVerifiesSentBytes(t *testing.T) {
	payload := bytes.Repeat([]byte("s"), 50)
	srv := newFakeContents(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{
				"name": "blob.bin", "type": "file", "size": 10, // only 10 of 50 bytes stored
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "file"})
	})
	c := smallBudgetClient(t, testConfig(srv.URL), "content/blob.bin", 40)

	err := c.WriteStream(context.Background(), "content/blob.bin",
		&nonSeekableReader{bytes.NewReader(payload)}, 20)
	var incomplete *IncompleteUploadError
	if !errors.As(err, &incomplete) {
		t.Fatalf("err = %v (%T), want *IncompleteUploadError when non-seekable upload size mismatches", err, err)
	}
	if !strings.HasPrefix(incomplete.Detail, "verify: ") ||
		!strings.Contains(incomplete.Detail, "remote size 10") ||
		!strings.Contains(incomplete.Detail, "local size 50") {
		t.Errorf("Detail = %q, want verify mismatch against 50 sent bytes", incomplete.Detail)
	}
	if incomplete.ChunksSent != 3 {
		t.Errorf("ChunksSent = %d, want 3", incomplete.ChunksSent)
	}
	gets := 0
	for _, req := range srv.requests() {
		if req.method == http.MethodGet {
			gets++
		}
	}
	if gets != 1 {
		t.Errorf("verification GETs = %d, want 1", gets)
	}
}

// TestStoredErrorDetailNeverCarriesCause guards the token-safety contract of
// IncompleteUploadError: only a redacted string is retained, not the error
// that may embed the proxy-token URL.
func TestStoredErrorDetailNeverCarriesCause(t *testing.T) {
	// Build the detail exactly as the production failure path does: a raw
	// token-bearing transport error must never be copied into the message.
	boom := &TransportError{Op: "write", Err: errors.New("boom " + testToken)}
	e := &IncompleteUploadError{
		RemotePath: "content/blob.bin", ChunksSent: 2,
		Detail: describeUploadError(boom),
	}
	if strings.Contains(e.Error(), testToken) {
		t.Errorf("error leaked token: %q", e.Error())
	}
	if e.Unwrap() != ErrIncompleteUpload {
		t.Error("Unwrap must return only the sentinel")
	}
	// Ensure the JSON envelope helper is stable for the documented edge case.
	if uploadEnvelopeSize("content/big.bin", 1) <= 0 {
		t.Error("empty envelope must have a positive size")
	}
	var m Model
	if err := json.Unmarshal([]byte(`{"name":"f","size":42,"content":"aGk=","format":"base64"}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Size == nil || *m.Size != 42 || !m.ContentPresent() {
		t.Fatalf("size/content presence not decoded: %+v", m)
	}
}
