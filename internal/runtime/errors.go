package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

// Typed failure categories. Callers must use errors.Is/errors.As; status codes
// and error text are never matched as strings.
var (
	// ErrSessionLost means the runtime proxy rejected our credentials or the
	// session itself is gone (401/404). The caller should prune its session.
	ErrSessionLost = errors.New("session lost")
	// ErrKernelGone means a status dead/restarting ended the execution.
	ErrKernelGone = errors.New("kernel gone")
	// ErrTimeout means no relevant message arrived within the idle limit.
	ErrTimeout = errors.New("idle timeout")
	// ConnectionLost means the WebSocket reader ended unexpectedly.
	ConnectionLost = errors.New("connection was lost")
)

// SessionLostError carries the HTTP status that proved the session is gone.
// It never carries a token, URL with a token query, or response body.
type SessionLostError struct {
	Op         string
	StatusCode int
}

func (e *SessionLostError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("session lost during %s (HTTP %d)", e.Op, e.StatusCode)
	}
	return fmt.Sprintf("session lost during %s", e.Op)
}
func (e *SessionLostError) Is(target error) bool { return target == ErrSessionLost }
func (e *SessionLostError) Unwrap() error        { return ErrSessionLost }

// KernelGoneError names the terminal execution_state that ended an execution.
type KernelGoneError struct{ State string }

func (e *KernelGoneError) Error() string {
	return fmt.Sprintf("kernel %s before the code finished", e.State)
}
func (e *KernelGoneError) Is(target error) bool { return target == ErrKernelGone }
func (e *KernelGoneError) Unwrap() error        { return ErrKernelGone }

// TimeoutError reports which phase of the execute loop went quiet.
type TimeoutError struct {
	WaitingFor string
	Idle       time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf(
		"Timeout waiting for %s: no message from the kernel for %gs. Use --timeout to allow longer silences.",
		e.WaitingFor, e.Idle.Seconds(),
	)
}
func (e *TimeoutError) Is(target error) bool { return target == ErrTimeout }
func (e *TimeoutError) Unwrap() error        { return ErrTimeout }

// HTTPStatusError is a non-2xx kernel REST response that is not a lost session.
type HTTPStatusError struct {
	Op   string
	Code int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("kernel %s failed (HTTP %d)", e.Op, e.Code)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// redactedError keeps the original cause (so errors.Is/As still work) while
// presenting a token-free message. HTTP client errors can embed the full URL,
// and the WebSocket URL carries the proxy token as a query parameter.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

func safeError(err error, token string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "REDACTED")
	}
	msg = logx.Redact(msg)
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, cause: err}
}
