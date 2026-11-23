package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
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
	// test_execute_stdin_and_streaming_together).
	h.kernel.Script(
		stream("stdout", "prompt incoming\n"),
		inputRequest("Enter code: "),
		statusIdle(),
		replyOK(),
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
	h.kernel.Script(
		colabRequest("dfs_ephemeral", "cm-42"),
		statusIdleAfter(500*time.Millisecond),
		replyOK(),
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
