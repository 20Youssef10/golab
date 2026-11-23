// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
//
// Tests in this file port the intent of upstream tests/test_auth_adc.py. The
// ADC discovery function is replaced with an in-process fake, so no real
// Google endpoint, gcloud subprocess or metadata server is ever consulted.
package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type fakeTokenSource struct {
	tok *oauth2.Token
	err error
}

func (f fakeTokenSource) Token() (*oauth2.Token, error) { return f.tok, f.err }

func adcTestConfig(t *testing.T, stderr io.Writer) Config {
	t.Helper()
	return Config{
		Provider:    AuthADC,
		ConfigDir:   t.TempDir(),
		Stdin:       strings.NewReader(""),
		Stdout:      io.Discard,
		Stderr:      stderr,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		ADCTimeout:  2 * time.Second,
		HTTPTimeout: 2 * time.Second,
	}
}

// Ports upstream test_get_credentials_adc_success and
// test_get_credentials_adc_requests_required_scopes: ADC must delegate to the
// discovery chain with the required scopes, then produce a bearer client.
func TestADCSuccessUsesScopesAndExplicitTimeouts(t *testing.T) {
	var requested []string
	res := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer adc-access" {
			t.Errorf("Authorization = %q", got)
		}
	}))
	defer res.Close()

	cfg := adcTestConfig(t, io.Discard)
	p := mustProvider(t, cfg).(*adcProvider)
	p.defaults = func(_ context.Context, scopes ...string) (oauth2.TokenSource, error) {
		requested = append([]string(nil), scopes...)
		return fakeTokenSource{tok: &oauth2.Token{
			AccessToken: "adc-access", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour),
		}}, nil
	}

	client, err := p.HTTPClient(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(requested, "|") != strings.Join(Scopes, "|") {
		t.Fatalf("ADC scopes = %v; want %v", requested, Scopes)
	}
	if tr, ok := client.Transport.(*oauth2.Transport); !ok {
		t.Fatalf("transport = %T, want *oauth2.Transport", client.Transport)
	} else if bt, ok := tr.Base.(*http.Transport); !ok {
		t.Fatalf("base transport = %T, want *http.Transport", bt)
	} else if bt.TLSHandshakeTimeout <= 0 || bt.ResponseHeaderTimeout <= 0 || bt.IdleConnTimeout <= 0 {
		t.Fatalf("transport timeouts not set explicitly: %+v", bt)
	}
	resp, err := client.Get(res.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

// Ports upstream test_get_credentials_adc_success with an explicit scope
// argument: the caller-provided set wins over the default.
func TestADCUsesCallerScopes(t *testing.T) {
	cfg := adcTestConfig(t, io.Discard)
	p := mustProvider(t, cfg).(*adcProvider)
	var requested []string
	p.defaults = func(_ context.Context, scopes ...string) (oauth2.TokenSource, error) {
		requested = append([]string(nil), scopes...)
		return fakeTokenSource{tok: &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}}, nil
	}
	_, err := p.HTTPClient(context.Background(), MinimalScopes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(requested, "|") != strings.Join(MinimalScopes, "|") {
		t.Fatalf("ADC scopes = %v; want %v", requested, MinimalScopes)
	}
}

// Ports upstream test_get_credentials_adc_default_error_prints_gcloud_and_exits
// (Go returns a typed error instead of calling os.Exit).
func TestADCNoCredentialsFriendlyError(t *testing.T) {
	stderr := &bytes.Buffer{}
	cfg := adcTestConfig(t, stderr)
	p := mustProvider(t, cfg).(*adcProvider)
	p.defaults = func(context.Context, ...string) (oauth2.TokenSource, error) {
		return nil, fmt.Errorf("could not find default credentials: %s", secretSentinel)
	}

	_, err := p.HTTPClient(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ADC error")
	}
	if !errors.Is(err, ErrADCUnavailable) {
		t.Fatalf("error = %v; want ErrADCUnavailable", err)
	}
	for _, needle := range []string{
		"No valid default credentials found", "gcloud auth application-default login",
		"openid", "https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email", CollaboratoryScope,
	} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("error missing %q: %s", needle, err.Error())
		}
		if !strings.Contains(stderr.String(), needle) {
			t.Errorf("stderr missing %q: %s", needle, stderr.String())
		}
	}
	if strings.Contains(err.Error(), secretSentinel) || strings.Contains(stderr.String(), secretSentinel) {
		t.Fatal("ADC error leaked the underlying secret")
	}
}

// Documents doc 02 §1.2: user credentials minted without the colaboratory
// scope receive 401/403 from /tun/m/*; we detect it from the token's scope
// claim and explain the scoped gcloud command.
func TestADCMissingColaboratoryScope(t *testing.T) {
	stderr := &bytes.Buffer{}
	cfg := adcTestConfig(t, stderr)
	p := mustProvider(t, cfg).(*adcProvider)
	p.defaults = func(context.Context, ...string) (oauth2.TokenSource, error) {
		tok := (&oauth2.Token{
			AccessToken: "scoped-access", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour),
		}).WithExtra(map[string]any{"scope": "openid https://www.googleapis.com/auth/userinfo.email"})
		return fakeTokenSource{tok: tok}, nil
	}

	_, err := p.HTTPClient(context.Background(), nil)
	if !errors.Is(err, ErrADCMissingScope) {
		t.Fatalf("error = %v; want ErrADCMissingScope", err)
	}
	if !strings.Contains(err.Error(), "colaboratory") || !strings.Contains(err.Error(), "gcloud") {
		t.Fatalf("unhelpful error: %s", err.Error())
	}
}

// A slow ADC discovery must be bounded by a hard timeout (AGENTS.md #5).
func TestADCHardTimeout(t *testing.T) {
	cfg := adcTestConfig(t, io.Discard)
	cfg.ADCTimeout = 30 * time.Millisecond
	p := mustProvider(t, cfg).(*adcProvider)
	p.defaults = func(ctx context.Context, _ ...string) (oauth2.TokenSource, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	_, err := p.HTTPClient(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ADC timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ADC timeout took %v", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("timeout error is not descriptive: %s", err.Error())
	}
}

// Ports test_get_credentials_adc_does_not_invoke_other_providers: the ADC path
// must not read an OAuth client config or prompt for a pasted code.
func TestADCDoesNotInvokeOAuthFlow(t *testing.T) {
	stdout := &bytes.Buffer{}
	cfg := adcTestConfig(t, io.Discard)
	cfg.Stdout = stdout
	cfg.ClientOAuthConfigPath = filepath.Join(t.TempDir(), "does-not-exist.json")
	p := mustProvider(t, cfg).(*adcProvider)
	p.defaults = func(context.Context, ...string) (oauth2.TokenSource, error) {
		return fakeTokenSource{tok: &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}}, nil
	}
	if _, err := p.HTTPClient(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("ADC prompted on stdout: %q", stdout.String())
	}
}
