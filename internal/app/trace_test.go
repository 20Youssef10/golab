package app

// The composition root is where a control-plane OAuth client is built. This
// test proves GOLAB_TRACE/context opt-in actually wraps that client rather
// than merely creating an unused recorder.

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/auth"
	"github.com/20Youssef10/golab/internal/trace"
)

type traceAuth struct{ client *http.Client }

func (a traceAuth) HTTPClient(context.Context, []string) (*http.Client, error) { return a.client, nil }

type jsonTransport struct{}

func (jsonTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Simulate the OAuth transport adding a bearer header below the tracer.
	req.Header.Set("Authorization", "Bearer CANARY_APP_TOKEN")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"assignments":[]}`)),
		Request:    req,
	}, nil
}

func TestAppNewInstallsTraceAndWrapsControlPlane(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLAB_CONFIG_DIR", dir)
	t.Setenv("COLAB_SESSIONS_FILE", "")
	path := filepath.Join(dir, "wire.ndjson")

	ctx := trace.WithConfig(context.Background(), trace.Config{Path: path})
	d, err := New(ctx, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Trace == nil || !d.Trace.Enabled() {
		t.Fatal("composition root did not install the trace recorder")
	}
	d.Auth = traceAuth{client: &http.Client{Transport: jsonTransport{}}}
	client, err := d.ColabClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "/tun/m/assignments") {
		t.Fatalf("control-plane request was not traced: %s", s)
	}
	if strings.Contains(s, "CANARY_APP_TOKEN") {
		t.Fatalf("bearer token leaked into trace: %s", s)
	}
	var _ auth.Provider = traceAuth{}
}
