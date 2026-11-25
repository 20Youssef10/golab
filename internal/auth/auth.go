// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/platform"
)

// AuthProvider selects the control-plane credential strategy, mirroring
// upstream's AuthProvider enum used by the global --auth flag.
type AuthProvider string

const (
	AuthOAuth2          AuthProvider = "oauth2"
	AuthADC             AuthProvider = "adc"
	DefaultAuthProvider              = AuthOAuth2
)

// ErrUnknownProvider is returned by ParseAuthProvider/New for an unrecognised
// --auth value.
var ErrUnknownProvider = errors.New("unknown auth provider")

// ParseAuthProvider accepts either provider case-insensitively, as upstream's
// Typer option does (case_sensitive=False).
func ParseAuthProvider(s string) (AuthProvider, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(AuthOAuth2):
		return AuthOAuth2, nil
	case string(AuthADC):
		return AuthADC, nil
	default:
		return "", fmt.Errorf("%w: %q (want %q or %q)", ErrUnknownProvider, s, AuthOAuth2, AuthADC)
	}
}

func (p AuthProvider) String() string { return string(p) }

func (p AuthProvider) Valid() bool {
	_, err := ParseAuthProvider(string(p))
	return err == nil
}

// Set implements pflag.Value so *AuthProvider can be bound directly to
// --auth by the CLI layer.
func (p *AuthProvider) Set(s string) error {
	parsed, err := ParseAuthProvider(s)
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

// Type implements pflag.Value.
func (p AuthProvider) Type() string { return "string" }

// Provider produces a fully authenticated control-plane HTTP client.
// An empty scopes slice means Scopes.
type Provider interface {
	HTTPClient(ctx context.Context, scopes []string) (*http.Client, error)
}

// KeyringBackend abstracts the OS keyring so tests can use an in-memory one.
type KeyringBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, value string) error
	Delete(service, user string) error
}

// TokenStore persists the opaque refresh-token blob. Load returns
// os.ErrNotExist when there is nothing stored.
type TokenStore interface {
	Load() ([]byte, error)
	Save([]byte) error
	Delete() error
}

// Config carries every dependency of the auth package. All writers are
// injectable so tests never block on a real TTY or touch the real keyring.
type Config struct {
	Provider AuthProvider // empty means AuthOAuth2

	// ScopeMode selects the requested OAuth scope set (--scopes). Empty means
	// ScopeModeFull. See scopes.go: minimal cannot mount Drive (drive.file).
	ScopeMode ScopeMode

	// ClientOAuthConfigPath is the -c/--client-oauth-config path. Empty means
	// ~/.colab-cli-oauth-config.json, falling back to the bundled config.
	ClientOAuthConfigPath string
	// ConfigDir is the directory for the 0600 fallback token file and the
	// legacy Python token.json. Empty means platform.ConfigDir().
	ConfigDir string

	Stdin  io.Reader // pasted authorisation code
	Stdout io.Writer // "Enter the authorization code:" prompt
	Stderr io.Writer // auth URL and one-line diagnostics ([colab] …)
	Logger *slog.Logger

	// TokenStore overrides the whole tiered store (useful for integration
	// tests); Keyring overrides just the keyring backend.
	TokenStore TokenStore
	Keyring    KeyringBackend

	ProbeTimeout time.Duration // keyring availability probe; default 5s
	TokenTimeout time.Duration // one OAuth token HTTP exchange; default 15s
	ADCTimeout   time.Duration // ADC discovery + first token; default 10s
	HTTPTimeout  time.Duration // per-request client timeout; default 60s
}

// Constant identifiers shared with the CLI and with users' existing Python
// installations (docs/02 §1.1, ADR-006).
const (
	RemoteRedirectURI   = "https://sdk.cloud.google.com/applicationdefaultauthcode.html"
	KeyringService      = "google-colab-cli"
	KeyringUser         = "oauth_token"
	KeyringProbeTimeout = 5 * time.Second
	LegacyTokenFileName = "token.json"
	OAuthTokenFileName  = "oauth_token.json"
)

// ErrADCUnavailable means no usable Application Default Credentials were
// found; the message carries the scoped gcloud command to fix it.
var ErrADCUnavailable = errors.New("no valid default credentials")

// ErrADCMissingScope means ADC credentials were found but their scoped token
// lacks the colaboratory scope required by /tun/m/*.
var ErrADCMissingScope = errors.New("ADC credentials missing the colaboratory scope")

// ErrADCTimeout means ADC discovery exceeded Config.ADCTimeout.
var ErrADCTimeout = errors.New("ADC discovery timed out")

// ErrNoClientConfig means neither -c nor the bundled OAuth client config is
// usable.
var ErrNoClientConfig = errors.New("client OAuth config not found")

// ErrNoAuthCode means the user pasted an empty authorisation code.
var ErrNoAuthCode = errors.New("no authorization code entered")

// ErrLegacyToken means the migrated Python token.json was unusable.
var ErrLegacyToken = errors.New("invalid legacy token file")

// ErrInsecureTokenURI means a token URI other than the pinned Google endpoint
// was configured (loopback is permitted only in the test binary).
var ErrInsecureTokenURI = errors.New("insecure OAuth token URI")

// ErrTokenExchange is the sentinel for TokenExchangeError.
var ErrTokenExchange = errors.New("OAuth token exchange failed")

// TokenExchangeError reports only an operation and an HTTP status. The token
// endpoint's response body is deliberately never included: Google may echo
// the pasted code or client_secret back in an error.
type TokenExchangeError struct {
	Operation  string
	StatusCode int
}

func (e *TokenExchangeError) Error() string {
	return fmt.Sprintf("oauth2: %s failed (HTTP %d)", e.Operation, e.StatusCode)
}

func (e *TokenExchangeError) Unwrap() error { return ErrTokenExchange }

// safeError carries an error for errors.Is/As while exposing only a redacted
// message. It prevents token-endpoint bodies from reaching any sink.
type safeError struct {
	msg   string
	cause error
}

func (e *safeError) Error() string { return e.msg }
func (e *safeError) Unwrap() error { return e.cause }

func safeWrap(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return &safeError{msg: prefix + ": " + logx.Redact(err.Error()), cause: err}
}

// New constructs a provider. It performs no network I/O and reads no token;
// all of that happens in Provider.HTTPClient.
func New(cfg Config) (Provider, error) {
	kind := cfg.Provider
	if kind == "" {
		kind = DefaultAuthProvider
	}
	kind, err := ParseAuthProvider(string(kind))
	if err != nil {
		return nil, err
	}
	if cfg.ScopeMode == "" {
		cfg.ScopeMode = DefaultScopeMode
	}
	if _, err := ParseScopeMode(string(cfg.ScopeMode)); err != nil {
		return nil, err
	}

	if cfg.ConfigDir == "" {
		cfg.ConfigDir, err = platform.ConfigDir()
		if err != nil {
			return nil, fmt.Errorf("auth: config dir: %w", err)
		}
	}
	if cfg.Stdin == nil {
		cfg.Stdin = os.Stdin
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.Logger == nil {
		cfg.Logger = logx.Logger()
	}
	cfg.ProbeTimeout = positiveDuration(cfg.ProbeTimeout, KeyringProbeTimeout)
	cfg.TokenTimeout = positiveDuration(cfg.TokenTimeout, 15*time.Second)
	cfg.ADCTimeout = positiveDuration(cfg.ADCTimeout, 10*time.Second)
	cfg.HTTPTimeout = positiveDuration(cfg.HTTPTimeout, 60*time.Second)

	switch kind {
	case AuthADC:
		return &adcProvider{
			stderr:      cfg.Stderr,
			logger:      cfg.Logger,
			timeout:     cfg.ADCTimeout,
			httpTimeout: cfg.HTTPTimeout,
			scopeMode:   cfg.ScopeMode,
			defaults:    defaultTokenSource,
		}, nil
	case AuthOAuth2:
		store := cfg.TokenStore
		if store == nil {
			kr := cfg.Keyring
			if kr == nil {
				kr = realKeyring{}
			}
			tokenPath := filepath.Join(cfg.ConfigDir, OAuthTokenFileName)
			store = &tieredStore{
				kr:      kr,
				file:    newFileStore(tokenPath),
				path:    tokenPath,
				stderr:  cfg.Stderr,
				logger:  cfg.Logger,
				timeout: cfg.ProbeTimeout,
			}
		}
		return &oauth2Provider{
			cfg:         cfg,
			store:       store,
			logger:      cfg.Logger,
			stderr:      cfg.Stderr,
			stdout:      cfg.Stdout,
			stdin:       bufio.NewReader(cfg.Stdin),
			tokenHTTP:   &http.Client{Transport: newBaseTransport(), Timeout: cfg.TokenTimeout},
			httpTimeout: cfg.HTTPTimeout,
			scopeMode:   cfg.ScopeMode,
		}, nil
	default:
		// Unreachable: ParseAuthProvider validated it above.
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, kind)
	}
}

func positiveDuration(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}

// newBaseTransport gives every HTTP call explicit connect/TLS/header/idle
// limits (AGENTS.md #5). No InsecureSkipVerify anywhere.
func newBaseTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// authenticatedClient uses oauth2.NewClient and then replaces the underlying
// transport with one that has explicit timeouts. The caller's ctx stays alive
// for the lifetime of the returned client.
func authenticatedClient(ctx context.Context, ts oauth2.TokenSource, timeout time.Duration) *http.Client {
	client := oauth2.NewClient(ctx, ts)
	if tr, ok := client.Transport.(*oauth2.Transport); ok {
		tr.Base = newBaseTransport()
	}
	client.Timeout = timeout
	return client
}
