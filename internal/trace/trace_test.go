package trace

// Ports the honesty requirement from docs/gap-report.md ("no redacted wire
// fixtures exist"). These tests are the ground truth for the recorder: a trace
// must be useful for fixture capture while never becoming a credential sink.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

const (
	canaryHeader = "CANARY_AUTHORIZATION_VALUE_9876543210"
	canaryQuery  = "CANARY_QUERY_VALUE_9876543210"
	canaryBody   = "CANARY_BODY_TOKEN_9876543210"
)

type stubTransport struct{ body string }

func (s stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Request:    req,
	}, nil
}

func readEvents(t *testing.T, path string) ([]Event, string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for i, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d is not NDJSON: %v (%s)", i+1, err, line)
		}
		out = append(out, e)
	}
	return out, string(raw)
}

// TestRecorderRedactionNeverEmitsCanaries is the core security regression:
// bearer headers, query values and token-shaped JSON values must never reach
// the trace file, even when --unsafe-bodies is opted into.
func TestRecorderRedactionNeverEmitsCanaries(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		path := t.TempDir() + "/wire.ndjson"
		r, err := New(Config{Path: path, UnsafeBodies: unsafe})
		if err != nil {
			t.Fatal(err)
		}
		body := `{"token":"` + canaryBody + `","assignments":[{"endpoint":"m-s-1"}],"count":1}`
		client := &http.Client{Transport: r.Wrap(stubTransport{body: body})}
		req, err := http.NewRequest(http.MethodGet,
			"https://colab.research.google.com/tun/m/assign?authuser=0&code="+canaryQuery, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+canaryHeader)
		req.Header.Set("Cookie", canaryHeader)
		req.Header.Set("X-Goog-Colab-Token", canaryHeader)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if string(got) != body {
			t.Fatalf("wrapper changed response body: %s", got)
		}

		events, raw := readEvents(t, path)
		if len(events) != 1 {
			t.Fatalf("events = %d, want 1: %s", len(events), raw)
		}
		e := events[0]
		if e.Kind != "http" || e.Method != http.MethodGet || e.Status != 200 {
			t.Fatalf("unexpected event: %+v", e)
		}
		if e.Path != "/tun/m/assign" {
			t.Fatalf("path = %q", e.Path)
		}
		if !contains(e.QueryKeys, "authuser") || !contains(e.QueryKeys, "code") {
			t.Fatalf("query keys = %v", e.QueryKeys)
		}
		if e.ContentType != "application/json" {
			t.Fatalf("content type = %q", e.ContentType)
		}
		if !strings.Contains(e.BodyShape, "assignments") || strings.Contains(e.BodyShape, canaryBody) {
			t.Fatalf("body shape = %q", e.BodyShape)
		}
		if e.BodySHA256 == "" {
			t.Fatal("body hash is missing")
		}
		for _, canary := range []string{canaryHeader, canaryQuery, canaryBody} {
			if strings.Contains(raw, canary) {
				t.Fatalf("unsafe=%t canary %q leaked into trace: %s", unsafe, canary, raw)
			}
		}
		if unsafe && e.UnsafeBody == "" {
			t.Fatal("unsafe bodies opted in but no body was recorded")
		}
		if !unsafe && e.UnsafeBody != "" {
			t.Fatal("unsafe body recorded without opt-in")
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("trace mode = %04o, want 0600", fi.Mode().Perm())
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestRecorderAppendsAndFrameSummary checks append-only semantics and that
// WebSocket frames are counted, not content-logged.
func TestRecorderAppendsAndFrameSummary(t *testing.T) {
	path := t.TempDir() + "/wire.ndjson"
	r, err := New(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	r.CountFrame("iopub", "in", "execute_result")
	r.CountFrame("iopub", "in", "stream")
	r.CountFrame("stdin", "out", "input_reply")
	if err := r.FlushWS(); err != nil {
		t.Fatal(err)
	}
	other, err := New(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Record(Event{Kind: "note", Note: "second process"}); err != nil {
		t.Fatal(err)
	}
	events, raw := readEvents(t, path)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2: %s", len(events), raw)
	}
	ws := events[0]
	if ws.Kind != "ws" || ws.Frames["iopub/in/execute_result"] != 1 ||
		ws.Frames["iopub/in/stream"] != 1 || ws.Frames["stdin/out/input_reply"] != 1 {
		t.Fatalf("frame summary = %+v", ws)
	}
	if ws.UnsafeBody != "" || strings.Contains(raw, "stream") == false {
		t.Fatalf("unexpected ws event: %+v / %s", ws, raw)
	}
	if events[1].Note != "second process" {
		t.Fatalf("append failed: %s", raw)
	}
}

// TestConfigFromEnv pins the GOLAB_TRACE opt-in and the unsafe label.
func TestConfigFromEnv(t *testing.T) {
	t.Setenv("GOLAB_TRACE", "")
	t.Setenv("GOLAB_TRACE_UNSAFE_BODIES", "")
	if c := ConfigFromEnv(); c.Path != "" || c.UnsafeBodies {
		t.Fatalf("empty env produced %+v", c)
	}
	t.Setenv("GOLAB_TRACE", "wire.ndjson")
	t.Setenv("GOLAB_TRACE_UNSAFE_BODIES", "1")
	if c := ConfigFromEnv(); c.Path != "wire.ndjson" || !c.UnsafeBodies {
		t.Fatalf("env not honoured: %+v", c)
	}
	ctx := WithConfig(context.Background(), Config{Path: "x.ndjson"})
	if c := FromContext(ctx); c.Path != "x.ndjson" {
		t.Fatalf("context config lost: %+v", c)
	}
}
