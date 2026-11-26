package app

import (
	"os"
	"strings"

	"github.com/20Youssef10/golab/internal/state"
)

// SessionEnvVar is the environment override for the default session [tier5].
const SessionEnvVar = "GOLAB_SESSION"

// ConfiguredSessionName resolves a session name from, in order: an explicit
// value (the -s flag), the GOLAB_SESSION environment variable, and the
// settings.json default_session written by `golab use NAME` [tier5].
//
// The empty result means nothing was configured and the caller applies its
// unique-session fallback. Only a locked, local settings read is performed;
// no token material is ever involved. A corrupt settings file aborts instead
// of silently resetting the user's configured default.
func ConfiguredSessionName(explicit string, settings *state.SettingsStore) (string, error) {
	if v := strings.TrimSpace(explicit); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(os.Getenv(SessionEnvVar)); v != "" {
		return v, nil
	}
	if settings == nil {
		return "", nil
	}
	s, err := settings.Load()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s.DefaultSession), nil
}
