// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/20Youssef10/golab/internal/logx"
)

func defaultTokenSource(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
	return google.DefaultTokenSource(ctx, scopes...)
}

const adcScopesHint = "openid," + CloudPlatformScope + "," + EmailScope + "," + CollaboratoryScope

// adcHint mirrors upstream's actionable error: user credentials minted with
// `gcloud auth application-default login` ignore requested scopes, so the
// required set has to be passed at login time.
func adcHint() string {
	return "No valid default credentials found. To authenticate, run:\n\n" +
		"  gcloud auth application-default login \\\n" +
		"      --scopes=" + adcScopesHint + "\n"
}

type adcProvider struct {
	stderr      io.Writer
	logger      *slog.Logger
	timeout     time.Duration
	httpTimeout time.Duration
	scopeMode   ScopeMode
	// defaults is replaced by in-process fakes in tests; production uses
	// google.DefaultTokenSource (the standard ADC discovery chain).
	defaults func(context.Context, ...string) (oauth2.TokenSource, error)
}

// HTTPClient obtains ADC credentials under a hard wall-clock bound. Discovery
// itself may block in D-Bus/cloud SDK code that has no context support, so it
// runs in a helper goroutine and the caller returns after p.timeout at most.
func (p *adcProvider) HTTPClient(ctx context.Context, scopes []string) (*http.Client, error) {
	if len(scopes) == 0 {
		// --scopes drives the default set; an explicit caller argument still
		// wins (as in TestADCUsesCallerScopes).
		scopes = p.scopeMode.Scopes()
	}
	type result struct {
		ts  oauth2.TokenSource
		tok *oauth2.Token
		err error
	}
	ch := make(chan result, 1)
	go func() {
		ts, err := p.defaults(ctx, scopes...)
		if err != nil {
			ch <- result{err: err}
			return
		}
		tok, err := ts.Token()
		if err != nil {
			ch <- result{ts: ts, err: err}
			return
		}
		ch <- result{ts: ts, tok: tok}
	}()

	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, p.fail(r.err, false)
		}
		if missingColaboratory(r.tok) {
			return nil, p.missingScopeError()
		}
		return authenticatedClient(ctx, r.ts, p.httpTimeout), nil
	case <-timer.C:
		return nil, p.fail(errors.New("ADC discovery exceeded its deadline"), true)
	}
}

func (p *adcProvider) fail(cause error, timedOut bool) error {
	if p.logger != nil {
		p.logger.Debug("adc: credential discovery failed", "error", logx.Redact(cause.Error()))
	}
	hint := adcHint()
	fmt.Fprint(p.stderr, hint+"\n")
	if timedOut {
		return fmt.Errorf("%w after %s\n%s", ErrADCTimeout, p.timeout, hint)
	}
	return fmt.Errorf("%w\n%s", ErrADCUnavailable, hint)
}

func (p *adcProvider) missingScopeError() error {
	msg := "ADC credentials are missing the required 'colaboratory' scope; control-plane " +
		"requests to /tun/m/* will be rejected with 401/403.\n" +
		"Re-mint user credentials with:\n\n" +
		"  gcloud auth application-default login \\\n" +
		"      --scopes=" + adcScopesHint + "\n"
	fmt.Fprint(p.stderr, msg+"\n")
	if p.logger != nil {
		p.logger.Warn("adc: credentials lack colaboratory scope")
	}
	return fmt.Errorf("%w: %s", ErrADCMissingScope, msg)
}

// missingColaboratory checks the `scope` claim of a scoped ADC token. Some
// credential types do not report one; in that case we cannot judge and let
// the control-plane surface a 401/403 instead of guessing.
func missingColaboratory(tok *oauth2.Token) bool {
	if tok == nil {
		return false
	}
	scope, _ := tok.Extra("scope").(string)
	if strings.TrimSpace(scope) == "" {
		return false
	}
	for _, s := range strings.Fields(scope) {
		if s == CollaboratoryScope {
			return false
		}
	}
	return true
}
