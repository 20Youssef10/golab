package sshx

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/20Youssef10/golab/internal/logx"
)

// ErrNoSSHKey means no usable key was found in the documented search order.
var ErrNoSSHKey = errors.New("no SSH key found")

// ErrUnsupportedKeyType means the key is not one of the types Colab accepts.
var ErrUnsupportedKeyType = errors.New("unsupported SSH key type")

// ErrInterrupted means a termination signal cancelled a command.
var ErrInterrupted = errors.New("interrupted")

// HandshakeError is the typed error for a failed WebSocket upgrade. Status is
// the HTTP status (0 for a network error without a status), Body the raw
// response body, and Message the upstream-actionable explanation.
type HandshakeError struct {
	Status  int
	Body    []byte
	Message string
}

func (e *HandshakeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message != "" {
		return logx.Redact(e.Message)
	}
	return fmt.Sprintf("WebSocket upgrade failed (HTTP %d)", e.Status)
}

// maxSessionLimit is the documented upper bound for already-active-session
// responses (upstream _MAX_SSH_SESSIONS).
const maxSessionLimit = 5

// SessionLimit parses the 429 already-active-session body. It accepts only an
// integer limit in [1, 5], exactly like upstream's _session_limit; booleans,
// floats, strings, null, out-of-range values and non-objects are rejected.
func SessionLimit(body []byte) (int, bool) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return 0, false
	}
	var name string
	if err := json.Unmarshal(top["error"], &name); err != nil || name != "already-active-session" {
		return 0, false
	}
	var limit int
	if err := json.Unmarshal(top["limit"], &limit); err != nil {
		return 0, false
	}
	if limit < 1 || limit > maxSessionLimit {
		return 0, false
	}
	return limit, true
}

// ExplainHandshakeFailure maps an upgrade status/body to the exact actionable
// upstream message (tests/test_ssh.py::test_explain_handshake_failure_mapping).
// Status numbers are matched as typed values, never as substrings of an error.
func ExplainHandshakeFailure(status int, body []byte) string {
	snippet := strings.TrimSpace(strings.ToValidUTF8(string(body), "\uFFFD"))
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	// The response body is untrusted and may echo credentials. Redact here as
	// well as at the call site so direct callers of this exported helper are
	// safe by default (AUDIT-02 S-6).
	snippet = logx.Redact(snippet)
	switch status {
	case 400:
		switch {
		case strings.Contains(snippet, "missing pubkey"):
			return "Server rejected request: missing pubkey header. This is " +
				"likely a CLI bug; please file a colab-cli issue."
		case strings.Contains(snippet, "unsupported key type"):
			return "Server rejected pubkey: unsupported key type. Accepted " +
				"key types: ssh-ed25519, ecdsa-sha2-nistp{256,384,521}. RSA " +
				"keys (ssh-rsa) are NOT accepted. Generate an Ed25519 key " +
				"with `ssh-keygen -t ed25519` and re-run (optionally with " +
				"--identity)."
		default:
			return fmt.Sprintf("Server rejected pubkey (HTTP 400): %s. "+
				"Re-check your key with `ssh-keygen -y -f <key>`.", snippet)
		}
	case 401:
		return "Authentication failed (HTTP 401): the runtime-proxy token " +
			"is invalid. The session may have expired - try `colab new`."
	case 403:
		return "Forbidden (HTTP 403): the server refused this request; the " +
			"runtime-proxy token may lack permission for this action. (A " +
			"runtime without SSH enabled returns 404, not 403.)"
	case 404:
		return "Endpoint not found (HTTP 404): this runtime does not expose " +
			"the /colab/ssh endpoint. SSH is enabled at runtime creation, " +
			"so an older or non-SSH runtime will not have it - run " +
			"`colab new` for a fresh runtime with SSH."
	case 429:
		if limit, ok := SessionLimit(body); ok {
			return fmt.Sprintf(
				"SSH session limit reached (HTTP 429): %d of %d SSH sessions "+
					"in use on this runtime. Close another `colab ssh` or IDE "+
					"connection to it and retry. If the previous client died, "+
					"retry with `--force`. The limit depends on your Colab plan.",
				limit, limit)
		}
		return "Already-active SSH session (HTTP 429): another `colab ssh` " +
			"is connected to this runtime. Disconnect it and retry. " +
			"If the previous client died, retry with `--force`."
	case 502:
		return "Bad gateway (HTTP 502): the runtime's local sshd is " +
			"unreachable. The runtime may be unhealthy; try `colab " +
			"status`, then `colab stop` + `colab new`."
	case 0:
		return "WebSocket upgrade failed without an HTTP status: " +
			snippet + ". Check your network."
	default:
		return fmt.Sprintf("WebSocket upgrade rejected (HTTP %d): %s", status, snippet)
	}
}

// redactHandshake removes field-named credentials and the known proxy token
// verbatim. A server can echo the raw token without any key prefix; only the
// dialling call site knows the token, so it supplies it here.
func redactHandshake(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "REDACTED")
	}
	return logx.Redact(s)
}

// KeyError is a user-facing key discovery/derivation failure.
type KeyError struct {
	Path   string
	Reason string
	Err    error
}

// Unwrap keeps errors.Is/errors.As working through discovery failures.
func (e *KeyError) Unwrap() error { return e.Err }

func (e *KeyError) Error() string {
	if e.Path == "" {
		return "[colab] " + e.Reason
	}
	return fmt.Sprintf("[colab] --identity %s: %s", e.Path, e.Reason)
}
