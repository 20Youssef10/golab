// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/20Youssef10/golab/internal/logx"
)

// bundledOAuthConfig is upstream's cloud-SDK installed client (R-02: the
// remote redirect URI is registered to this client id; BYO clients must use
// their own registered redirect).
//
//go:embed oauth_config.json
var bundledOAuthConfig []byte

type clientConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
}

type clientConfigFile struct {
	Installed *clientConfigDetails `json:"installed"`
	Web       *clientConfigDetails `json:"web"`
}

type clientConfigDetails struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURI      string `json:"auth_uri"`
	TokenURI     string `json:"token_uri"`
}

// storedToken is the on-disk/keyring payload: refresh token plus only the
// metadata needed to mint access tokens. secrets are logx.Secret so an
// accidental fmt/slog of the struct renders REDACTED.
type storedToken struct {
	AccessToken  logx.Secret `json:"access_token,omitempty"`
	RefreshToken logx.Secret `json:"refresh_token,omitempty"`
	TokenType    string      `json:"token_type,omitempty"`
	Expiry       time.Time   `json:"expiry,omitempty"`
	Scopes       []string    `json:"scopes,omitempty"`
	ClientID     string      `json:"client_id,omitempty"`
	ClientSecret logx.Secret `json:"client_secret,omitempty"`
	TokenURI     string      `json:"token_uri,omitempty"`
}

// decodeStored also accepts Python google.oauth2.credentials' "token" field.
func decodeStored(data []byte) (storedToken, error) {
	var s storedToken
	if err := json.Unmarshal(data, &s); err != nil {
		return storedToken{}, err
	}
	var legacy struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &legacy); err == nil && s.AccessToken.Reveal() == "" && legacy.Token != "" {
		s.AccessToken = logx.NewSecret(legacy.Token)
	}
	return s, nil
}

func tokenFromStored(s storedToken) *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  s.AccessToken.Reveal(),
		RefreshToken: s.RefreshToken.Reveal(),
		TokenType:    s.TokenType,
		Expiry:       s.Expiry,
	}
}

type oauth2Provider struct {
	cfg         Config
	store       TokenStore
	logger      *slog.Logger
	stderr      io.Writer
	stdout      io.Writer
	stdin       *bufio.Reader
	tokenHTTP   *http.Client
	httpTimeout time.Duration
}

// loadClientConfig reads -c, then ~/.colab-cli-oauth-config.json, then the
// bundled cloud-SDK client (upstream test_get_credentials_fallback_config).
func (p *oauth2Provider) loadClientConfig() (clientConfig, error) {
	path := p.cfg.ClientOAuthConfigPath
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".colab-cli-oauth-config.json")
		}
	}
	var data []byte
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			data = b
		case errors.Is(err, os.ErrNotExist):
			// Fall through to the embedded config.
		default:
			return clientConfig{}, safeWrap("oauth2: reading client config", err)
		}
	}
	if data == nil {
		data = bundledOAuthConfig
	}
	var file clientConfigFile
	if err := json.Unmarshal(data, &file); err != nil {
		return clientConfig{}, safeWrap("oauth2: parsing client config", err)
	}
	details := file.Installed
	if details == nil {
		details = file.Web
	}
	if details == nil || details.ClientID == "" || details.TokenURI == "" {
		return clientConfig{}, fmt.Errorf("%w: no installed/web client_id and token_uri in %q", ErrNoClientConfig, path)
	}
	if !allowedTokenURI(details.TokenURI) {
		return clientConfig{}, fmt.Errorf("%w: token URI must be HTTPS (or loopback for local fakes)", ErrInsecureTokenURI)
	}
	authURI := details.AuthURI
	if authURI == "" {
		authURI = "https://accounts.google.com/o/oauth2/auth"
	}
	return clientConfig{
		ClientID:     details.ClientID,
		ClientSecret: details.ClientSecret,
		AuthURL:      authURI,
		TokenURL:     details.TokenURI,
	}, nil
}

// allowedTokenURI rejects plaintext HTTP except for loopback test servers.
func allowedTokenURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Scheme, "https") {
		return true
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

// HTTPClient resolves a cached token, refreshes it when needed (otherwise
// running the remote consent flow), and returns a bearer-authenticated client.
func (p *oauth2Provider) HTTPClient(ctx context.Context, scopes []string) (*http.Client, error) {
	if len(scopes) == 0 {
		scopes = Scopes
	}
	cc, err := p.loadClientConfig()
	if err != nil {
		return nil, err
	}
	conf := &oauth2.Config{
		ClientID:     cc.ClientID,
		ClientSecret: cc.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: cc.AuthURL, TokenURL: cc.TokenURL},
		RedirectURL:  RemoteRedirectURI,
		Scopes:       append([]string(nil), scopes...),
	}

	raw, loadErr := p.store.Load()
	var s storedToken
	if loadErr == nil {
		s, loadErr = decodeStored(raw)
		if loadErr != nil {
			p.notice("Could not read the cached OAuth token; signing in again.")
		}
	}
	if loadErr != nil {
		if imported, ierr := p.importLegacy(); ierr == nil && imported != nil {
			s = *imported
			loadErr = nil
		}
	}
	if loadErr == nil {
		// Python's token.json carries its own client metadata; honour it so a
		// legacy token can be refreshed without -c pointing at the same client.
		if s.ClientID != "" {
			conf.ClientID = s.ClientID
		}
		if s.ClientSecret.Reveal() != "" {
			conf.ClientSecret = s.ClientSecret.Reveal()
		}
		if s.TokenURI != "" && allowedTokenURI(s.TokenURI) {
			conf.Endpoint.TokenURL = s.TokenURI
		}
	}

	var tok *oauth2.Token
	var granted []string
	if loadErr == nil {
		tok = tokenFromStored(s)
		granted = append([]string(nil), s.Scopes...)
		if len(granted) == 0 {
			granted = append([]string(nil), scopes...)
		}
		if !tok.Valid() {
			if tok.RefreshToken == "" {
				tok = nil
			} else {
				refreshed, refreshedScopes, rerr := p.refresh(ctx, conf, tok, granted)
				if rerr != nil {
					p.notice("Cached OAuth token could not be refreshed; signing in again.")
					tok = nil
				} else {
					tok, granted = refreshed, refreshedScopes
					p.save(s, tok, granted, cc)
				}
			}
		}
	}
	if tok == nil || !tok.Valid() {
		tok, granted, err = p.runRemoteFlow(ctx, conf)
		if err != nil {
			return nil, err
		}
		p.save(s, tok, granted, cc)
	}

	conf.Scopes = granted
	return authenticatedClient(ctx, conf.TokenSource(ctx, tok), p.httpTimeout), nil
}

func (p *oauth2Provider) refresh(ctx context.Context, conf *oauth2.Config, tok *oauth2.Token, granted []string) (*oauth2.Token, []string, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {conf.ClientID},
		"scope":         {strings.Join(granted, " ")},
	}
	if conf.ClientSecret != "" {
		form.Set("client_secret", conf.ClientSecret)
	}
	refreshed, scope, err := p.postToken(ctx, conf.Endpoint.TokenURL, form, "refresh")
	if err != nil {
		return nil, nil, err
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = tok.RefreshToken
	}
	return refreshed, grantedScopes(scope, granted), nil
}

// runRemoteFlow implements the upstream copy-paste flow (no localhost server,
// no blocked OOB redirect). The URL goes to stderr, the prompt to stdout, and
// the code is read from injectable stdin. Neither code nor verifier is logged.
func (p *oauth2Provider) runRemoteFlow(ctx context.Context, conf *oauth2.Config) (*oauth2.Token, []string, error) {
	verifier, err := randomOpaque(32)
	if err != nil {
		return nil, nil, safeWrap("oauth2: generating PKCE verifier", err)
	}
	state, err := randomOpaque(16)
	if err != nil {
		return nil, nil, safeWrap("oauth2: generating state", err)
	}
	authURL := conf.AuthCodeURL(state,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.SetAuthURLParam("token_usage", "remote"),
		oauth2.AccessTypeOffline,
		oauth2.S256ChallengeOption(verifier),
	)
	fmt.Fprintln(p.stderr)
	fmt.Fprintln(p.stderr, "To authorize colab-go, visit this URL in any browser:")
	fmt.Fprintln(p.stderr)
	fmt.Fprintln(p.stderr, "  "+authURL)
	fmt.Fprintln(p.stderr)
	fmt.Fprintln(p.stderr, "After approving, Google will display an authorization code.")

	fmt.Fprint(p.stdout, "Enter the authorization code: ")
	line, readErr := p.stdin.ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, nil, safeWrap("oauth2: reading authorization code", readErr)
	}
	code := strings.TrimSpace(line)
	if code == "" {
		return nil, nil, ErrNoAuthCode
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {RemoteRedirectURI},
		"client_id":     {conf.ClientID},
		"code_verifier": {verifier},
	}
	if conf.ClientSecret != "" {
		form.Set("client_secret", conf.ClientSecret)
	}
	tok, scope, err := p.postToken(ctx, conf.Endpoint.TokenURL, form, "exchange")
	if err != nil {
		return nil, nil, err
	}
	return tok, grantedScopes(scope, conf.Scopes), nil
}

// postToken performs a bounded token request. Response bodies are never
// included in errors: an endpoint may echo the code or client_secret.
func (p *oauth2Provider) postToken(ctx context.Context, tokenURI string, form url.Values, operation string) (*oauth2.Token, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", safeWrap("oauth2: "+operation+" request", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.tokenHTTP.Do(req)
	if err != nil {
		return nil, "", safeWrap("oauth2: "+operation+" request failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", &TokenExchangeError{Operation: operation, StatusCode: resp.StatusCode}
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
		TokenType    string `json:"token_type"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, "", &TokenExchangeError{Operation: operation, StatusCode: resp.StatusCode}
	}
	if payload.AccessToken == "" {
		return nil, "", &TokenExchangeError{Operation: operation, StatusCode: resp.StatusCode}
	}
	tok := &oauth2.Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		TokenType:    payload.TokenType,
	}
	if tok.TokenType == "" {
		tok.TokenType = "Bearer"
	}
	if payload.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return tok, payload.Scope, nil
}

func grantedScopes(scope string, fallback []string) []string {
	if fields := strings.Fields(scope); len(fields) > 0 {
		return fields
	}
	return append([]string(nil), fallback...)
}

func (p *oauth2Provider) save(old storedToken, tok *oauth2.Token, granted []string, cc clientConfig) {
	refresh := tok.RefreshToken
	if refresh == "" {
		refresh = old.RefreshToken.Reveal()
	}
	out := storedToken{
		AccessToken:  logx.NewSecret(tok.AccessToken),
		RefreshToken: logx.NewSecret(refresh),
		TokenType:    tok.TokenType,
		Expiry:       tok.Expiry,
		Scopes:       append([]string(nil), granted...),
		ClientID:     cc.ClientID,
		ClientSecret: logx.NewSecret(cc.ClientSecret),
		TokenURI:     cc.TokenURL,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	if err := p.store.Save(data); err != nil {
		p.notice("Could not save the OAuth token; you may be asked to sign in again. Use --auth=adc to avoid token storage.")
	}
}

// importLegacy performs the one-time migration of Python's token.json. A
// corrupt file holds nothing worth keeping and is removed; a usable file is
// deleted only after a successful write to the new store.
func (p *oauth2Provider) importLegacy() (*storedToken, error) {
	path := filepath.Join(p.cfg.ConfigDir, LegacyTokenFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s, err := decodeStored(data)
	if err != nil || (s.AccessToken.Reveal() == "" && s.RefreshToken.Reveal() == "") {
		_ = os.Remove(path)
		return nil, ErrLegacyToken
	}
	canonical, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	if err := p.store.Save(canonical); err != nil {
		return &s, err // keep the only copy
	}
	_ = os.Remove(path)
	return &s, nil
}

func (p *oauth2Provider) notice(msg string) {
	fmt.Fprintln(p.stderr, "[colab] "+msg)
	if p.logger != nil {
		p.logger.Debug("oauth2: " + msg)
	}
}

func randomOpaque(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
