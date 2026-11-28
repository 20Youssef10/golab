package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
)

// attachTestToken is deliberately distinctive so a leak in stdout/stderr is
// unambiguous. It is the runtime-proxy token served by the fake Public API.
const attachTestToken = "SECRET-ATTACH-PROXY-TOKEN-DO-NOT-PRINT"

// webAttachRuntime mirrors a runtime created in the browser: the Public API
// lists it with connection info, but golab has no local session for it.
func webAttachRuntime() fakecolab.Runtime {
	return fakecolab.Runtime{
		Name:        "runtimes/r-web-1234",
		Endpoint:    "web-endpoint-1",
		URL:         "https://runtime-web.example",
		Token:       attachTestToken,
		ExpireTime:  time.Now().UTC().Add(time.Hour),
		Variant:     "VARIANT_GPU",
		Accelerator: "T4",
		Shape:       "SHAPE_HIGHMEM",
	}
}

func newPublicAttachHarness(t *testing.T, runtimes ...fakecolab.Runtime) *testHarness {
	t.Helper()
	h := newHarness(t)
	usePublic := true
	h.deps.Config.UsePublicAPI = &usePublic
	h.ctrl.SetRuntimes(runtimes)
	return h
}

// attach must be a local-state operation: it never calls POST /assign or POST
// /v1beta/runtimes, both of which allocate a billable VM.
func assertNoAllocationPosts(t *testing.T, h *testHarness) {
	t.Helper()
	for _, req := range h.ctrl.Requests() {
		if req.Method == http.MethodPost {
			t.Errorf("unexpected %s %s: attach must never allocate", req.Method, req.Path)
		}
	}
}

func assertAttachTokenAbsent(t *testing.T, outputs ...string) {
	t.Helper()
	for i, out := range outputs {
		if strings.Contains(out, attachTestToken) {
			t.Errorf("output %d leaked the runtime-proxy token", i)
		}
	}
}

// TestAttachCommandIsRegistered locks down the command surface: one
// registration point, --runtime/--session flags, and no --yes/budget guard on
// a non-billable operation.
func TestAttachCommandIsRegisteredWithRuntimeFlags(t *testing.T) {
	root := NewRootCommand(strings.NewReader(""), &strings.Builder{}, &strings.Builder{})
	cmd, _, err := root.Find([]string{"attach"})
	if err != nil || cmd == nil || cmd.Name() != "attach" {
		t.Fatalf("attach command not registered: cmd=%v err=%v", cmd, err)
	}
	for _, flag := range []string{"runtime", "session"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("attach is missing --%s", flag)
		}
	}
	if cmd.Flags().Lookup("yes") != nil {
		t.Error("attach must not expose --yes: it allocates nothing")
	}
}

// Upstream 1.0 'colab attach': a runtime created in the browser becomes a
// local named session usable by -s NAME with no allocation.
func TestAttachWebRuntimeIsUsableViaStatus(t *testing.T) {
	rt := webAttachRuntime()
	h := newPublicAttachHarness(t, rt)
	h.ctrl.ResetLog()

	if code := h.run(t, "", "attach", "--runtime", rt.Name, "--session", "webclone"); code != 0 {
		t.Fatalf("attach exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	assertAttachTokenAbsent(t, h.stdout.String(), h.stderr.String())

	s, err := h.deps.Store.Get("webclone")
	if err != nil {
		t.Fatalf("attached session was not persisted: %v", err)
	}
	if got := s.Token.Reveal(); got != attachTestToken {
		t.Errorf("persisted token = %q, want the listed proxy token", got)
	}
	if s.URL != rt.URL || s.Endpoint != rt.Endpoint || s.RuntimeName != rt.Name {
		t.Errorf("connection info mismatch: url=%q endpoint=%q runtime=%q",
			s.URL, s.Endpoint, s.RuntimeName)
	}
	if s.TokenExpiresAt == nil || time.Until(*s.TokenExpiresAt) <= 0 {
		t.Errorf("token expiry not persisted: %v", s.TokenExpiresAt)
	}
	if s.Variant != "GPU" || s.Accelerator != "T4" || s.MachineShape != "HIGH_RAM" {
		t.Errorf("hardware not persisted: variant=%q accelerator=%q shape=%q",
			s.Variant, s.Accelerator, s.MachineShape)
	}

	// The attached session must resolve with -s NAME in an existing command.
	if code := h.run(t, "", "status", "-s", "webclone"); code != 0 {
		t.Fatalf("status -s webclone exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "[webclone] "+rt.Endpoint) {
		t.Errorf("status did not show the attached session: stdout=%q", h.stdout.String())
	}
	assertNoAllocationPosts(t, h)
	assertAttachTokenAbsent(t, h.stdout.String(), h.stderr.String())
}

// An unknown runtime is a usage error and must not touch local state.
func TestAttachUnknownRuntimeIsUsageError(t *testing.T) {
	h := newPublicAttachHarness(t, webAttachRuntime())
	h.ctrl.ResetLog()

	code := h.run(t, "", "attach", "--runtime", "runtimes/r-does-not-exist")
	if code != 2 {
		t.Fatalf("attach exit = %d, want 2; stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "not found") {
		t.Errorf("error should name the missing runtime: stderr=%q", h.stderr.String())
	}
	sessions, err := h.deps.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("unknown runtime wrote %d session(s)", len(sessions))
	}
	assertNoAllocationPosts(t, h)
	assertAttachTokenAbsent(t, h.stdout.String(), h.stderr.String())
}

// A duplicate local name must be rejected without overwriting the existing
// session record.
func TestAttachDuplicateSessionNameIsUsageError(t *testing.T) {
	rt := webAttachRuntime()
	h := newPublicAttachHarness(t, rt)
	h.putSession(t, "taken")
	h.ctrl.ResetLog()

	code := h.run(t, "", "attach", "--runtime", rt.Name, "--session", "taken")
	if code != 2 {
		t.Fatalf("attach exit = %d, want 2; stderr=%q", code, h.stderr.String())
	}
	s, err := h.deps.Store.Get("taken")
	if err != nil {
		t.Fatal(err)
	}
	if s.Endpoint != "endpoint-test" {
		t.Errorf("duplicate name overwrote the existing session: endpoint=%q", s.Endpoint)
	}
	assertNoAllocationPosts(t, h)
	assertAttachTokenAbsent(t, h.stdout.String(), h.stderr.String())
}

// Browser-created runtimes are auto-discovered on the legacy tunnel plane too:
// attach no longer demands --public-api, and still allocates nothing.
func TestAttachWorksOnLegacyTunnelPlane(t *testing.T) {
	h := newHarness(t)
	assignment, err := h.client.Assign(context.Background(), colabapi.AssignRequest{})
	if err != nil {
		t.Fatal(err)
	}
	h.ctrl.ResetLog()

	code := h.run(t, "", "attach", "--runtime", assignment.Endpoint, "--session", "tunnelweb")
	if code != 0 {
		t.Fatalf("attach exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if strings.Contains(h.stderr.String(), "--public-api") {
		t.Errorf("legacy attach should not demand --public-api: stderr=%q", h.stderr.String())
	}
	s, err := h.deps.Store.Get("tunnelweb")
	if err != nil {
		t.Fatal(err)
	}
	if s.Endpoint != assignment.Endpoint || s.Token.Reveal() != assignment.RuntimeProxyInfo.Token.Reveal() {
		t.Errorf("attached session = %+v", s)
	}
	assertNoAllocationPosts(t, h)
	assertAttachTokenAbsent(t, h.stdout.String(), h.stderr.String())
}

// The default name is the sanitized, truncated suffix of the runtime name and
// always satisfies state.ValidateName.
func TestAttachDefaultNameIsSanitizedAndTruncated(t *testing.T) {
	long := "r-" + strings.Repeat("a", 80)
	rt := webAttachRuntime()
	rt.Name = "runtimes/r-web@1234"
	h := newPublicAttachHarness(t, rt)
	longRT := rt
	longRT.Name = "runtimes/" + long
	longRT.Endpoint = "web-endpoint-long"
	h.ctrl.SetRuntimes([]fakecolab.Runtime{rt, longRT})

	if code := h.run(t, "", "attach", "--runtime", rt.Name); code != 0 {
		t.Fatalf("attach exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if code := h.run(t, "", "attach", "--runtime", longRT.Name); code != 0 {
		t.Fatalf("attach long exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if _, err := h.deps.Store.Get("r-web-1234"); err != nil {
		t.Errorf("sanitized default name was not used: %v", err)
	}
	if _, err := h.deps.Store.Get(long[:64]); err != nil {
		t.Errorf("truncated default name was not used: %v", err)
	}
	if err := state.ValidateName(long[:64]); err != nil {
		t.Errorf("truncated default name is invalid: %v", err)
	}
	assertNoAllocationPosts(t, h)
	assertAttachTokenAbsent(t, h.stdout.String(), h.stderr.String())
}

// A bare endpoint (as returned by `golab sessions`) also resolves to the
// browser-created runtime.
func TestAttachResolvesBareEndpoint(t *testing.T) {
	rt := webAttachRuntime()
	h := newPublicAttachHarness(t, rt)

	if code := h.run(t, "", "attach", "--runtime", rt.Endpoint); code != 0 {
		t.Fatalf("attach by endpoint exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if _, err := h.deps.Store.Get("r-web-1234"); err != nil {
		t.Errorf("endpoint lookup did not persist the default-named session: %v", err)
	}
	assertNoAllocationPosts(t, h)
	assertAttachTokenAbsent(t, h.stdout.String(), h.stderr.String())
}

func TestAttachRejectsInvalidSessionName(t *testing.T) {
	rt := webAttachRuntime()
	h := newPublicAttachHarness(t, rt)
	h.ctrl.ResetLog()

	code := h.run(t, "", "attach", "--runtime", rt.Name, "--session", "bad name")
	if code != 2 {
		t.Fatalf("attach exit = %d, want 2; stderr=%q", code, h.stderr.String())
	}
	assertNoAllocationPosts(t, h)
	assertAttachTokenAbsent(t, h.stdout.String(), h.stderr.String())
}
