// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package auth

import (
	"errors"
	"fmt"
	"strings"
)

// Individual OAuth scopes, matching upstream PUBLIC_SCOPES in auth.py.
const (
	OpenIDScope        = "openid"
	ProfileScope       = "https://www.googleapis.com/auth/userinfo.profile"
	EmailScope         = "https://www.googleapis.com/auth/userinfo.email"
	CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
	CollaboratoryScope = "https://www.googleapis.com/auth/colaboratory"
	DriveFileScope     = "https://www.googleapis.com/auth/drive.file"
)

// Scopes is the six-scope set requested by upstream by default. Treat it as
// read-only; copy it before modifying (Provider.HTTPClient already does).
var Scopes = []string{
	OpenIDScope,
	ProfileScope,
	EmailScope,
	CloudPlatformScope,
	CollaboratoryScope,
	DriveFileScope,
}

// MinimalScopes is the candidate reduced scope set from DEC-10 / doc 08 §5
// (spike S5): it drops cloud-platform (very broad), drive.file (only needed
// for Drive mount) and userinfo.profile. It is an EXPERIMENT, not the default;
// Scopes remains the compatibility choice. gcloud requires openid,
// cloud-platform, userinfo.email and colaboratory for ADC user credentials,
// so this set is only meaningful for the OAuth2 provider. Keeping
// colaboratory is mandatory: without it /tun/m/* returns 401/403.
var MinimalScopes = []string{
	OpenIDScope,
	EmailScope,
	CollaboratoryScope,
}

// ScopeMode selects which of the two scope sets a sign-in requests. It backs
// the global --scopes flag (issue #71). full is the compatibility default: it
// matches upstream's six scopes and keeps `golab drivemount` working, because
// drive.file is needed only by that command. minimal drops drive.file (so a
// minimal grant cannot mount Drive) plus cloud-platform and userinfo.profile;
// callers in minimal mode must not attempt a Drive mount.
type ScopeMode string

const (
	ScopeModeFull    ScopeMode = "full"
	ScopeModeMinimal ScopeMode = "minimal"
	// DefaultScopeMode preserves the pre-flag behaviour for every caller that
	// does not pass --scopes.
	DefaultScopeMode = ScopeModeFull
)

// ErrUnknownScopeMode is returned for a --scopes value other than full/minimal.
var ErrUnknownScopeMode = errors.New("unknown scope mode")

// ParseScopeMode accepts either mode case-insensitively, like --auth.
func ParseScopeMode(s string) (ScopeMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(ScopeModeFull):
		return ScopeModeFull, nil
	case string(ScopeModeMinimal):
		return ScopeModeMinimal, nil
	default:
		return "", fmt.Errorf("%w: %q (want %q or %q)", ErrUnknownScopeMode, s, ScopeModeFull, ScopeModeMinimal)
	}
}

func (m ScopeMode) String() string { return string(m) }

func (m ScopeMode) Valid() bool {
	_, err := ParseScopeMode(string(m))
	return err == nil
}

// Set implements pflag.Value so --scopes can bind directly to a ScopeMode.
func (m *ScopeMode) Set(s string) error {
	parsed, err := ParseScopeMode(s)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// Type implements pflag.Value.
func (m ScopeMode) Type() string { return "string" }

// Scopes returns the concrete scope set for this mode. The zero value maps to
// the full set so callers that never set --scopes stay backward compatible.
func (m ScopeMode) Scopes() []string {
	if m == ScopeModeMinimal {
		return MinimalScopes
	}
	return Scopes
}
