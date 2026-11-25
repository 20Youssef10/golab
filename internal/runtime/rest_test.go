package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/20Youssef10/golab/internal/logx"
)

// staticTransport returns a fixed HTTP response and records the escaped path
// and raw query of each request, so tests can assert on the exact wire format
// the restClient produced.
type staticTransport struct {
	status  int
	body    []byte
	paths   []string
	queries []string
}

func (t *staticTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.paths = append(t.paths, req.URL.EscapedPath())
	t.queries = append(t.queries, req.URL.RawQuery)
	return &http.Response{
		StatusCode: t.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(t.body)),
	}, nil
}

func newTestREST(tr http.RoundTripper) *restClient {
	return newREST(
		Target{URL: "http://fake", Token: logx.NewSecret("tok")},
		Config{HTTPClient: &http.Client{Transport: tr}},
	)
}

func TestRESTEscapesKernelIDInPaths(t *testing.T) {
	// Upstream: google-colab-cli's runtime REST client URL-encodes kernel ids
	// so a tampered sessions.json cannot inject a path separator or a query.
	const id = "abc/def?ghi"
	const escapedID = "abc%2Fdef%3Fghi"
	tr := &staticTransport{status: http.StatusOK, body: []byte(`{}`)}
	c := newTestREST(tr)

	if _, err := c.inspectKernel(context.Background(), id); err != nil {
		t.Fatalf("inspectKernel: %v", err)
	}
	if err := c.lifecycle(context.Background(), "restart", id); err != nil {
		t.Fatalf("lifecycle restart: %v", err)
	}
	if err := c.lifecycle(context.Background(), "shutdown", id); err != nil {
		t.Fatalf("lifecycle shutdown: %v", err)
	}

	wantPaths := []string{
		"/api/kernels/" + escapedID,
		"/api/kernels/" + escapedID + "/restart",
		"/api/kernels/" + escapedID,
	}
	if len(tr.paths) != len(wantPaths) {
		t.Fatalf("got %d requests, want %d: %q", len(tr.paths), len(wantPaths), tr.paths)
	}
	for i, want := range wantPaths {
		if tr.paths[i] != want {
			t.Errorf("request %d escaped path = %q, want %q", i, tr.paths[i], want)
		}
		if tr.queries[i] != "" {
			t.Errorf("request %d raw query = %q, want empty (injected '?' must not become a query)", i, tr.queries[i])
		}
	}
}

func TestRESTRejectsOversizedResponse(t *testing.T) {
	// Upstream: google-colab-cli rejects a REST response larger than the frame
	// limit instead of silently decoding a truncated (but valid-looking) body.
	const prefix = `{"id":"kernel-leaked"}`
	body := make([]byte, maxFrameSize+1)
	copy(body, prefix)
	for i := len(prefix); i < len(body); i++ {
		body[i] = ' '
	}
	tr := &staticTransport{status: http.StatusOK, body: body}
	c := newTestREST(tr)

	id, err := c.createKernel(context.Background())
	if err == nil {
		t.Fatalf("createKernel succeeded with a truncated body; got id %q", id)
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("createKernel error = %v, want errors.Is(_, ErrResponseTooLarge)", err)
	}
	if id != "" {
		t.Fatalf("createKernel returned partial id %q from an oversized response", id)
	}
}
