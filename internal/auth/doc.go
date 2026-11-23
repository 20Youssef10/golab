// Package auth implements the two control-plane credential providers used by
// the Colab CLI: Application Default Credentials ("adc") and the public OAuth2
// installed-app flow ("oauth2"), including its remote copy-paste consent
// variant and tiered refresh-token storage (OS keyring, then a 0600 file).
//
// It is a Go port of google-colab-cli's src/colab_cli/auth.py (upstream HEAD,
// 0.8.x). Portions derived from google-colab-cli, © Google LLC, Apache-2.0;
// modified. The bundled oauth_config.json is upstream's Google cloud-SDK
// installed client; its identity and redirect URI belong to Google (R-02).
//
// The package never imports internal/cli, internal/app or internal/state, and
// performs no network I/O at construction time. All credentials exposed by
// this package are logx.Secret values; no token, code or PKCE verifier is ever
// formatted into an error, log line or command line.
package auth
