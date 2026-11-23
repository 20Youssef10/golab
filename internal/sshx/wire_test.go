package sshx

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/testutil/fakesshd"
)

// Ports upstream tests/test_ssh_wire_contract.py::test_wire_contract_path_token_and_pubkey_header.
// The real connect path runs against a loopback server; we assert on the bytes
// the server actually received.
func TestWireContractPathTokenAndPubkeyHeader(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{})
	pubLine := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIWIRECONTRACTMARKER00000000000000000000 wire-contract@colab-cli-test"
	identity := writePub(t, pubLine)

	runViaProxy(t, srv, identity, "TOKEN123", "colab-cli", ModeProxy, strings.NewReader(""), nil)

	req := srv.LastRequest()
	if req == nil {
		t.Fatal("server saw no upgrade request")
	}
	if req.Method != http.MethodGet {
		t.Errorf("method = %q; want GET", req.Method)
	}
	if req.Path != "/colab/ssh" {
		t.Errorf("path = %q; want /colab/ssh", req.Path)
	}
	if !strings.Contains(req.Query, "colab-runtime-proxy-token=TOKEN123") {
		t.Errorf("query = %q; token missing", req.Query)
	}
	if !strings.Contains(req.Query, "colab-client-agent=colab-cli") {
		t.Errorf("query = %q; agent missing", req.Query)
	}
	if !strings.Contains(req.Query, "colab-client-agent=ssh-mode-proxy") {
		t.Errorf("query = %q; mode missing", req.Query)
	}
	wantTarget := "/colab/ssh?colab-runtime-proxy-token=TOKEN123&colab-client-agent=colab-cli&colab-client-agent=ssh-mode-proxy"
	if req.Target != wantTarget {
		t.Errorf("target = %q; want %q", req.Target, wantTarget)
	}
	if got := req.Header.Get(PubkeyHeader); got != pubLine {
		t.Errorf("%s = %q; want verbatim %q", PubkeyHeader, got, pubLine)
	}
	if got := req.Header.Get(ClientAgentHeader); got != "colab-cli" {
		t.Errorf("%s = %q; want colab-cli", ClientAgentHeader, got)
	}
}

// Ports upstream tests/test_ssh_wire_contract.py::test_wire_contract_distinct_token_reaches_wire.
func TestWireContractDistinctToken(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{})
	identity := writePub(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIuser host")
	runViaProxy(t, srv, identity, "DISTINCT_TOKEN_zzz999", "colab-cli", ModeProxy, strings.NewReader(""), nil)
	req := srv.LastRequest()
	if req == nil || !strings.Contains(req.Query, "colab-runtime-proxy-token=DISTINCT_TOKEN_zzz999") {
		t.Fatalf("distinct token not on wire: %+v", req)
	}
}

// Ports upstream tests/test_ssh_wire_contract.py::test_interactive_ssh_reinvocation_sends_interactive_mode.
func TestWireContractModes(t *testing.T) {
	for _, tc := range []struct {
		mode Mode
		want string
	}{
		{ModeInteractive, "ssh-mode-interactive"},
		{ModeCmd, "ssh-mode-cmd"},
		{ModeProxy, "ssh-mode-proxy"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			srv := newFakeServer(t, fakesshd.Options{ShellHold: 30 * 1e6})
			identity := writeKey(t)
			var out bytes.Buffer
			r := newRunner(t, Config{
				ProxyURL:    srv.URL(),
				Token:       logx.NewSecret("TOKEN"),
				Mode:        tc.mode,
				Identity:    identity,
				Stdin:       strings.NewReader(""),
				Stdout:      &out,
				ClientAgent: "colab-cli",
				Command:     []string{"true"},
			})
			if _, err := r.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			req := srv.LastRequest()
			if req == nil || !strings.Contains(req.Query, tc.want) {
				t.Fatalf("mode %q not on wire: %+v", tc.want, req)
			}
		})
	}
}

// Ports upstream tests/test_ssh_wire_contract.py::test_real_status_mapping_via_loopback.
// A real failing upgrade is mapped to a typed HandshakeError with the upstream
// message; no string matching of status numbers anywhere in the client.
func TestHandshakeStatusMappingViaServer(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{400, "unsupported key type", "ssh-keygen -t ed25519"},
		{400, "missing pubkey", "missing pubkey header"},
		{401, "", "token is invalid"},
		{403, "", "Forbidden"},
		{404, "", "Endpoint not found"},
		{429, `{"error":"already-active-session","limit":2}`, "2 of 2 SSH sessions in use"},
		{502, "sshd unreachable", "Bad gateway"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := newFakeServer(t, fakesshd.Options{Failure: &fakesshd.Failure{Status: tc.status, Body: tc.body}})
			identity := writePub(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIuser host")
			r := newRunner(t, Config{
				ProxyURL: srv.URL(), Token: logx.NewSecret("TOKEN"), Mode: ModeProxy,
				Identity: identity, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
			})
			_, err := r.Run(context.Background())
			var he *HandshakeError
			if !errors.As(err, &he) {
				t.Fatalf("want *HandshakeError, got %T: %v", err, err)
			}
			if he.Status != tc.status {
				t.Errorf("status = %d; want %d", he.Status, tc.status)
			}
			if !strings.Contains(he.Message, tc.want) {
				t.Errorf("message = %q; want substring %q", he.Message, tc.want)
			}
			// The server must still have seen a contracted request on the rejection path.
			if req := srv.LastRequest(); req == nil || req.Path != SSHPath {
				t.Fatalf("rejection path did not reach %s: %+v", SSHPath, req)
			}
		})
	}
}

// A failing upgrade that echoes the proxy token in its body must not store it
// in the typed error's Message or Body (AUDIT-02 S-6 cross-check).
func TestHandshakeErrorNeverContainsProxyToken(t *testing.T) {
	const token = "ECHOPROXYCANARY7f3a99"
	srv := newFakeServer(t, fakesshd.Options{Failure: &fakesshd.Failure{
		Status: 400, Body: "invalid pubkey " + token,
	}})
	identity := writePub(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIuser host")
	r := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret(token), Mode: ModeProxy,
		Identity: identity, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	})
	_, err := r.Run(context.Background())
	var he *HandshakeError
	if !errors.As(err, &he) {
		t.Fatalf("want *HandshakeError, got %T: %v", err, err)
	}
	if strings.Contains(he.Message, token) || strings.Contains(string(he.Body), token) {
		t.Fatalf("typed handshake error leaked the proxy token: %+v", he)
	}
}

func TestBuildWSURL(t *testing.T) {
	got := BuildWSURL("https://abc.colab.googleusercontent.com", logx.NewSecret("TOK"), ModeCmd, "colab-go/0.0.1-dev")
	want := "wss://abc.colab.googleusercontent.com/colab/ssh?colab-runtime-proxy-token=TOK&colab-client-agent=colab-go/0.0.1-dev&colab-client-agent=ssh-mode-cmd"
	if got != want {
		t.Fatalf("BuildWSURL = %q; want %q", got, want)
	}
	got = BuildWSURL("http://127.0.0.1:8080/", logx.NewSecret("T"), ModeProxy, "a")
	if !strings.HasPrefix(got, "ws://127.0.0.1:8080/colab/ssh?") || strings.Contains(got, "//colab") {
		t.Fatalf("http->ws / trailing slash: %q", got)
	}
	u, err := url.Parse(got)
	if err != nil || u.Path != SSHPath {
		t.Fatalf("parse %q: %v, path %q", got, err, u.Path)
	}
}

// Tokens must never reach an error message: net/http's url.Error embeds the
// request URL, including the proxy-token query (AGENTS.md security rules).
func TestNetworkErrorRedactsToken(t *testing.T) {
	const token = "SUPERSECRET_TOKEN_DO_NOT_LOG_123"
	u := BuildWSURL("http://127.0.0.1:1", logx.NewSecret(token), ModeProxy, "a")
	_, err := DialWS(context.Background(), u, "pk", "a", nil)
	if err == nil {
		t.Fatal("want connection error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked into error: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("error should be redacted: %q", err.Error())
	}
}

func newFakeServer(t *testing.T, opts fakesshd.Options) *fakesshd.Server {
	t.Helper()
	s, err := fakesshd.New(opts)
	if err != nil {
		t.Fatalf("fakesshd.New: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func writePub(t *testing.T, line string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeKey(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "id_ed25519")
	if err := generateEd25519PrivateKeyFile(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func newRunner(t *testing.T, cfg Config) *Runner {
	t.Helper()
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func runViaProxy(t *testing.T, srv *fakesshd.Server, identity, token, agent string, mode Mode, stdin *strings.Reader, stdout *bytes.Buffer) {
	t.Helper()
	if stdout == nil {
		stdout = &bytes.Buffer{}
	}
	r := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret(token), Mode: mode,
		Identity: identity, Stdin: stdin, Stdout: stdout, ClientAgent: agent,
	})
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
