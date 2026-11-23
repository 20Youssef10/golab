package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
	"github.com/20Youssef10/golab/internal/testutil/fakesshd"
)

func stream(name, text string) fakekernel.Step { return fakekernel.Stream(name, text) }
func statusIdle() fakekernel.Step              { return fakekernel.Status("idle") }
func statusIdleAfter(d time.Duration) fakekernel.Step {
	return fakekernel.Step{Delay: d, MsgType: "status", Content: map[string]any{"execution_state": "idle"}}
}
func replyOK() fakekernel.Step                   { return fakekernel.Reply("ok", nil) }
func inputRequest(prompt string) fakekernel.Step { return fakekernel.InputRequest(prompt) }
func colabRequest(authType, id string) fakekernel.Step {
	return fakekernel.ColabRequest(authType, id)
}

// setStreams wires the harness buffers into the deps for tests that call a
// command's core helper directly (cobra normally does this via SetStreams).
func setStreams(h *testHarness) {
	h.deps.Stdin = strings.NewReader("")
	h.deps.Stdout = &h.stdout
	h.deps.Stderr = &h.stderr
}

// waitInputs polls the fake kernel for recorded input_reply frames. The
// drivemount reply is sent from a hook goroutine, so it can lag Execute.
func waitInputs(t *testing.T, h *testHarness, n int) []fakekernel.InputRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := h.kernel.Inputs()
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not receive %d input replies (got %d)", n, len(h.kernel.Inputs()))
	return nil
}

// writeEd25519Key writes a real OpenSSH-format Ed25519 private key so the
// embedded SSH client can authenticate against the fake sshd.
func writeEd25519Key(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "test")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// newFakeSSH starts a loopback SSH endpoint and registers its cleanup.
func newFakeSSH(t *testing.T, opts fakesshd.Options) *fakesshd.Server {
	t.Helper()
	srv, err := fakesshd.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// putSSHSession stores a session whose data-plane URL points at the fake sshd.
func putSSHSession(t *testing.T, h *testHarness, name, sshdURL, token string) {
	t.Helper()
	expiry := time.Now().Add(time.Hour)
	s := state.Session{
		Name: name, Endpoint: "ep-ssh", URL: sshdURL,
		Token: logx.NewSecret(token), TokenExpiresAt: &expiry,
		Variant: "DEFAULT", Accelerator: "NONE", MachineShape: "STANDARD",
	}
	if err := h.deps.Store.Put(s); err != nil {
		t.Fatal(err)
	}
}

// overrideAssignResponse makes the next alloca­tion hand out the fake sshd URL,
// so an auto-created session can complete an SSH connection entirely in-process.
func overrideAssignResponse(h *testHarness, sshdURL string) {
	body := fmt.Sprintf(
		`{"accelerator":"NONE","endpoint":"ep-ssh","runtimeProxyInfo":{"token":"proxy-ssh-token","tokenExpiresInSeconds":3600,"url":%q},"variant":0}`,
		sshdURL)
	h.ctrl.SetFault(fakecolab.Fault{Method: http.MethodPost, PathContains: "/assign", Status: http.StatusOK, Body: body})
}
