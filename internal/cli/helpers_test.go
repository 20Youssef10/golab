package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/history"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

// fakeAuth implements auth.Provider without performing any credential work.
// The fake control-plane server accepts unauthenticated requests.
type fakeAuth struct{ client *http.Client }

func (f fakeAuth) HTTPClient(context.Context, []string) (*http.Client, error) {
	return f.client, nil
}

// rewriteTransport redirects the runtime proxy's synthetic URL (issued by the
// fake control plane) to the local fake kernel, preserving path and query.
type rewriteTransport struct {
	base http.RoundTripper
	to   *url.URL
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.to.Scheme
	clone.URL.Host = t.to.Host
	clone.Host = t.to.Host
	return t.base.RoundTrip(clone)
}

func runtimeClientFor(t *testing.T, ksrv *fakekernel.Server, ctrl *fakecolab.Server) *http.Client {
	t.Helper()
	base := ksrv.Client()
	return &http.Client{
		Transport: rewriteTransport{base: base.Transport, to: mustURL(t, ksrv.URL())},
		Timeout:   10 * time.Second,
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// testHarness bundles the fakes used by the CLI golden tests.
type testHarness struct {
	deps     *app.Deps
	ctrl     *fakecolab.Server
	kernel   *fakekernel.Server
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	dir      string
	client   colabapi.Client
	contents *fakeContents
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	ctrl := fakecolab.New()
	t.Cleanup(ctrl.Close)
	kernel := fakekernel.New(t)
	dir := t.TempDir()
	store, err := state.NewStore(filepath.Join(dir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := state.NewSettingsStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	colab, err := colabapi.New(colabapi.Config{Domain: ctrl.TunURL(), HTTPClient: ctrl.Client()})
	if err != nil {
		t.Fatal(err)
	}
	usePublic := false
	d := &app.Deps{
		Config: app.Config{
			ColabDomain:        ctrl.TunURL(),
			PublicAPI:          ctrl.PublicURL(),
			UsePublicAPI:       &usePublic,
			RuntimeHTTPClient:  runtimeClientFor(t, kernel, ctrl),
			ContentsHTTPClient: kernel.Client(),
		},
		Store:         store,
		SettingsStore: settings,
		Auth:          fakeAuth{client: ctrl.Client()},
		HistoryDir:    dir,
		Log:           logx.Logger(),
	}
	rt, err := runtime.New(runtime.Config{HTTPClient: d.Config.RuntimeHTTPClient})
	if err != nil {
		t.Fatal(err)
	}
	d.Runtime = rt
	return &testHarness{
		deps: d, ctrl: ctrl, kernel: kernel, dir: dir, client: colab,
	}
}

// attachContents points the fake Contents API at the harness and returns it.
func (h *testHarness) attachContents(t *testing.T, fc *fakeContents) {
	t.Helper()
	h.contents = fc
	h.deps.Config.ContentsHTTPClient = fc.Client()
	// Point every stored session's data-plane URL at the fake Contents API.
	sessions, err := h.deps.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range sessions {
		s.URL = fc.URL
		if err := h.deps.Store.Put(s); err != nil {
			t.Fatal(err)
		}
		_ = name
	}
}

// run executes args against the harness and returns the exit code. Program
// output and diagnostics are captured in h.stdout and h.stderr.
func (h *testHarness) run(t *testing.T, stdin string, args ...string) int {
	t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	ctx := WithDeps(context.Background(), h.deps)
	return execute(ctx, args, strings.NewReader(stdin), &h.stdout, &h.stderr)
}

// putSession stores a session whose data-plane URL points at the fake kernel.
func (h *testHarness) putSession(t *testing.T, name string) state.Session {
	t.Helper()
	expiry := time.Now().Add(time.Hour)
	s := state.Session{
		Name: name, Endpoint: "endpoint-test", URL: h.kernel.URL(),
		Token: logx.NewSecret(h.kernel.Token()), TokenExpiresAt: &expiry,
		Variant: "DEFAULT", Accelerator: "NONE", MachineShape: "STANDARD",
	}
	if err := h.deps.Store.Put(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// assignAndStore allocates on the fake control plane and persists the result,
// returning the session with its data-plane URL rewritten to the fake kernel.
func (h *testHarness) assignAndStore(t *testing.T, name string, req colabapi.AssignRequest) state.Session {
	t.Helper()
	res, err := h.client.Assign(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	expiry := res.RuntimeProxyInfo.ExpiresAt(time.Now())
	acc := string(res.Accelerator)
	if acc == "" {
		acc = string(req.Accelerator)
	}
	s := state.Session{
		Name: name, Endpoint: res.Endpoint, URL: h.kernel.URL(),
		Token: res.RuntimeProxyInfo.Token, TokenExpiresAt: &expiry,
		Variant: string(req.Variant), Accelerator: acc, MachineShape: shapeLabel(req.Shape),
	}
	if err := h.deps.Store.Put(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// addHistory appends raw events to a session's JSONL log for log-command tests.
func (h *testHarness) addHistory(t *testing.T, name string, events ...history.Event) {
	t.Helper()
	l, err := history.NewAt(h.dir, name)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if err := l.Append(e.EventType, json.RawMessage(e.Payload)); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeContents is a minimal Jupyter Contents API served over TLS.
type fakeContents struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	dirs  map[string][]string
	puts  []string
}

type contentsPayload struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Format  string `json:"format"`
	Content string `json:"content"`
	Chunk   int    `json:"chunk"`
}

func newFakeContents(t *testing.T) *fakeContents {
	t.Helper()
	fc := &fakeContents{files: map[string][]byte{}, dirs: map[string][]string{}}
	fc.Server = httptest.NewTLSServer(http.HandlerFunc(fc.serve))
	t.Cleanup(fc.Server.Close)
	return fc
}

func (fc *fakeContents) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/contents/"), "/")
	fc.mu.Lock()
	defer fc.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		if children, ok := fc.dirs[p]; ok || p == "" {
			entries := make([]map[string]any, 0, len(children))
			for _, c := range children {
				kind := "file"
				if strings.HasSuffix(c, "/") {
					kind = "directory"
					c = strings.TrimSuffix(c, "/")
				}
				entries = append(entries, map[string]any{"name": c, "type": kind})
			}
			writeJSONResp(w, map[string]any{"name": p, "path": p, "type": "directory", "content": entries})
			return
		}
		data, ok := fc.files[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSONResp(w, map[string]any{
			"name": filepath.Base(p), "path": p, "type": "file",
			"format": "text", "content": string(data),
		})
	case http.MethodPut:
		var body contentsPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		fc.puts = append(fc.puts, p)
		if body.Chunk >= 1 {
			data, err := base64.StdEncoding.DecodeString(body.Content)
			if err != nil {
				http.Error(w, "bad base64", http.StatusBadRequest)
				return
			}
			fc.files[p] = data
		}
		writeJSONResp(w, map[string]any{"path": p, "type": "file"})
	case http.MethodDelete:
		if _, ok := fc.files[p]; !ok {
			http.NotFound(w, r)
			return
		}
		delete(fc.files, p)
		writeJSONResp(w, map[string]any{"path": p})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (fc *fakeContents) addFile(name, content string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.files[name] = []byte(content)
}

func (fc *fakeContents) file(name string) string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return string(fc.files[name])
}

func (fc *fakeContents) hasFile(name string) bool {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	_, ok := fc.files[name]
	return ok
}

// writeEditorScript creates an executable shell script that appends the given
// text to the file passed as its first argument. Setting EDITOR to this script
// makes `edit` tests deterministic and independent of a real editor.
func writeEditorScript(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "editor.sh")
	script := "#!/bin/sh\nprintf '%s' '" + text + "' >> \"$1\"\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}
