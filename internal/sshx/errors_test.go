package sshx

import (
	"strings"
	"testing"
)

// Ports upstream tests/test_ssh.py::test_explain_handshake_failure_mapping and
// tests/test_ssh_wire_contract.py::test_real_status_mapping_via_loopback.
func TestExplainHandshakeFailureMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"missing-pubkey", 400, "missing pubkey", "missing pubkey header"},
		{"unsupported-key", 400, "unsupported key type", "unsupported key type"},
		{"bad-pubkey", 400, "invalid pubkey: bad base64", "Re-check your key"},
		{"token", 401, "", "token is invalid"},
		{"forbidden", 403, "", "Forbidden"},
		{"no-endpoint", 404, "", "Endpoint not found"},
		{"already-active", 429, `{"error":"already-active-session"}`, "Already-active SSH"},
		{"bad-gateway", 502, "sshd unreachable", "Bad gateway"},
		{"other-status", 503, "", "WebSocket upgrade rejected (HTTP 503)"},
		{"no-status", 0, "", "WebSocket upgrade failed without an HTTP status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExplainHandshakeFailure(tc.status, []byte(tc.body))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("ExplainHandshakeFailure(%d, %q) = %q; want substring %q", tc.status, tc.body, got, tc.want)
			}
			if tc.status == 400 && tc.body == "unsupported key type" {
				if !strings.Contains(got, "ssh-keygen -t ed25519") {
					t.Fatalf("missing keygen hint: %q", got)
				}
			}
		})
	}
}

// Ports upstream tests/test_ssh.py::test_explain_handshake_failure_session_limit.
func TestSessionLimitAndMessage(t *testing.T) {
	for _, limit := range []int{1, 2, 4} {
		body := []byte(`{"error":"already-active-session","limit":` + string(rune('0'+limit)) + `}`)
		got, ok := SessionLimit(body)
		if !ok || got != limit {
			t.Fatalf("SessionLimit(%q) = %d,%v; want %d,true", body, got, ok, limit)
		}
		msg := ExplainHandshakeFailure(429, body)
		want := "SSH session limit reached (HTTP 429): " + string(rune('0'+limit)) + " of " + string(rune('0'+limit)) + " SSH sessions in use"
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
	}
}

// Ports upstream tests/test_ssh.py::test_explain_handshake_failure_429_without_usable_limit.
func TestSessionLimitRejectsUnusableBodies(t *testing.T) {
	bodies := []string{
		`{"error":"already-active-session"}`,
		`{"error":"already-active-session","limit":true}`,
		`{"error":"already-active-session","limit":3.0}`,
		`{"error":"already-active-session","limit":"3"}`,
		`{"error":"already-active-session","limit":null}`,
		`{"error":"already-active-session","limit":0}`,
		`{"error":"already-active-session","limit":-1}`,
		`{"error":"already-active-session","limit":6}`,
		`{"error":"rate-limited","limit":2}`,
		`{"limit":2}`,
		`[{"error":"already-active-session","limit":2}]`,
		`<html><body>Too Many Requests</body></html>`,
		"",
		"[" + strings.Repeat("[", 1000),
	}
	fallback := ExplainHandshakeFailure(429, []byte(`{"error":"already-active-session"}`))
	for _, body := range bodies {
		if got, ok := SessionLimit([]byte(body)); ok {
			t.Errorf("SessionLimit(%q) unexpectedly accepted %d", body, got)
		}
		if got := ExplainHandshakeFailure(429, []byte(body)); got != fallback {
			t.Errorf("ExplainHandshakeFailure(429, %q) = %q; want fallback %q", body, got, fallback)
		}
	}
}

// Ports upstream tests/test_ssh.py::test_explain_handshake_failure_session_limit_past_200_char_snippet.
func TestSessionLimitParsedBeyondSnippet(t *testing.T) {
	body := []byte(`{"error":"already-active-session","pad":"` + strings.Repeat("x", 200) + `","limit":2}`)
	if got := ExplainHandshakeFailure(429, body); !strings.Contains(got, "2 of 2 SSH sessions") {
		t.Fatalf("limit past 200-char snippet lost: %q", got)
	}
}

// HandshakeError is a typed error usable with errors.As.
func TestHandshakeErrorType(t *testing.T) {
	e := &HandshakeError{Status: 404, Body: []byte("nope"), Message: "Endpoint not found"}
	if e.Error() == "" {
		t.Fatal("empty error message")
	}
	if e.Status != 404 {
		t.Fatalf("status = %d", e.Status)
	}
}

// AUDIT-02 S-6 + cross-check: an upgrade failure body is attacker-influenced
// and may echo credentials. Redaction must happen inside the explainer and the
// typed error, not only at the call site.
func TestExplainHandshakeRedactsSecrets(t *testing.T) {
	canaries := []struct{ name, key, value string }{
		{"id_token", "id_token", "IDTOKCANARY7f3a99"},
		{"password", "password", "PASSWDCANARY7f3a99"},
		{"code_verifier", "code_verifier", "VERIFYCANARY7f3a99"},
	}
	bodies := []struct {
		name   string
		status int
		build  func(k, v string) string
	}{
		{"400 json snippet", 400, func(k, v string) string { return `invalid pubkey {"` + k + `":"` + v + `"}` }},
		{"503 query snippet", 503, func(k, v string) string { return `upstream said https://x/cb?` + k + `=` + v }},
		{"no-status json", 0, func(k, v string) string { return `dial failed {"` + k + `":"` + v + `"}` }},
		{"json body", 400, func(k, v string) string { return `{"` + k + `":"` + v + `"}` }},
		{"query body", 503, func(k, v string) string { return `https://x/cb?` + k + `=` + v }},
	}
	for _, b := range bodies {
		for _, c := range canaries {
			t.Run(b.name+"/"+c.name, func(t *testing.T) {
				body := []byte(b.build(c.key, c.value))
				msg := ExplainHandshakeFailure(b.status, body)
				if strings.Contains(msg, c.value) {
					t.Errorf("ExplainHandshakeFailure leaked %s: %q", c.name, msg)
				}
				e := &HandshakeError{Status: b.status, Body: body, Message: msg}
				if strings.Contains(e.Error(), c.value) {
					t.Errorf("HandshakeError.Error leaked %s: %q", c.name, e.Error())
				}
			})
		}
	}
}

// A server may echo the proxy token verbatim with no field name at all. The
// pure explainer cannot know it, so the call site removes the known token from
// both the message and the stored body before constructing the typed error.
func TestRedactHandshakeKnownToken(t *testing.T) {
	const token = "RAWPROXYCANARY7f3a99"
	for _, in := range []string{
		"invalid pubkey " + token,
		`{"id_token":"` + token + `"}`,
	} {
		got := redactHandshake(in, token)
		if strings.Contains(got, token) {
			t.Errorf("redactHandshake leaked the proxy token: %q", got)
		}
	}
}
