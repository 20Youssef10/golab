package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/20Youssef10/golab/internal/auth"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
)

// exitError is the typed carrier for a command's exit status. A message may be
// empty when the status alone carries the meaning (e.g. `run` mirroring a
// script's sys.exit(N)).
type exitError struct {
	code  int
	msg   string
	cause error
}

func (e *exitError) Error() string { return e.msg }

// Unwrap keeps errors.Is/errors.As working through the CLI's exit-code
// carrier, so callers can still identify typed causes such as a failed update
// verification.
func (e *exitError) Unwrap() error { return e.cause }

func exitf(code int, format string, args ...any) error {
	return &exitError{code: code, msg: fmt.Sprintf(format, args...)}
}

// exitCausef attaches a typed cause while mapping an error to an exit code.
func exitCausef(code int, cause error, format string, args ...any) error {
	return &exitError{code: code, msg: fmt.Sprintf(format, args...), cause: cause}
}

// usagef reports a bad invocation (missing prerequisite, invalid flag value).
func usagef(format string, args ...any) error { return exitf(2, format, args...) }

// sessionNotFoundError retains the session name while remaining compatible
// with errors.Is(err, state.ErrNotFound).
type sessionNotFoundError struct{ name string }

func (e *sessionNotFoundError) Error() string        { return fmt.Sprintf("session %q not found", e.name) }
func (e *sessionNotFoundError) Is(target error) bool { return target == state.ErrNotFound }

// sessionErr attaches the resolved name to a missing-session error.
func sessionErr(name string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, state.ErrNotFound) {
		return &sessionNotFoundError{name: name}
	}
	return err
}

// mapError is the single error→message/exit-code table (docs/03 §5). It never
// string-matches a status code and always redacts free-form messages.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee
	}
	var missing *sessionNotFoundError
	if errors.As(err, &missing) {
		return exitf(1, "[colab] Session '%s' not found.", missing.name)
	}
	switch {
	case errors.Is(err, colabapi.ErrTooManyAssignments):
		return exitf(1, "[colab] Allocation refused (precondition failed). This can mean "+
			"too many active sessions, or a temporary usage or capacity limit for "+
			"the requested runtime. Run `colab stop` to free up a session, wait "+
			"and retry, or try a different accelerator.")
	case errors.Is(err, colabapi.ErrNoQuota):
		return exitf(1, "[colab] %s", logx.Redact(err.Error()))
	case errors.Is(err, colabapi.ErrCapacity):
		// A stable reason code keeps this actionable in text and structured
		// output; both modes print this same message on stderr.
		return exitf(1, "[colab] %s: Colab has no spare capacity for the requested runtime. "+
			"Wait a few minutes and retry, or try a different runtime or accelerator. "+
			"Run `colab sessions` first if an allocation may have succeeded anyway.",
			colabapi.ReasonOf(err))
	case errors.Is(err, runtime.ErrSessionLost):
		return exitf(1, "[colab] Session appears to be lost (404/401). Run `colab sessions` to check its state.")
	case errors.Is(err, runtime.ErrTimeout):
		return exitf(1, "[colab] %s", logx.Redact(err.Error()))
	case errors.Is(err, auth.ErrADCUnavailable), errors.Is(err, auth.ErrADCMissingScope),
		errors.Is(err, auth.ErrADCTimeout), errors.Is(err, auth.ErrNoAuthCode),
		errors.Is(err, auth.ErrNoClientConfig), errors.Is(err, auth.ErrUnknownProvider):
		// Missing credentials are a missing prerequisite, not a usage error in
		// the flag sense; upstream exits 1. Keep the actionable advice intact.
		return exitf(1, "[colab] %s", logx.Redact(err.Error()))
	case errors.Is(err, context.Canceled):
		return exitf(130, "")
	case errors.Is(err, state.ErrCorrupt):
		return exitf(1, "[colab] Corrupt state file: %s", logx.Redact(err.Error()))
	default:
		return exitf(1, "[colab] %s", logx.Redact(err.Error()))
	}
}

// flagError marks cobra/pflag parse errors as usage errors (exit 2).
func flagError(err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: 2, msg: strings.TrimSpace(err.Error())}
}
