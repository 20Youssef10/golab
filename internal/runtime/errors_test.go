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

// Cross-check (AUDIT-02 S-6): field-named OAuth codes and passwords can appear
// in HTTP error bodies; safeError must scrub them even when no proxy token is
// supplied, while keeping the typed cause intact.
func TestSafeErrorRedactsOAuthAndPasswordFields(t *testing.T) {
	msg := `request failed: {"id_token":"IDTOKC7f3a","password":"PASSWC7f3a",` +
		`"code_verifier":"VERIFYC7f3a"} at https://x/cb?code=AUTHCODEC7f3a&state=ok`
	canaries := []string{"IDTOKC7f3a", "PASSWC7f3a", "VERIFYC7f3a", "AUTHCODEC7f3a"}
	err := safeError(errors.New(msg), "")
	for _, c := range canaries {
		if strings.Contains(err.Error(), c) {
			t.Errorf("safeError leaked %q: %q", c, err.Error())
		}
	}
	if !strings.Contains(err.Error(), "request failed") {
		t.Errorf("redaction destroyed the diagnostic: %q", err.Error())
	}
	// Typed errors from this package never interpolate remote strings.
	if got := (&HTTPStatusError{Op: "inspect", Code: 401}).Error(); strings.Contains(got, "TOKEN") {
		t.Errorf("HTTPStatusError leaked: %q", got)
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
