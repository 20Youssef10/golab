package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSafeErrorRedactsTokenAndPreservesCause(t *testing.T) {
	// AGENTS.md security rule: a proxy token embedded in an HTTP client error
	// (the WS URL carries it as a query parameter) must never reach a log.
	token := "super-secret-proxy-token-1234"
	cause := fmt.Errorf(`dial "wss://host/channels?token=%s": %w`, token, context.DeadlineExceeded)
	err := safeError(cause, token)
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked: %q", err.Error())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("redaction must preserve errors.Is on the cause")
	}
	// Non-timeout errors are not retried, regardless of redaction.
	if isTimeout(safeError(errors.New("permanent failure"), token)) {
		t.Fatal("permanent error classified as a timeout")
	}
}

func TestTypedErrorSentinels(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want error
	}{
		{&SessionLostError{Op: "inspect kernel", StatusCode: 404}, ErrSessionLost},
		{&KernelGoneError{State: "dead"}, ErrKernelGone},
		{&TimeoutError{WaitingFor: "output", Idle: 30}, ErrTimeout},
		{ConnectionLost, ConnectionLost},
	} {
		if !errors.Is(tc.err, tc.want) {
			t.Fatalf("%T does not match %v", tc.err, tc.want)
		}
	}
}
