package colabapi

// Regression tests for upstream issue #270: body-less POSTs behind a forward
// proxy get HTTP 411 when Go sends an explicit "Content-Length: 0". The fix
// must be scoped to POST with a nil body: GET and requests with a body keep
// their normal content length.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/logx"
)

type wireSnapshot struct {
	method           string
	contentLength    int64
	transferEncoding []string
	accept           string
	agent            string
	xsrf             string
	contentType      string
}

func snapshotRequest(r *http.Request) wireSnapshot {
	return wireSnapshot{
		method:           r.Method,
		contentLength:    r.ContentLength,
		transferEncoding: append([]string(nil), r.TransferEncoding...),
		accept:           r.Header.Get("Accept"),
		agent:            r.Header.Get("X-Colab-Client-Agent"),
		xsrf:             r.Header.Get("X-Goog-Colab-Token"),
		contentType:      r.Header.Get("Content-Type"),
	}
}

func testWireClient(t *testing.T, srv *httptest.Server) *client {
	t.Helper()
	c, err := New(Config{Domain: srv.URL, HTTPClient: srv.Client(), ClientAgent: "wire-test"})
	if err != nil {
		t.Fatal(err)
	}
	impl, ok := c.(*client)
	if !ok {
		t.Fatalf("unexpected client type %T", c)
	}
	return impl
}

func TestBodyLessPostUsesChunkedTransfer(t *testing.T) {
	var got wireSnapshot
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = snapshotRequest(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"endpoint":"ep-1"}`))
	}))
	defer srv.Close()

	c := testWireClient(t, srv)
	_, err := c.postAssignment(context.Background(), AssignRequest{}, logx.NewSecret("xsrf-wire-test"))
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	// A chunked body is visible to a net/http server as ContentLength == -1
	// and TransferEncoding == ["chunked"]; there must be no Content-Length: 0.
	if got.contentLength != -1 {
		t.Errorf("ContentLength = %d, want -1 (chunked), i.e. no Content-Length: 0", got.contentLength)
	}
	if len(got.transferEncoding) != 1 || got.transferEncoding[0] != "chunked" {
		t.Errorf("TransferEncoding = %q, want [chunked]", got.transferEncoding)
	}
	// The wire fix must not disturb headers used by the handshake.
	if got.xsrf != "xsrf-wire-test" {
		t.Errorf("X-Goog-Colab-Token = %q, want unchanged XSRF token", got.xsrf)
	}
	if got.accept != "application/json" || got.agent != "wire-test" {
		t.Errorf("headers changed: Accept=%q Agent=%q", got.accept, got.agent)
	}
}

func TestGetAndBodyRequestsKeepContentLength(t *testing.T) {
	var gets, puts []wireSnapshot
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := snapshotRequest(r)
		switch r.Method {
		case http.MethodGet:
			gets = append(gets, snap)
			_, _ = w.Write([]byte(`{"token":"xsrf-wire-test"}`))
		case http.MethodPut:
			puts = append(puts, snap)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	c := testWireClient(t, srv)
	pending, _, err := c.getAssignment(context.Background(), AssignRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.token.Reveal() != "xsrf-wire-test" {
		t.Fatalf("unexpected pending assignment: %+v", pending)
	}
	body := `{"payload":"kept"}`
	if err := c.doRequest(context.Background(), http.MethodPut, c.tunURL("/tun/m/probe", nil), requestOptions{
		body: strings.NewReader(body),
	}); err != nil {
		t.Fatal(err)
	}

	if len(gets) != 1 || gets[0].contentLength != 0 || len(gets[0].transferEncoding) != 0 {
		t.Errorf("GET wire shape changed: %+v", gets)
	}
	if len(puts) != 1 {
		t.Fatalf("PUT requests = %d, want 1", len(puts))
	}
	if puts[0].contentLength != int64(len(body)) {
		t.Errorf("PUT ContentLength = %d, want %d", puts[0].contentLength, len(body))
	}
	if len(puts[0].transferEncoding) != 0 {
		t.Errorf("PUT TransferEncoding = %q, want none", puts[0].transferEncoding)
	}
}
