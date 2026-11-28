// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// ClearTokenResult reports which local credential stores held data before
// they were cleared. It never contains a token or any other secret.
type ClearTokenResult struct {
	Removed bool // any credential was present
	Keyring bool // OS keyring held an oauth_token entry
	File    bool // the 0600 fallback token file held data
	Legacy  bool // Python's legacy token.json was present
}

// TokenClearer is implemented by providers that hold local OAuth credentials.
// The CLI holds only an auth.Provider, so it type-asserts to this interface
// before invoking ClearToken (ADC has nothing to clear).
type TokenClearer interface {
	ClearToken(ctx context.Context) (ClearTokenResult, error)
}

// ClearToken removes locally cached OAuth2 credentials. It is the package-level
// convenience for callers that construct an auth.Config directly; CLI callers
// use the TokenClearer assertion on their configured Provider instead.
//
// It is deliberately offline: it does NOT call Google's /revoke endpoint (the
// bundled client id is shared with the Google Cloud SDK) and never prints or
// returns a token.
func ClearToken(ctx context.Context, cfg Config) (ClearTokenResult, error) {
	if cfg.Provider == AuthADC {
		// ADC credentials belong to gcloud, not to this token store.
		return ClearTokenResult{}, nil
	}
	p, err := New(cfg)
	if err != nil {
		return ClearTokenResult{}, err
	}
	clearer, ok := p.(TokenClearer)
	if !ok {
		return ClearTokenResult{}, errors.New("auth: configured provider has no local token to clear")
	}
	return clearer.ClearToken(ctx)
}

// ClearToken removes the OAuth2 token from every local tier: Python's legacy
// token.json, the 0600 fallback file and the OS keyring entry
// (google-colab-cli / oauth_token). All tiers are attempted even when one of
// them fails. The call is idempotent and emits nothing on any writer.
func (p *oauth2Provider) ClearToken(ctx context.Context) (ClearTokenResult, error) {
	if err := ctx.Err(); err != nil {
		return ClearTokenResult{}, err
	}
	var res ClearTokenResult
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Legacy Python file(s), including the literal ~/.config/colab-cli path on
	// platforms whose native config dir is elsewhere (e.g. Windows). A named
	// account never touches them: they belong to the default account.
	if p.clearsLegacyToken() {
		for _, legacy := range legacyTokenPaths(p.cfg.ConfigDir) {
			removed, err := removeLegacyToken(legacy)
			record(err)
			if removed {
				res.Legacy = true
				res.Removed = true
			}
		}
	}

	// Current store: both tiers are attempted even if the availability probe
	// failed or one tier errors (upstream PR #266's "attempt both stores"
	// requirement). Every keyring call is bounded by the probe timeout.
	if ts, ok := p.store.(*tieredStore); ok {
		ts.probe()
		v, getErr := withStringTimeout(ts.timeout, func() (string, error) {
			return ts.kr.Get(KeyringService, ts.keyringUser())
		})
		if getErr == nil && v != "" {
			res.Keyring, res.Removed = true, true
		}
		if data, err := ts.file.Load(); err == nil && len(data) > 0 {
			res.File, res.Removed = true, true
		}
		err := withErrorTimeout(ts.timeout, func() error {
			return ts.kr.Delete(KeyringService, ts.keyringUser())
		})
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			record(err)
		}
		if err := ts.file.Delete(); err != nil && !errors.Is(err, os.ErrNotExist) {
			record(err)
		}
	} else {
		// An injected/in-memory store: Load is the only way to know whether it
		// held anything. Never claim removal for an opaque empty store.
		if raw, err := p.store.Load(); err == nil && len(raw) > 0 {
			res.Removed = true
		}
		if err := p.store.Delete(); err != nil && !errors.Is(err, os.ErrNotExist) {
			record(err)
		}
	}
	return res, firstErr
}

// clearsLegacyToken reports whether this provider owns the legacy single-user
// credential files. Only the default account does.
func (p *oauth2Provider) clearsLegacyToken() bool {
	return p.cfg.KeyringUser == "" || p.cfg.KeyringUser == KeyringUser
}

// legacyTokenPaths returns the configured legacy path plus the canonical
// ~/.config/colab-cli/token.json when it is a different file.
func legacyTokenPaths(configDir string) []string {
	paths := []string{filepath.Join(configDir, LegacyTokenFileName)}
	if home, err := os.UserHomeDir(); err == nil {
		canonical := filepath.Join(home, ".config", "colab-cli", LegacyTokenFileName)
		different := filepath.Clean(canonical) != filepath.Clean(paths[0])
		if different {
			paths = append(paths, canonical)
		}
	}
	return paths
}

func removeLegacyToken(path string) (bool, error) {
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("auth: remove legacy token: %w", err)
	}
	return true, nil
}
