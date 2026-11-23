package colabapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/20Youssef10/golab/internal/logx"
)

// logx.Redact's JSON pattern only covers a subset of token field spellings;
// redact every quoted credential field ourselves before handing off to it.
var jsonTokenField = regexp.MustCompile(
	`(?i)("(?:token|access_token|refresh_token|client_secret|authorization|x-goog-colab-token|x-colab-runtime-proxy-token)"\s*:\s*)"[^"]*"`)

var (
	// ErrTooManyAssignments maps HTTP 412 on /assign (too many sessions or a
	// temporary capacity/usage limit). Not retryable.
	ErrTooManyAssignments = errors.New("too many Colab assignments")
	// ErrNoQuota maps HTTP 400 on /assign when an accelerator was requested.
	ErrNoQuota = errors.New("no quota or entitlement for the requested accelerator")
	// ErrUnexpectedResponse marks a structurally invalid control-plane reply.
	ErrUnexpectedResponse = errors.New("unexpected Colab control-plane response")
)

// StatusError is the typed non-2xx error. Body is redacted and length-capped.
type StatusError struct {
	Code   int
	Body   string
	Method string
	Path   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("colab %s %s: HTTP %d: %s", e.Method, e.Path, e.Code, e.Body)
}

func statusOf(err error) *StatusError {
	var se *StatusError
	if errors.As(err, &se) {
		return se
	}
	return nil
}

func retryableStatus(code int) bool {
	return code == 502 || code == 503 || code == 504
}

// isTimeout covers Go's url.Error/net.Error timeout (including a read timeout
// caused by our explicit HTTP client timeout) and context deadline expiry.
// Go does not expose a separate ReadTimeout type, so all network timeouts are
// treated as uncertain POSTs and reconciled before retrying.
func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func redactBody(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	s = strings.ReplaceAll(s, "\x00", "")
	// logx.Redact removes bearer tokens and JSON token fields from free-form text.
	s = jsonTokenField.ReplaceAllString(s, `${1}"REDACTED"`)
	s = logx.Redact(s)
	const max = 2048
	runes := []rune(s)
	if len(runes) > max {
		s = string(runes[:max]) + "…"
	}
	return s
}
