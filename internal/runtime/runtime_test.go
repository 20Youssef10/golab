package runtime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

func newTestRuntime(t *testing.T, srv *fakekernel.Server) Runtime {
	t.Helper()
	rt, err := New(Config{
		HTTPClient:     srv.Client(),
		RequestTimeout: 50 * time.Millisecond,
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
	rt := newTestRuntime(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	k, err := rt.Attach(ctx, target(srv, srv.KernelID()), AttachOptions{})
	if err != nil {
		t.Fatalf("Attach after retries: %v", err)
	}
	defer k.Close()
	if time.Since(start) > 2*time.Second {
		t.Fatal("retries took too long; test backoff was not injected")
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
