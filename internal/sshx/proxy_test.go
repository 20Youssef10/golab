package sshx

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/testutil/fakesshd"
)

// Ports upstream tests/test_ssh.py::test_bridge_proxy_mode_pumps_ws_to_stdout
// and ::test_bridge_proxy_mode_pumps_stdin_to_ws: the proxy bridge must be
// byte-exact in both directions, with no framing or transformation.
func TestProxyBridgeByteExact(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{Echo: true})
	var out, errBuf syncBuffer
	gate := make(chan struct{})
	stdin := &gatedReader{data: []byte("ping \x00 hello\n"), gate: gate}
	type result struct {
		rc  int
		err error
	}
	runner := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeProxy,
		Identity: writePub(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIuser host"),
		Stdin:    stdin, Stdout: &out, Stderr: &errBuf,
	})
	done := make(chan result, 1)
	go func() {
		rc, err := runner.Run(context.Background())
		done <- result{rc, err}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && out.Len() < len("ping \x00 hello\n") {
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	res := <-done
	if res.err != nil {
		t.Fatalf("Run: %v", res.err)
	}
	if res.rc != 0 {
		t.Fatalf("rc = %d", res.rc)
	}
	if out.String() != "ping \x00 hello\n" {
		t.Fatalf("stdout = %q; want byte-exact echo", out.String())
	}
}

// gatedReader yields its data, then blocks until gate closes before EOF. It
// keeps the stdin side of the proxy bridge open long enough for the echo to
// come back (closing stdin early is what OpenSSH's ProxyCommand would do on a
// real connection, but races this in-process echo test).
type gatedReader struct {
	data []byte
	gate <-chan struct{}
}

func (g *gatedReader) Read(p []byte) (int, error) {
	if len(g.data) > 0 {
		n := copy(p, g.data)
		g.data = g.data[n:]
		return n, nil
	}
	<-g.gate
	return 0, io.EOF
}

// Ports docs/02 §4: text frames received are treated as bytes; close ⇒ EOF.
func TestProxyBridgeTextFramesAndClose(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{
		Echo:       true,
		InjectText: [][]byte{[]byte("TEXTMARK")},
	})
	var out syncBuffer
	gate := make(chan struct{})
	runner := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeProxy,
		Identity: writePub(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIuser host"),
		Stdin:    &gatedReader{gate: gate}, Stdout: &out, Stderr: &syncBuffer{},
	})
	type result struct {
		rc  int
		err error
	}
	done := make(chan result, 1)
	go func() {
		rc, err := runner.Run(context.Background())
		done <- result{rc, err}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "TEXTMARK") {
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	res := <-done
	if res.err != nil {
		t.Fatalf("Run: %v", res.err)
	}
	if res.rc != 0 {
		t.Fatalf("rc = %d", res.rc)
	}
	if !strings.Contains(out.String(), "TEXTMARK") {
		t.Fatalf("text frame not treated as bytes: stdout = %q", out.String())
	}
}

// Ports upstream tests/test_windows_ssh.py::test_ssh_uses_local_null_device
// and doc 05/ssh: host-key checking is disabled with a null known-hosts file
// because VM host keys are ephemeral (ADR-005).
func TestHostKeyPolicyIsAcceptAny(t *testing.T) {
	cfg := sshClientConfigForTest()
	if cfg.HostKeyCallback == nil {
		t.Fatal("HostKeyCallback must be set")
	}
	// Accept any host key: a randomly generated key must not produce an error.
	_, pub, err := ed25519KeyForTest()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.HostKeyCallback("colab-runtime:22", &net.TCPAddr{}, pub); err != nil {
		t.Fatalf("ephemeral host key rejected: %v", err)
	}
}
