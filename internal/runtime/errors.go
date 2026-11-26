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
	// ErrOutputGap means a socket drop may have swallowed output that was
	// published while no socket was attached. This port cannot replay those
	// messages (iopub has no ack/resend and REST has no output endpoint), so the
	// error is deliberately honest about what was and was not recovered.
	ErrOutputGap = errors.New("output gap")
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

// ConnectionLostError is surfaced only after a bounded reconnect budget has
// been exhausted. Transient drops are retried in place and never reach the
// caller. It keeps errors.Is(err, ConnectionLost) working for existing callers
// while exposing the attempt count and last dial cause via errors.As.
type ConnectionLostError struct {
	Attempts int
	Elapsed  time.Duration
	Cause    error
}

func (e *ConnectionLostError) Error() string {
	msg := fmt.Sprintf("kernel connection lost after %d reconnect attempts over %s",
		e.Attempts, e.Elapsed.Round(time.Millisecond))
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}
func (e *ConnectionLostError) Is(target error) bool { return target == ConnectionLost }
func (e *ConnectionLostError) Unwrap() error {
	// Keep the sentinel match via Is while preserving typed causes (e.g. a
	// redial timeout) for errors.Is/errors.As callers.
	if e.Cause != nil {
		return e.Cause
	}
	return ConnectionLost
}

// OutputGapError is returned when an execution ends without a terminal
// idle+reply after its channels socket dropped. Recovered holds exactly the
// outputs this process observed (deduplicated); anything the kernel emitted
// during the dead window is unrecoverable and is NOT invented here. Cause is
// the underlying transport error (e.g. a *ConnectionLostError) when there is
// one, kept typed for errors.Is/errors.As callers.
type OutputGapError struct {
	MsgID     string
	Recovered []Output
	Cause     error
}

func (e *OutputGapError) Error() string {
	msg := fmt.Sprintf("output may have been emitted while the kernel connection was down "+
		"(execution %s): recovered %d output(s); output published during the drop cannot "+
		"be replayed by this client. Work may still be running. Recover with: golab resume -s NAME",
		e.MsgID, len(e.Recovered))
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}
func (e *OutputGapError) Is(target error) bool { return target == ErrOutputGap }
func (e *OutputGapError) Unwrap() error {
	if e.Cause != nil {
		return e.Cause
	}
	return ErrOutputGap
}

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
