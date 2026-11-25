package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

// The old 50 ms request timeout was shorter than a TLS handshake under the
// race detector on a loaded runner, making unrelated tests flaky. Keep it only
// for the test that intentionally hangs requests to exercise retry logic.
func newTestRuntime(t *testing.T, srv *fakekernel.Server) Runtime {
	t.Helper()
	return newTestRuntimeWithTimeout(t, srv, 2*time.Second)
}

func newTestRuntimeWithTimeout(t *testing.T, srv *fakekernel.Server, timeout time.Duration) Runtime {
	t.Helper()
	rt, err := New(Config{
		HTTPClient:     srv.Client(),
		RequestTimeout: timeout,
		RetryBackoff:   func(int) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return rt
}

func target(srv *fakekernel.Server, kernelID string) Target {
	return Target{
		URL: srv.URL(), Token: logx.NewSecret(srv.Token()),
		Endpoint: "ep-test", KernelID: kernelID,
	}
}

func TestAttachCreatesKernelAndCapturesIDs(t *testing.T) {
	// Ports test_colab_runtime_kernel_client: a fresh target must create a
	// kernel, capture the effective kernel id and the generated session id.
	srv := fakekernel.New(t)
	rt := newTestRuntime(t, srv)
	var kernelID, sessionID string
	k, err := rt.Attach(context.Background(), target(srv, ""), AttachOptions{
		OnKernelStarted:  func(id string) { kernelID = id },
		OnSessionStarted: func(id string) { sessionID = id },
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer k.Close()
	if kernelID != srv.KernelID() || kernelID == "" {
		t.Fatalf("OnKernelStarted got %q, want %q", kernelID, srv.KernelID())
	}
	if sessionID == "" || k.SessionID() != sessionID || k.ID() != kernelID {
		t.Fatalf("session id not captured: session=%q kernel=%q", sessionID, k.ID())
	}
	for _, r := range srv.Requests() {
		if r.Method == http.MethodGet && strings.HasPrefix(r.Path, "/api/kernels/") && !strings.HasSuffix(r.Path, "/channels") {
			t.Fatalf("fresh attach must not GET a kernel: %s %s", r.Method, r.Path)
		}
	}
}

func TestAttachReusesLiveKernel(t *testing.T) {
	srv := fakekernel.New(t)
	rt := newTestRuntime(t, srv)
	k, err := rt.Attach(context.Background(), target(srv, srv.KernelID()), AttachOptions{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer k.Close()
	if k.ID() != srv.KernelID() {
		t.Fatalf("kernel id %q, want %q", k.ID(), srv.KernelID())
	}
	gets := 0
	for _, r := range srv.Requests() {
		if r.Method == http.MethodGet && !strings.HasSuffix(r.Path, "/channels") {
			gets++
		}
	}
	if gets != 1 {
		t.Fatalf("want exactly one GET /api/kernels/{id}, got %d", gets)
	}
}

func TestAttachStaleKernelIDAlwaysPersistsEffectiveID(t *testing.T) {
	// Ports doc 02 §3.2 / ADR-009.4: upstream only saves a replacement kernel
	// id when self.kernel_id was empty. Go must always report the effective id.
	srv := fakekernel.New(t)
	oldID := srv.KernelID()
	srv.SetAlive(false) // cached kernel is gone: GET returns 404
	rt := newTestRuntime(t, srv)
	var started []string
	k, err := rt.Attach(context.Background(), target(srv, "stale-kernel-id"), AttachOptions{
		OnKernelStarted: func(id string) { started = append(started, id) },
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer k.Close()
	if len(started) != 1 || started[0] != srv.KernelID() || started[0] == oldID {
		t.Fatalf("effective kernel id not persisted: started=%v effective=%q", started, srv.KernelID())
	}
	if k.ID() != srv.KernelID() {
		t.Fatalf("Kernel.ID() = %q, want %q", k.ID(), srv.KernelID())
	}
}

func TestAttachRetriesOnlyTimeouts(t *testing.T) {
	srv := fakekernel.New(t)
	srv.HangFirstREST(2)
	// A localhost TLS handshake plus request-cancellation propagation can take
	// far longer than 250 ms on a loaded CI runner. If the per-attempt deadline
	// fires before the handler is reached, timeoutFirst never decrements and
	// all three attempts time out ("inspect kernel: ... deadline exceeded").
	// Give each attempt a multi-second budget and the whole Attach a generous
	// deadline so the retry sequence is the only thing under test; the original
	// comment about the 50 ms timeout applies with even more force here.
	rt := newTestRuntimeWithTimeout(t, srv, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	k, err := rt.Attach(ctx, target(srv, srv.KernelID()), AttachOptions{})
	if err != nil {
		t.Fatalf("Attach after retries: %v", err)
	}
	defer k.Close()
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("retries took too long (%v); test backoff was not injected", elapsed)
	}
	// The first two kernel GETs were hung until their deadline; only the third
	// succeeds. Counting them proves the timeouts were actually retried.
	gets := 0
	for _, r := range srv.Requests() {
		if r.Method == http.MethodGet &&
			strings.HasPrefix(r.Path, "/api/kernels/") &&
			!strings.HasSuffix(r.Path, "/channels") {
			gets++
		}
	}
	if gets != 3 {
		t.Fatalf("want 3 kernel GETs (2 timed out + 1 success), got %d", gets)
	}
}

func TestAttachDoesNotRetryPermanentErrors(t *testing.T) {
	srv := fakekernel.New(t)
	srv.FailCreates(3)
	rt := newTestRuntime(t, srv)
	_, err := rt.Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err == nil {
		t.Fatal("expected create to fail")
	}
	posts := 0
	for _, r := range srv.Requests() {
		if r.Method == http.MethodPost && r.Path == "/api/kernels" {
			posts++
		}
	}
	if posts != 1 {
		t.Fatalf("permanent error was retried %d times", posts)
	}
}

func TestAttachUnauthorizedIsSessionLost(t *testing.T) {
	srv := fakekernel.New(t)
	srv.SetUnauthorized(true)
	rt := newTestRuntime(t, srv)
	_, err := rt.Attach(context.Background(), target(srv, srv.KernelID()), AttachOptions{})
	if !errors.Is(err, ErrSessionLost) {
		t.Fatalf("want ErrSessionLost, got %v", err)
	}
	var sl *SessionLostError
	if !errors.As(err, &sl) || sl.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want typed SessionLostError with 401, got %#v", err)
	}
}

func TestSecretsNeverLeakIntoErrors(t *testing.T) {
	// Ports doc 07 §8 / AGENTS.md "no secret in error messages": a token must
	// never appear in an error, even when it is embedded in the failing URL.
	srv := fakekernel.New(t)
	sec := logx.NewSecret(srv.Token())
	if sec.String() != "REDACTED" || strings.Contains(sec.String(), srv.Token()) {
		t.Fatalf("Secret.String leaked the token: %q", sec.String())
	}
	srv.SetUnauthorized(true)
	rt := newTestRuntime(t, srv)
	_, err := rt.Attach(context.Background(), target(srv, srv.KernelID()), AttachOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), srv.Token()) {
		t.Fatalf("error leaked the proxy token: %q", err.Error())
	}
	if sec.Reveal() != srv.Token() {
		t.Fatal("Reveal must return the real token")
	}
}

func TestRESTPresentationHeaders(t *testing.T) {
	srv := fakekernel.New(t)
	rt := newTestRuntime(t, srv)
	k, err := rt.Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer k.Close()
	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	for _, r := range reqs {
		if r.Headers.Get("Authorization") != "Bearer "+srv.Token() ||
			r.Headers.Get("X-Colab-Runtime-Proxy-Token") != srv.Token() ||
			r.Headers.Get("X-Colab-Client-Agent") == "" ||
			r.Headers.Get("Content-Type") != "application/json" {
			t.Fatalf("wrong kernel REST headers on %s %s: %v", r.Method, r.Path, r.Headers)
		}
	}
}

func TestRestartInterruptShutdown(t *testing.T) {
	srv := fakekernel.New(t)
	rt := newTestRuntime(t, srv)
	k, err := rt.Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := k.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if err := k.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if err := k.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range srv.Requests() {
		if strings.HasSuffix(r.Path, "/restart") && r.Method == http.MethodPost {
			seen["restart"] = true
		}
		if strings.HasSuffix(r.Path, "/interrupt") && r.Method == http.MethodPost {
			seen["interrupt"] = true
		}
		if r.Method == http.MethodDelete {
			seen["shutdown"] = true
		}
	}
	for _, op := range []string{"restart", "interrupt", "shutdown"} {
		if !seen[op] {
			t.Fatalf("missing %s REST call", op)
		}
	}
}

// --- create/reconcile fake transport (defect §3.17) ------------------------

// fakeTimeoutError satisfies net.Error so isTimeout() classifies the create
// POST as retryable exactly as a real client-side deadline would.
type fakeTimeoutError struct{}

func (fakeTimeoutError) Error() string   { return "fake client timeout" }
func (fakeTimeoutError) Timeout() bool   { return true }
func (fakeTimeoutError) Temporary() bool { return true }

// reconcilingTransport implements only the two REST calls involved in create
// reconciliation: POST /api/kernels (create) and GET /api/kernels (list). It
// keeps its own server-side kernel store so a create that "times out" can
// still land, reproducing the orphan-runtime hazard of §3.17.
type reconcilingTransport struct {
	mu          sync.Mutex
	createCalls int
	listCalls   int
	kernels     map[string]bool
	// orphanFirst makes the first create land server-side but report a
	// client-side timeout. When false, the first create times out without
	// landing, so a genuine retry is still expected.
	orphanFirst bool
}

func (t *reconcilingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case req.Method == http.MethodPost && req.URL.Path == "/api/kernels":
		t.createCalls++
		if t.createCalls == 1 {
			if t.orphanFirst {
				t.kernels["kernel-orphan-1"] = true
			}
			// In both modes the client observes a timeout on the first create.
			return nil, fakeTimeoutError{}
		}
		id := fmt.Sprintf("kernel-created-%d", t.createCalls)
		t.kernels[id] = true
		return reconcileJSON(http.StatusCreated, map[string]any{"id": id})
	case req.Method == http.MethodGet && req.URL.Path == "/api/kernels":
		t.listCalls++
		models := make([]map[string]any, 0, len(t.kernels))
		for id := range t.kernels {
			models = append(models, map[string]any{"id": id, "name": "python3"})
		}
		return reconcileJSON(http.StatusOK, models)
	default:
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}
}

func reconcileJSON(status int, v any) (*http.Response, error) {
	b, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
	}, nil
}

func newReconcileRuntime(t *testing.T, tr http.RoundTripper) (*runtimeImpl, *restClient) {
	t.Helper()
	rt, err := New(Config{
		HTTPClient:   &http.Client{Transport: tr},
		RetryBackoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r := rt.(*runtimeImpl)
	return r, newREST(Target{URL: "http://fake", Token: logx.NewSecret("tok")}, r.cfg)
}

func TestResolveReconcilesTimedOutCreateBeforeRetry(t *testing.T) {
	// Regression for defect §3.17: a create that landed server-side but timed
	// out client-side must be reconciled via GET /api/kernels and reused, never
	// allocated a second (billable) runtime. Upstream has no idempotency key.
	tr := &reconcilingTransport{kernels: map[string]bool{}, orphanFirst: true}
	r, rest := newReconcileRuntime(t, tr)

	id, err := r.resolve(context.Background(), rest, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id != "kernel-orphan-1" {
		t.Fatalf("reused kernel id = %q, want %q", id, "kernel-orphan-1")
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.listCalls == 0 {
		t.Fatal("timed-out create was not reconciled: no GET /api/kernels")
	}
	if tr.createCalls != 1 {
		t.Fatalf("create POSTs = %d, want 1 (zero additional allocations)", tr.createCalls)
	}
	if len(tr.kernels) != 1 {
		t.Fatalf("server has %d kernels, want exactly 1 runtime", len(tr.kernels))
	}
}

func TestResolveStillRetriesCreateWhenReconcileFindsNothing(t *testing.T) {
	// Guardrail: reconciliation must not suppress a legitimate retry when the
	// timed-out create truly did not land server-side.
	tr := &reconcilingTransport{kernels: map[string]bool{}, orphanFirst: false}
	r, rest := newReconcileRuntime(t, tr)

	id, err := r.resolve(context.Background(), rest, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id != "kernel-created-2" {
		t.Fatalf("kernel id = %q, want %q from the retried create", id, "kernel-created-2")
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.createCalls != 2 {
		t.Fatalf("create POSTs = %d, want 2 (one genuine retry)", tr.createCalls)
	}
	if len(tr.kernels) != 1 {
		t.Fatalf("server has %d kernels, want exactly 1", len(tr.kernels))
	}
}
