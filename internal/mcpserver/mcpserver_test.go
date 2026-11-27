package mcpserver_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/20Youssef10/golab/internal/mcpserver"
)

// fakeGolab is a stand-in for the golab binary. It records argv and stdin,
// then emits a well-formed agentout document (the format produced by
// `golab --output json`). Its modes exercise success, redaction and failure.
const fakeGolab = `#!/bin/sh
if [ -n "$FAKE_GOLAB_LOG" ]; then printf '%s\n' "$*" >> "$FAKE_GOLAB_LOG"; fi
if [ -n "$FAKE_GOLAB_STDIN" ]; then cat > "$FAKE_GOLAB_STDIN"; fi
case "$FAKE_GOLAB_MODE" in
  fail)
    printf '%s\n' '[colab] boom Authorization: Bearer abcdefghijklmnop {"access_token":"supersecret123"}' >&2
    exit 1 ;;
  secret)
    printf '%s\n' '{"schema":"golab/1","events":[{"schema":"golab/1","type":"output","text":"Authorization: Bearer abcdefghijklmnop"},{"schema":"golab/1","type":"output","text":"{\"access_token\":\"supersecret123\"}"}]}' ;;
  *)
    printf '%s\n' '{"schema":"golab/1","events":[{"schema":"golab/1","type":"output","text":"ok from fake"}]}' ;;
esac
`

func writeFakeGolab(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "golab-fake")
	if err := os.WriteFile(bin, []byte(fakeGolab), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return bin, filepath.Join(dir, "argv.log"), filepath.Join(dir, "stdin.txt")
}

func newTestServer(t *testing.T, bin string) (*mcp.Server, *mcp.ClientSession) {
	t.Helper()
	srv, err := mcpserver.NewServer(mcpserver.Config{GolabBin: bin})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	clientToServer, serverToClient := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), serverToClient, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	cs, err := client.Connect(context.Background(), clientToServer, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return srv, cs
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("nil CallToolResult")
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

var allToolNames = []string{
	"colab_cat", "colab_compat", "colab_cost", "colab_doctor", "colab_du",
	"colab_exec", "colab_fetch", "colab_find", "colab_grep", "colab_jobs",
	"colab_logs", "colab_new", "colab_ps", "colab_reap", "colab_resume",
	"colab_run", "colab_sessions", "colab_status", "colab_stop", "colab_tail",
	"colab_usage", "colab_wait",
}

var confirmGated = []string{
	"colab_exec", "colab_fetch", "colab_new", "colab_reap", "colab_resume", "colab_run", "colab_stop",
}

func minimalArgs(name string) map[string]any {
	switch name {
	case "colab_cat", "colab_tail", "colab_du":
		return map[string]any{"path": "/content/a.txt"}
	case "colab_grep":
		return map[string]any{"pattern": "needle", "path": "/content/a.txt"}
	case "colab_find":
		return map[string]any{"name": "a.txt"}
	case "colab_logs":
		return map[string]any{"id": "run-1"}
	case "colab_exec":
		return map[string]any{"session": "vm", "code": "print('hi')"}
	case "colab_run":
		return map[string]any{"script": "/tmp/sentinel.py"}
	case "colab_fetch":
		return map[string]any{"url": "https://example.com/a"}
	case "colab_reap":
		return map[string]any{"dry_run": false}
	default:
		return map[string]any{"session": "vm"}
	}
}

// TestListTools covers the contract every MCP client sees: the full suite, a
// valid object input schema, a non-empty description with side effects, and
// behaviour annotations that distinguish read-only from mutating tools.
func TestListTools(t *testing.T) {
	bin, _, _ := writeFakeGolab(t)
	_, cs := newTestServer(t, bin)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var got []string
	byName := map[string]bool{}
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		byName[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("%s: empty description", tool.Name)
		}
		if tool.Annotations == nil {
			t.Errorf("%s: nil annotations", tool.Name)
			continue
		}
		schemaBytes, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("%s: marshal schema: %v", tool.Name, err)
		}
		var schema struct {
			Type       string         `json:"type"`
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal(schemaBytes, &schema); err != nil {
			t.Fatalf("%s: unmarshal schema: %v", tool.Name, err)
		}
		if schema.Type != "object" {
			t.Errorf("%s: invalid input schema: %s", tool.Name, schemaBytes)
		}
		if _, hasConfirm := schema.Properties["confirm"]; hasConfirm {
			if !contains(confirmGated, tool.Name) {
				t.Errorf("%s: unexpected confirm field", tool.Name)
			}
		}
	}
	sort.Strings(got)
	want := append([]string(nil), allToolNames...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tool names:\n got %v\nwant %v", got, want)
	}
	for _, name := range []string{"colab_sessions", "colab_status", "colab_usage", "colab_cost", "colab_ps", "colab_cat", "colab_doctor"} {
		if tool, err := findTool(res.Tools, name); err != nil {
			t.Error(err)
		} else if !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s: expected readOnlyHint", name)
		}
	}
	for _, name := range confirmGated {
		tool, err := findTool(res.Tools, name)
		if err != nil {
			t.Error(err)
			continue
		}
		if tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("%s: expected destructiveHint and no readOnlyHint", name)
		}
		if !strings.Contains(strings.ToLower(tool.Description), "confirm") {
			t.Errorf("%s: description must explain confirm gating", name)
		}
	}
	_ = byName
}

func findTool(tools []*mcp.Tool, name string) (*mcp.Tool, error) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool, nil
		}
	}
	return nil, &toolNotFound{name}
}

type toolNotFound struct{ name string }

func (e *toolNotFound) Error() string { return "tool not found: " + e.name }

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestReadOnlyCall proves the client/server session path and the typed JSON
// envelope parser, and that argv starts with `--output json <command>`.
func TestReadOnlyCall(t *testing.T) {
	bin, log, _ := writeFakeGolab(t)
	t.Setenv("FAKE_GOLAB_LOG", log)
	_, cs := newTestServer(t, bin)
	res := call(t, cs, "colab_sessions", nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	if !strings.Contains(textOf(t, res), "ok from fake") {
		t.Fatalf("unexpected output: %q", textOf(t, res))
	}
	argv, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	if !strings.Contains(string(argv), "--output json sessions") {
		t.Fatalf("argv log: %q", string(argv))
	}
}

// TestConfirmGate checks that no billable/destructive tool reaches the
// subprocess without an explicit confirm, and that every one can be unlocked.
func TestConfirmGate(t *testing.T) {
	bin, log, _ := writeFakeGolab(t)
	t.Setenv("FAKE_GOLAB_LOG", log)
	_, cs := newTestServer(t, bin)
	for _, name := range confirmGated {
		t.Run(name+"-denied", func(t *testing.T) {
			if err := os.Remove(log); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			res := call(t, cs, name, minimalArgs(name))
			if !res.IsError {
				t.Fatalf("%s ran without confirm: %q", name, textOf(t, res))
			}
			if !strings.Contains(textOf(t, res), "confirm: true") {
				t.Fatalf("%s: gate message: %q", name, textOf(t, res))
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatalf("%s: subprocess was invoked without confirm", name)
			}
		})
		t.Run(name+"-allowed", func(t *testing.T) {
			if err := os.Remove(log); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			args := minimalArgs(name)
			args["confirm"] = true
			res := call(t, cs, name, args)
			if res.IsError {
				t.Fatalf("%s with confirm: %q", name, textOf(t, res))
			}
			if _, err := os.Stat(log); err != nil {
				t.Fatalf("%s: subprocess was not invoked: %v", name, err)
			}
		})
	}
	// Reap is safe by default: dry-run needs no confirmation.
	t.Run("reap-dry-run", func(t *testing.T) {
		if err := os.Remove(log); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		res := call(t, cs, "colab_reap", map[string]any{})
		if res.IsError {
			t.Fatalf("reap dry-run: %q", textOf(t, res))
		}
		argv, err := os.ReadFile(log)
		if err != nil || !strings.Contains(string(argv), "--dry-run") {
			t.Fatalf("reap argv: %q, err %v", string(argv), err)
		}
	})
}

// TestRedaction verifies that credentials in either channel never reach a
// tool result; the subprocess deliberately emits both header and JSON forms.
func TestRedaction(t *testing.T) {
	bin, _, _ := writeFakeGolab(t)
	t.Setenv("FAKE_GOLAB_MODE", "secret")
	_, cs := newTestServer(t, bin)
	res := call(t, cs, "colab_sessions", nil)
	body := textOf(t, res)
	for _, secret := range []string{"abcdefghijklmnop", "supersecret123"} {
		if strings.Contains(body, secret) {
			t.Fatalf("secret leaked: %q", body)
		}
	}
	if !strings.Contains(body, "REDACTED") {
		t.Fatalf("expected redaction marker: %q", body)
	}
}

// TestErrorMapping checks failures become isError tool results with redacted
// diagnostic text, never protocol errors or leaked credentials.
func TestErrorMapping(t *testing.T) {
	bin, _, _ := writeFakeGolab(t)
	t.Setenv("FAKE_GOLAB_MODE", "fail")
	_, cs := newTestServer(t, bin)
	res := call(t, cs, "colab_sessions", nil)
	if !res.IsError {
		t.Fatalf("expected isError: %q", textOf(t, res))
	}
	body := textOf(t, res)
	if strings.Contains(body, "abcdefghijklmnop") || strings.Contains(body, "supersecret123") {
		t.Fatalf("secret leaked in error: %q", body)
	}
	if !strings.Contains(body, "REDACTED") || !strings.Contains(body, "boom") {
		t.Fatalf("unexpected error text: %q", body)
	}
}

// TestExecCodeViaStdin proves user code is sent on stdin, never spliced into
// argv (AGENTS.md: never splice user strings into code run on the VM).
func TestExecCodeViaStdin(t *testing.T) {
	bin, log, stdinFile := writeFakeGolab(t)
	t.Setenv("FAKE_GOLAB_LOG", log)
	t.Setenv("FAKE_GOLAB_STDIN", stdinFile)
	_, cs := newTestServer(t, bin)
	const code = "print('sentinel_42')"
	res := call(t, cs, "colab_exec", map[string]any{
		"session": "vm", "code": code, "confirm": true,
	})
	if res.IsError {
		t.Fatalf("exec: %q", textOf(t, res))
	}
	got, err := os.ReadFile(stdinFile)
	if err != nil || !strings.Contains(string(got), code) {
		t.Fatalf("stdin: %q, err %v", string(got), err)
	}
	argv, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("argv log: %v", err)
	}
	if strings.Contains(string(argv), "sentinel_42") {
		t.Fatalf("code leaked into argv: %q", string(argv))
	}
	if !strings.Contains(string(argv), "--output json exec") {
		t.Fatalf("argv: %q", string(argv))
	}
}

// TestResources exposes the bundled agent guidance to any MCP client that
// supports resources/read.
func TestResources(t *testing.T) {
	bin, _, _ := writeFakeGolab(t)
	_, cs := newTestServer(t, bin)
	res, err := cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	uris := map[string]bool{}
	for _, r := range res.Resources {
		uris[r.URI] = true
	}
	for _, want := range []string{"golab://docs/skill", "golab://docs/usage"} {
		if !uris[want] {
			t.Errorf("missing resource %s in %v", want, uris)
		}
	}
	read, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "golab://docs/skill"})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	var body strings.Builder
	for _, c := range read.Contents {
		if c.Text != "" {
			body.WriteString(c.Text)
		}
	}
	if !strings.Contains(body.String(), "Safety rules") {
		t.Fatalf("skill resource: %q", body.String())
	}
}

// TestStreamableHTTP exercises the same tool set over the official SDK's
// streamable HTTP transport, so HTTP-only clients see identical contracts.
func TestStreamableHTTP(t *testing.T) {
	bin, _, _ := writeFakeGolab(t)
	srv, err := mcpserver.NewServer(mcpserver.Config{GolabBin: bin})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mcpserver.NewHTTPHandler(srv))
	defer ts.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "http-client", Version: "1.0.0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("HTTP connect: %v", err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("HTTP ListTools: %v", err)
	}
	if len(tools.Tools) != len(allToolNames) {
		t.Fatalf("HTTP tools: got %d want %d", len(tools.Tools), len(allToolNames))
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "colab_sessions"})
	if err != nil || res.IsError {
		t.Fatalf("HTTP CallTool: res=%v err=%v", res, err)
	}
	if !strings.Contains(textOf(t, res), "ok from fake") {
		t.Fatalf("HTTP output: %q", textOf(t, res))
	}
}

// TestStdioPurity connects an official SDK client to the real stdio
// entrypoint through a stdout tee, then asserts every byte the process wrote
// to stdout is a JSON-RPC message — the hard requirement in the official
// build guide. It also checks the modern protocol era negotiates.
func TestStdioPurity(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "stdout.ndjson")
	bin, _, _ := writeFakeGolab(t)
	script := fmt.Sprintf("%q -test.run '^TestHelperStdioServer$' | tee %q", os.Args[0], capture)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "GOLAB_MCP_TEST_STDIO=1", "FAKE_GOLAB_BIN="+bin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "purity-client", Version: "1.0.0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("CommandTransport connect: %v; stderr=%q", err, stderr.String())
	}
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("stdio ListTools: %v", err)
	}
	if len(tools.Tools) != len(allToolNames) {
		t.Fatalf("stdio tools: got %d want %d", len(tools.Tools), len(allToolNames))
	}
	if err := cs.Close(); err != nil {
		t.Fatalf("session close: %v", err)
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	lines := bufio.NewScanner(bytes.NewReader(data))
	count := 0
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" {
			continue
		}
		count++
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("stdout line %d is not JSON: %q (%v)", count, line, err)
		}
		if msg["jsonrpc"] != "2.0" {
			t.Fatalf("stdout line %d is not JSON-RPC: %q", count, line)
		}
	}
	if count == 0 {
		t.Fatalf("no JSON-RPC on stdout; stderr=%q", stderr.String())
	}
}

// TestLegacyProtocolEra drives the raw 2025-06-18 initialize handshake and
// confirms a legacy MCP client sees the same tool list. The Go SDK negotiates
// both protocol eras; this test guards the fallback path.
func TestLegacyProtocolEra(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperStdioServer$")
	cmd.Env = append(os.Environ(), "GOLAB_MCP_TEST_STDIO=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	reader := bufio.NewReader(stdout)
	_, _ = io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"legacy","version":"1"}}}`+"\n")
	var initResp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
		Error any `json:"error"`
	}
	readJSONLine(t, reader, &initResp)
	if initResp.Error != nil {
		t.Fatalf("legacy initialize error: %v", initResp.Error)
	}
	if initResp.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("negotiated version = %q, want 2025-06-18", initResp.Result.ProtocolVersion)
	}
	_, _ = io.WriteString(stdin, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n")
	_, _ = io.WriteString(stdin, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`+"\n")
	var listResp struct {
		Result struct {
			Tools []*mcp.Tool `json:"tools"`
		} `json:"result"`
		Error any `json:"error"`
	}
	readJSONLine(t, reader, &listResp)
	if listResp.Error != nil {
		t.Fatalf("legacy tools/list error: %v", listResp.Error)
	}
	if len(listResp.Result.Tools) != len(allToolNames) {
		t.Fatalf("legacy tools: got %d want %d", len(listResp.Result.Tools), len(allToolNames))
	}
}

func readJSONLine(t *testing.T, r *bufio.Reader, dst any) {
	t.Helper()
	lineCh, errCh := make(chan string, 1), make(chan error, 1)
	go func() {
		s, err := r.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		lineCh <- s
	}()
	select {
	case line := <-lineCh:
		if err := json.Unmarshal([]byte(line), dst); err != nil {
			t.Fatalf("invalid JSON-RPC line %q: %v", line, err)
		}
	case err := <-errCh:
		t.Fatalf("read JSON-RPC: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for JSON-RPC response")
	}
}

// TestHelperStdioServer is only executed as a child of TestStdioPurity.
func TestHelperStdioServer(t *testing.T) {
	if os.Getenv("GOLAB_MCP_TEST_STDIO") != "1" {
		return
	}
	_ = mcpserver.Serve(context.Background(), mcpserver.Config{}, "stdio", "")
	os.Exit(0) // never let the testing framework write PASS to stdout
}
