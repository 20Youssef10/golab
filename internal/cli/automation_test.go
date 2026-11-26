package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/history"
)

// jsonListArg extracts the Python string passed to json.loads(...) from the
// generated install snippet and decodes it. This proves user input travels as
// JSON data, never as generated source.
func jsonListArg(t *testing.T, code string) []string {
	t.Helper()
	i := strings.Index(code, "json.loads(")
	if i < 0 {
		t.Fatalf("snippet does not use json.loads: %s", code)
	}
	rest := code[i+len("json.loads("):]
	if len(rest) == 0 || rest[0] != '"' {
		t.Fatalf("json.loads argument is not a string literal: %q", rest)
	}
	// The literal is JSON-encoded with SetEscapeHTML(false), i.e. a Go string.
	end := 1
	for end < len(rest) {
		if rest[end] == '\\' {
			end += 2
			continue
		}
		if rest[end] == '"' {
			break
		}
		end++
	}
	literal, err := strconv.Unquote(rest[:end+1])
	if err != nil {
		t.Fatalf("unquote: %v (%q)", err, rest[:end+1])
	}
	var out []string
	if err := json.Unmarshal([]byte(literal), &out); err != nil {
		t.Fatalf("unmarshal payload: %v (%q)", err, literal)
	}
	return out
}

// Ports upstream tests/test_automation.py::test_cli_install: packages are sent
// to the kernel and the automation event is recorded.
func TestInstallBuildsAndExecutesSnippet(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(
		stream("stdout", "Installation Complete (via pip)!\n"),
		statusIdle(),
		replyOK(),
	)
	code := h.run(t, "", "install", "-s", "s1", "pandas", "numpy")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	execs := h.kernel.Executions()
	if len(execs) != 1 {
		t.Fatalf("executions = %+v", execs)
	}
	got := jsonListArg(t, execs[0].Code)
	if strings.Join(got, ",") != "pandas,numpy" {
		t.Fatalf("packages = %q", got)
	}
	for _, want := range []string{"subprocess", "pip", "uv", "Installation Complete"} {
		if !strings.Contains(execs[0].Code, want) {
			t.Fatalf("snippet missing %q: %s", want, execs[0].Code)
		}
	}
	if !strings.Contains(h.stdout.String(), "Installation Complete") {
		t.Fatalf("output not streamed: %q", h.stdout.String())
	}
	s, _ := h.deps.Store.Get("s1")
	if s.LastExecution == nil || s.LastExecution.Kind != "automation:install" {
		t.Fatalf("last execution = %+v", s.LastExecution)
	}
}

// Ports the D-3 regression this port fixes: upstream string-spliced package
// names into Python source, so a quote allowed arbitrary code execution. The
// Go port must embed the list as a JSON literal decoded remotely.
func TestInstallQuotingInjectionAttempt(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK())
	evil := "x'; import os; os.system('touch /tmp/pwned-by-install'); y='"
	code := h.run(t, "", "install", "-s", "s1", "pandas", evil)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	got := jsonListArg(t, h.kernel.Executions()[0].Code)
	if len(got) != 2 || got[0] != "pandas" || got[1] != evil {
		t.Fatalf("payload round-trip failed: %q", got)
	}
	snippet := h.kernel.Executions()[0].Code
	// The dangerous text may only appear inside the JSON string argument, never
	// as a bare Python statement. json.loads(...) is the sole interpreter.
	if strings.Contains(snippet, "\nos.system(") || strings.Contains(snippet, "\nimport os;") {
		t.Fatalf("user input was spliced as code: %s", snippet)
	}
}

// Ports upstream tests/test_cli.py::test_cli_install with no arguments: exit 1.
func TestInstallRequiresPackages(t *testing.T) {
	h := newHarness(t)
	code := h.run(t, "", "install", "-s", "s1")
	if code != 1 || !strings.Contains(h.stderr.String(), "No packages or requirements") {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
}

// Ports upstream tests/test_automation.py::test_cli_auth and
// test_cli_auth_uses_long_timeout: the hidden auth command runs the Colab
// authenticate_user() flow with a generous idle timeout and stdin enabled.
func TestAuthFlow(t *testing.T) {
	if interactiveAutomationTimeout < 300*time.Second {
		t.Fatalf("interactive timeout too short: %v", interactiveAutomationTimeout)
	}
	h := newHarness(t)
	h.putSession(t, "s1")
	// A stream message precedes the stdin prompt so the runtime has installed
	// the active-exec state before the prompt arrives (mirrors upstream's
	// test_execute_stdin_and_streaming_together). The small delay keeps the
	// fixture deterministic under -race / a fully loaded test binary.
	prompt := inputRequest("Enter code: ")
	prompt.Delay = 50 * time.Millisecond
	// Keep the WebSocket open after the prompt: the stdin reply is written by
	// the runtime's independent reader goroutine, so ending the script (and
	// closing the socket) immediately after the prompt races with that write.
	idle := statusIdle()
	idle.Delay = 300 * time.Millisecond
	reply := replyOK()
	reply.Delay = 500 * time.Millisecond
	h.kernel.Script(
		stream("stdout", "prompt incoming\n"),
		prompt,
		idle,
		reply,
	)
	code := h.run(t, "secret-code\n", "auth", "-s", "s1")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	exec := h.kernel.Executions()[0]
	for _, want := range []string{"USE_AUTH_EPHEM", "authenticate_user()"} {
		if !strings.Contains(exec.Code, want) {
			t.Fatalf("auth snippet missing %q: %s", want, exec.Code)
		}
	}
	if !exec.AllowStdin {
		t.Fatal("auth must allow stdin")
	}
	inputs := waitInputs(t, h, 1)
	if !strings.Contains(string(inputs[0].Value), "secret-code") {
		t.Fatalf("stdin not forwarded: %s", inputs[0].Value)
	}
	s, _ := h.deps.Store.Get("s1")
	if s.LastExecution == nil || s.LastExecution.Kind != "automation:auth" {
		t.Fatalf("last execution = %+v", s.LastExecution)
	}
}

// Ports upstream tests/test_automation.py::test_cli_drivemount and the
// drivefs_hook round-trip tests: a dfs_ephemeral colab_request must trigger
// credentials propagation and be answered with a colab_reply on stdin.
func TestDriveMountColabReplyRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.ctrl.SetPropagationAuthorizeURL("https://consent.example/auth")
	// Give the hook goroutine time to complete the control-plane round-trip and
	// send colab_reply before the scripted kernel declares itself idle.
	// Keep the socket open a little longer than the hook needs so the
	// colab_reply is recorded before the CLI closes the kernel.
	reply := replyOK()
	reply.Delay = 700 * time.Millisecond
	h.kernel.Script(
		colabRequest("dfs_ephemeral", "cm-42"),
		statusIdleAfter(500*time.Millisecond),
		reply,
	)
	code := h.run(t, "\n", "drivemount", "-s", "s1", "/foo/bar")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	exec := h.kernel.Executions()[0]
	if !strings.Contains(exec.Code, `drive.mount("/foo/bar")`) {
		t.Fatalf("mount snippet = %s", exec.Code)
	}
	inputs := waitInputs(t, h, 1)
	var body map[string]any
	if err := json.Unmarshal(inputs[0].Value, &body); err != nil {
		t.Fatalf("reply is not JSON: %s", inputs[0].Value)
	}
	if body["type"] != "colab_reply" || body["colab_msg_id"] != "cm-42" {
		t.Fatalf("reply body = %v", body)
	}
	if parent, _ := inputs[0].ParentHeader["msg_id"].(string); parent == "" {
		t.Fatalf("reply has no parent_header: %+v", inputs[0].ParentHeader)
	}
	combined := h.stdout.String() + h.stderr.String()
	if !strings.Contains(combined, "https://consent.example/auth") {
		t.Fatalf("consent URL not shown: %q", combined)
	}
	if !strings.Contains(combined, "Credentials propagated") {
		t.Fatalf("no success message: %q", combined)
	}
}

// Ports upstream tests/test_automation.py::test_show_drive_auth_url_headless_still_prints
// at the CLI level: a path containing quotes must be pyRepr-quoted, not spliced.
func TestDriveMountQuotesPath(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	h.kernel.Script(statusIdle(), replyOK())
	weird := "/foo/bar'); import os; os.system('touch /tmp/pwned-drive'); x=('"
	code := h.run(t, "", "drivemount", "-s", "s1", weird)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	snippet := h.kernel.Executions()[0].Code
	if !strings.Contains(snippet, strconv.Quote(weird)) {
		t.Fatalf("path not JSON-quoted: %s", snippet)
	}
	if strings.Contains(snippet, "\nimport os;") {
		t.Fatalf("path spliced as code: %s", snippet)
	}
}

// Ports upstream tests/test_automation.py::test_parse_propagation_response_reads_json
// and test_parse_propagation_response_never_raises. Only whitelisted string
// fields may surface; HTML, plain text, arrays and null degrade to empty.
func TestPropagationDetailOnlyReadsWhitelistedJSONFields(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"error", `{"success": true, "token": "tok", "error": "quota exceeded"}`, "quota exceeded"},
		{"message", "prefix\n{\"message\":\"denied\"}", "denied"},
		{"rendered-prefix", "rendered\nprefix\n{\"error\":\"bad\"}", "bad"},
		{"prefers-error", `{"error":"first","message":"second"}`, "first"},
		{"control-chars", `{"error":"bad\u001b[31mRED\u001b[0m\nvalue\u0007"}`, "badRED value"},
		{"html", `<html><body>400 Bad Request</body></html>`, ""},
		{"empty", "", ""},
		{"not-json", "not json", ""},
		{"null", "null", ""},
		{"array", "[1, 2]", ""},
		{"token-only", `{"token":"SUPER_SECRET"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := propagationDetail(tc.body); got != tc.want {
				t.Fatalf("propagationDetail(%q) = %q; want %q", tc.body, got, tc.want)
			}
		})
	}
}

// Ports upstream tests/test_automation.py::test_drive_propagation_failure_message_is_actionable
// and test_drive_propagation_failure_message_includes_server_detail.
func TestDriveFailureMessagePorted(t *testing.T) {
	m := driveFailureMessage(400, "")
	for _, want := range []string{"HTTP 400", "golab whoami", "different Google account"} {
		if !strings.Contains(m, want) {
			t.Fatalf("message missing %q: %q", want, m)
		}
	}
	if strings.Contains(m, "<html") {
		t.Fatalf("message echoed HTML: %q", m)
	}
	if m2 := driveFailureMessage(403, "quota exceeded"); !strings.Contains(m2, "Server said: quota exceeded") {
		t.Fatalf("server detail missing: %q", m2)
	}
}

// Ports upstream tests/test_automation.py::test_drivefs_hook_reports_400_without_raw_html
// (commit aa01a39): a 400 with an HTML body must report the status plus the
// actionable advice, never the raw body. The same sanitizer keeps ANSI/control
// characters and untrusted token-like fields out of the terminal and history.
func TestReportDriveFailureNeverEchoesRawHTML(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	h.putSession(t, "s1")

	html := "<html><body>400 Bad Request</body></html>\x1b[31mSECRET\x1b[0m"
	reportDriveFailure(h.deps, "s1", &colabapi.StatusError{
		Code: 400, Body: html, Method: "POST", Path: "/tun/m/credentials-propagation/ep",
	})
	out := h.stderr.String()
	for _, want := range []string{"HTTP 400", "golab whoami"} {
		if !strings.Contains(out, want) {
			t.Fatalf("message missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "REQUIRED: Google Drive Authorization") {
		t.Fatalf("message prompted for consent on a non-auth failure: %q", out)
	}
	for _, forbidden := range []string{"<html", "\x1b[31m", "SECRET"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("message leaked %q: %q", forbidden, out)
		}
	}
	if h.stdout.String() != "" {
		t.Fatalf("chatter leaked to stdout: %q", h.stdout.String())
	}

	l, err := history.NewAt(h.dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	events, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.EventType != "drive_auth_failed" {
			continue
		}
		found = true
		var payload map[string]any
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if body, _ := payload["body"].(string); strings.Contains(body, "<html") {
			t.Fatalf("history leaked raw body: %q", body)
		}
	}
	if !found {
		t.Fatalf("no drive_auth_failed event recorded: %+v", events)
	}
}

// Ports the server-detail half of the same upstream commit: parsed JSON fields
// are shown, but never the prefix or the rest of the raw response.
func TestReportDriveFailureUsesParsedDetail(t *testing.T) {
	h := newHarness(t)
	setStreams(h)
	h.putSession(t, "s1")
	reportDriveFailure(h.deps, "s1", &colabapi.StatusError{
		Code: 403, Body: "rendered\nprefix\n{\"error\":\"quota exceeded\",\"token\":\"LEAK-ME\"}",
		Method: "POST", Path: "/tun/m/credentials-propagation/ep",
	})
	out := h.stderr.String()
	if !strings.Contains(out, "Server said: quota exceeded") {
		t.Fatalf("parsed detail missing: %q", out)
	}
	for _, forbidden := range []string{"rendered", "prefix", "LEAK-ME"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("raw body leaked %q: %q", forbidden, out)
		}
	}
}

// --- issue #258: --wait-for-consent (polling, no stdin) ---------------------

// PollForConsent takes only a probe function: it has no stdin reader and no
// terminal handle, so a click-free caller cannot accidentally consume a line.
// These tests port the headless-consent cases from upstream issue #258.

// TestPollForConsentWaitsForSuccess: a fake control plane that first reports
// Success:false and only later Success:true must keep polling until it flips.
func TestPollForConsentWaitsForSuccess(t *testing.T) {
	calls := 0
	probe := func(context.Context) (*colabapi.CredProp, error) {
		calls++
		return &colabapi.CredProp{Success: calls >= 3}, nil
	}
	cp, err := pollForConsent(context.Background(), 2*time.Second, 5*time.Millisecond, probe)
	if err != nil {
		t.Fatalf("pollForConsent: %v", err)
	}
	if cp == nil || !cp.Success {
		t.Fatalf("propagation never succeeded: %+v", cp)
	}
	if calls != 3 {
		t.Fatalf("probe calls = %d; want 3 (false -> false -> true)", calls)
	}
}

// TestPollForConsentTimeoutIsSanitized: an unapproved consent must end with a
// bounded DeadlineExceeded, and the resulting driveFailureMessage must stay
// actionable without echoing HTML or token material.
func TestPollForConsentTimeoutIsSanitized(t *testing.T) {
	probe := func(context.Context) (*colabapi.CredProp, error) {
		return &colabapi.CredProp{Success: false}, nil
	}
	_, err := pollForConsent(context.Background(), 40*time.Millisecond, 5*time.Millisecond, probe)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v; want context.DeadlineExceeded", err)
	}
	h := newHarness(t)
	setStreams(h)
	h.putSession(t, "s1")
	reportDriveFailure(h.deps, "s1", err)
	out := h.stderr.String()
	for _, want := range []string{"Drive credentials could not be propagated", "golab whoami", "different Google account"} {
		if !strings.Contains(out, want) {
			t.Fatalf("message missing %q: %q", want, out)
		}
	}
	for _, forbidden := range []string{"<html", "SECRET-TOKEN", "\x1b[", "Press Enter after you have granted access"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("message leaked %q: %q", forbidden, out)
		}
	}
}

// countingStdin counts Read calls but never yields a line. Any code path that
// waits for Enter must show up as a non-zero count; code paths that do not
// touch stdin stay at zero.
type countingStdin struct {
	mu    sync.Mutex
	reads int
}

func (c *countingStdin) Read([]byte) (int, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return 0, io.EOF
}

func (c *countingStdin) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

// runWithStdin is h.run with an injectable reader so stdin access is observable.
func runWithStdin(h *testHarness, in io.Reader, args ...string) int {
	h.stdout.Reset()
	h.stderr.Reset()
	ctx := WithDeps(context.Background(), h.deps)
	return execute(ctx, args, in, &h.stdout, &h.stderr)
}

type propResponse struct {
	success bool
	uri     string
}

// propagationScript scripts only the dry-run POST. The XSRF GET still goes to
// the real fake control plane, so token handling stays exercised.
type propagationScript struct {
	mu        sync.Mutex
	responses []propResponse
	calls     int
}

func (s *propagationScript) next() propResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	if i < len(s.responses) {
		return s.responses[i]
	}
	return propResponse{success: true}
}

func (s *propagationScript) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type scriptedPropagationTransport struct {
	base   http.RoundTripper
	script *propagationScript
}

func (t *scriptedPropagationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost &&
		strings.HasPrefix(req.URL.Path, "/tun/m/credentials-propagation/") &&
		req.URL.Query().Get("dryrun") == "true" {
		r := t.script.next()
		body := `{"success":true}`
		if !r.success {
			// json.Marshal (not %q) so control characters become valid \uXXXX
			// JSON escapes: the decoded URI then carries the raw bytes a real
			// control plane would send, exercising the terminal-sanitizer path.
			encoded, err := json.Marshal(map[string]any{
				"success":                   false,
				"unauthorized_redirect_uri": r.uri,
			})
			if err != nil {
				return nil, err
			}
			body = string(encoded)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
	return t.base.RoundTrip(req)
}

func installScriptedPropagation(h *testHarness, s *propagationScript) {
	h.deps.Auth = fakeAuth{client: &http.Client{
		Transport: &scriptedPropagationTransport{base: h.ctrl.Client().Transport, script: s},
	}}
}

func driveKernelScript(h *testHarness, id string) {
	reply := replyOK()
	reply.Delay = 700 * time.Millisecond
	h.kernel.Script(
		colabRequest("dfs_ephemeral", id),
		statusIdleAfter(500*time.Millisecond),
		reply,
	)
}

// TestDriveMountWaitForConsentSkipsStdin is the end-to-end form of the issue
// #258 headless case: the fake control plane reports Success:false on the
// initial dry run and Success:true on the first poll. The mount must finish
// with zero stdin reads and never print the interactive Enter prompt.
func TestDriveMountWaitForConsentSkipsStdin(t *testing.T) {
	t.Setenv("COLAB_ASSUME_YES", "")
	t.Setenv("COLAB_TEST_STDIN_TTY", "0")
	h := newHarness(t)
	h.putSession(t, "s1")
	installScriptedPropagation(h, &propagationScript{responses: []propResponse{
		{success: false, uri: "https://consent.example/auth"}, // initial dry run
		{success: true}, // first poll flips it
	}})
	driveKernelScript(h, "cm-258")

	stdin := &countingStdin{}
	code := runWithStdin(h, stdin, "drivemount", "-s", "s1", "--wait-for-consent")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	combined := h.stdout.String() + h.stderr.String()
	for _, want := range []string{"https://consent.example/auth", "Credentials propagated"} {
		if !strings.Contains(combined, want) {
			t.Fatalf("missing %q in %q", want, combined)
		}
	}
	if strings.Contains(combined, "Press Enter after you have granted access") {
		t.Fatalf("interactive prompt leaked into polling mode: %q", combined)
	}
	if got := stdin.count(); got != 0 {
		t.Fatalf("stdin reads = %d; want 0 (poll must not wait for Enter)", got)
	}
	if got := h.deps.Auth.(fakeAuth).client.Transport.(*scriptedPropagationTransport).script.count(); got < 2 {
		t.Fatalf("propagation calls = %d; want >= 2 (initial + poll)", got)
	}
	inputs := waitInputs(t, h, 1)
	if !strings.Contains(string(inputs[0].Value), `"type":"colab_reply"`) {
		t.Fatalf("colab_reply not delivered: %s", inputs[0].Value)
	}
}

// TestDriveMountYesSkipsStdin proves the global --yes also suppresses the
// Enter prompt, without enabling polling (it goes straight to Confirm).
func TestDriveMountYesSkipsStdin(t *testing.T) {
	t.Setenv("COLAB_ASSUME_YES", "")
	t.Setenv("COLAB_TEST_STDIN_TTY", "0")
	h := newHarness(t)
	h.putSession(t, "s1")
	h.ctrl.SetPropagationAuthorizeURL("https://consent.example/auth")
	driveKernelScript(h, "cm-yes")

	stdin := &countingStdin{}
	code := runWithStdin(h, stdin, "drivemount", "--yes", "-s", "s1")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	combined := h.stdout.String() + h.stderr.String()
	if !strings.Contains(combined, "Credentials propagated") {
		t.Fatalf("no success message: %q", combined)
	}
	if strings.Contains(combined, "Press Enter after you have granted access") {
		t.Fatalf("--yes leaked the interactive prompt: %q", combined)
	}
	if got := stdin.count(); got != 0 {
		t.Fatalf("stdin reads = %d; want 0 with --yes", got)
	}
}

// TestDriveMountDefaultStillPrompts guards byte-identical legacy behaviour:
// with neither flag the original "Press Enter" wait and stdin read remain.
func TestDriveMountDefaultStillPrompts(t *testing.T) {
	t.Setenv("COLAB_ASSUME_YES", "")
	t.Setenv("COLAB_TEST_STDIN_TTY", "0")
	h := newHarness(t)
	h.putSession(t, "s1")
	h.ctrl.SetPropagationAuthorizeURL("https://consent.example/auth")
	driveKernelScript(h, "cm-default")

	stdin := &countingStdin{}
	code := runWithStdin(h, stdin, "drivemount", "-s", "s1")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "Press Enter after you have granted access") {
		t.Fatalf("default flow lost its prompt: %q", h.stderr.String())
	}
	if got := stdin.count(); got < 1 {
		t.Fatalf("default flow read stdin %d times; want >= 1", got)
	}
}

// --- defect 1 (MEDIUM): server-controlled authorization URL ---------------

// The UnauthorizedRedirectURI is server-controlled. A URI carrying an OSC 52
// clipboard-write payload and a CSI erase-screen sequence must be sanitized
// before it reaches stderr *or* the history event, and non-HTTP(S) schemes must
// never be displayed or launched.
func TestDriveAuthURIIsSanitizedAndSchemeChecked(t *testing.T) {
	const evilURI = "https://consent.example/auth?next=\x1b]52;c;c2VjcmV0\x1b\\\x1b[2J"
	for _, tc := range []struct {
		name, uri, wantURI string
	}{
		{"osc52-and-csi", evilURI, "https://consent.example/auth?next="},
		{"javascript-scheme", "javascript:alert(1)", ""},
		{"file-scheme", "file:///etc/passwd", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := launchBrowserFunc
			var launched []string
			launchBrowserFunc = func(target string) bool {
				launched = append(launched, target)
				return true
			}
			t.Cleanup(func() { launchBrowserFunc = orig })

			h := newHarness(t)
			setStreams(h)
			h.putSession(t, "s1")
			installScriptedPropagation(h, &propagationScript{responses: []propResponse{
				{success: false, uri: tc.uri},
				{success: true},
			}})
			driveKernelScript(h, "cm-authuri")

			code := runWithStdin(h, &countingStdin{}, "drivemount", "-s", "s1", "--yes")
			if code != 0 {
				t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
			}
			out := h.stderr.String()
			for _, bad := range []string{"\x1b", "]52;", "[2J", "javascript:", "file:///etc/passwd"} {
				if strings.Contains(out, bad) {
					t.Fatalf("stderr leaked %q: %q", bad, out)
				}
			}
			if tc.wantURI != "" {
				if !strings.Contains(out, tc.wantURI) {
					t.Fatalf("sanitized URI missing from stderr: %q", out)
				}
				if len(launched) != 1 || launched[0] != tc.wantURI {
					t.Fatalf("launched = %q; want [%q]", launched, tc.wantURI)
				}
			} else if len(launched) != 0 {
				t.Fatalf("invalid scheme was launched: %q", launched)
			}

			l, err := history.NewAt(h.dir, "s1")
			if err != nil {
				t.Fatal(err)
			}
			events, err := l.Read()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range events {
				if e.EventType != history.EventDriveAuthNeeded {
					continue
				}
				found = true
				var payload map[string]any
				if err := json.Unmarshal(e.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				uri, _ := payload["uri"].(string)
				if uri != tc.wantURI {
					t.Fatalf("history uri = %q; want %q", uri, tc.wantURI)
				}
				for _, bad := range []string{"\x1b", "]52;", "[2J"} {
					if strings.Contains(uri, bad) {
						t.Fatalf("history leaked %q: %q", bad, uri)
					}
				}
			}
			if !found {
				t.Fatalf("no drive_auth_needed event: %+v", events)
			}
		})
	}
}

// --- defect 2 (MEDIUM): one transient probe error aborted consent polling ---

func TestPollForConsentRetriesTransientProbeErrors(t *testing.T) {
	calls := 0
	probe := func(context.Context) (*colabapi.CredProp, error) {
		calls++
		if calls == 1 {
			return nil, &colabapi.StatusError{
				Code: 503, Body: "<html>temporarily unavailable</html>",
				Method: "POST", Path: "/tun/m/credentials-propagation/ep",
			}
		}
		return &colabapi.CredProp{Success: true}, nil
	}
	cp, err := pollForConsent(context.Background(), 2*time.Second, 5*time.Millisecond, probe)
	if err != nil {
		t.Fatalf("pollForConsent: %v", err)
	}
	if cp == nil || !cp.Success {
		t.Fatalf("propagation = %+v; want success", cp)
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d; want 2 (5xx then success)", calls)
	}
}

func TestPollForConsentAllFailuresBoundedAndSanitized(t *testing.T) {
	probe := func(context.Context) (*colabapi.CredProp, error) {
		return nil, &colabapi.StatusError{
			Code: 503, Method: "POST", Path: "/tun/m/credentials-propagation/ep",
			Body: "\x1b]52;c;c2VjcmV0\x1b\\\x1b[2J<html>SECRET-TOKEN</html>",
		}
	}
	start := time.Now()
	cp, err := pollForConsent(context.Background(), 40*time.Millisecond, 5*time.Millisecond, probe)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("poll not bounded by the deadline: %v", elapsed)
	}
	if cp != nil {
		t.Fatalf("propagation = %+v; want nil", cp)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v; want context.DeadlineExceeded", err)
	}
	for _, bad := range []string{"\x1b", "]52;", "[2J", "SECRET-TOKEN", "<html"} {
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("error leaked %q: %q", bad, err.Error())
		}
	}
	h := newHarness(t)
	setStreams(h)
	h.putSession(t, "s1")
	reportDriveFailure(h.deps, "s1", err)
	out := h.stderr.String()
	for _, want := range []string{"Drive credentials could not be propagated", "golab whoami"} {
		if !strings.Contains(out, want) {
			t.Fatalf("message missing %q: %q", want, out)
		}
	}
	for _, bad := range []string{"\x1b", "]52;", "[2J", "SECRET-TOKEN", "<html"} {
		if strings.Contains(out, bad) {
			t.Fatalf("reported failure leaked %q: %q", bad, out)
		}
	}
}

// --- defect 3 (LOW): waitForEnter stranded a reader goroutine on cancel ----

// closableBlockingStdin blocks in Read until Close unblocks it, modelling an
// embedded caller's stdin that can strand the waitForEnter reader goroutine.
type closableBlockingStdin struct {
	entered   chan struct{}
	release   chan struct{}
	readDone  chan struct{}
	closeOnce sync.Once
	doneOnce  sync.Once
}

func newClosableBlockingStdin() *closableBlockingStdin {
	return &closableBlockingStdin{
		entered:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		readDone: make(chan struct{}),
	}
}

func (r *closableBlockingStdin) Read([]byte) (int, error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-r.release
	r.doneOnce.Do(func() { close(r.readDone) })
	return 0, io.EOF
}

func (r *closableBlockingStdin) Close() error {
	r.closeOnce.Do(func() { close(r.release) })
	return nil
}

func TestWaitForEnterCancellationUnblocksReader(t *testing.T) {
	t.Setenv("COLAB_TEST_STDIN_TTY", "0")
	h := newHarness(t)
	setStreams(h)
	stdin := newClosableBlockingStdin()
	h.deps.Stdin = stdin
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	returned := make(chan struct{})
	go func() {
		waitForEnter(ctx, h.deps)
		close(returned)
	}()
	select {
	case <-stdin.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reader never started")
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForEnter did not return after cancellation")
	}
	select {
	case <-stdin.readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reader goroutine is still blocked on ReadString")
	}
	if !strings.Contains(h.stderr.String(), "Press Enter after you have granted access") {
		t.Fatalf("prompt missing from stderr: %q", h.stderr.String())
	}
}

func TestWaitForEnterNonTTYEOFPreservesPrompt(t *testing.T) {
	t.Setenv("COLAB_TEST_STDIN_TTY", "0")
	h := newHarness(t)
	setStreams(h) // setStreams installs an EOF reader on stdin.
	returned := make(chan struct{})
	go func() {
		waitForEnter(context.Background(), h.deps)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForEnter blocked on EOF stdin")
	}
	if !strings.Contains(h.stderr.String(), "Press Enter after you have granted access") {
		t.Fatalf("prompt missing from stderr: %q", h.stderr.String())
	}
	if !strings.HasSuffix(h.stderr.String(), "\n") {
		t.Fatalf("line break after prompt missing: %q", h.stderr.String())
	}
}

// --- defect §3.13: typed prompt replies must never land in history ---------

// Regression for §3.13 (ports the credential-bearing half of upstream
// tests/test_automation.py::test_cli_auth): a password typed at a getpass/auth
// stdin prompt must reach the kernel verbatim, but the history event and every
// `golab log -o` export derived from it must carry only a redacted form.
func TestPromptReplyNeverLeaksToHistoryOrExport(t *testing.T) {
	const password = "letmein-password-314159"

	h := newHarness(t)
	h.putSession(t, "s1")
	// A stream first installs the active-exec state before the stdin prompt;
	// the delays keep the independent stdin writer from racing the socket close.
	streamStep := stream("stdout", "Password prompt incoming\n")
	prompt := inputRequest("Password: ")
	prompt.Delay = 50 * time.Millisecond
	idle := statusIdle()
	idle.Delay = 300 * time.Millisecond
	reply := replyOK()
	reply.Delay = 500 * time.Millisecond
	h.kernel.Script(streamStep, prompt, idle, reply)

	if code := h.run(t, password+"\n", "auth", "-s", "s1"); code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}

	// (a) The runtime reply to the kernel must carry the real value unchanged.
	inputs := waitInputs(t, h, 1)
	if !strings.Contains(string(inputs[0].Value), password) {
		t.Fatalf("kernel did not receive the real reply: %s", inputs[0].Value)
	}

	// (b) The plaintext history JSONL must not contain the typed password.
	hist, err := os.ReadFile(filepath.Join(h.dir, "s1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, password, string(hist))
	// The record must carry a redaction marker, not an empty drop or the literal.
	l, err := history.NewAt(h.dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	events, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.EventType != history.EventInputReply {
			continue
		}
		found = true
		var payload map[string]any
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		stored, _ := payload["value"].(string)
		if !strings.Contains(stored, "redact") {
			t.Fatalf("input_reply stored no redaction marker: %q", stored)
		}
	}
	if !found {
		t.Fatalf("no input_reply event recorded: %+v", events)
	}

	// (c) Every export path (the source of `golab log -o`) must be clean too.
	for _, ext := range []string{".jsonl", ".txt", ".ipynb", ".md"} {
		t.Run(ext, func(t *testing.T) {
			exportPath := filepath.Join(t.TempDir(), "export"+ext)
			if code := h.run(t, "", "log", "-s", "s1", "-o", exportPath); code != 0 {
				t.Fatalf("log exit=%d stderr=%q", code, h.stderr.String())
			}
			exported, err := os.ReadFile(exportPath)
			if err != nil {
				t.Fatal(err)
			}
			assertNoSecret(t, password, string(exported))
		})
	}
}
