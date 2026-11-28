package account

import (
	"fmt"

	"github.com/20Youssef10/golab/internal/state"
)

// ValidateName applies the same allowlist as session names: a leading
// alphanumeric followed by at most 63 letters, digits, dots, underscores or
// hyphens, with Windows device names refused. It keeps account-derived file
// names free of path separators and traversal on every platform.
func ValidateName(name string) error {
	if err := state.ValidateName(name); err != nil {
		return fmt.Errorf("invalid account name %q: use letters, digits, '.', '_' or '-' (max 64 characters)", name)
	}
	return nil
}

// safeName returns the account name for derived storage names. Invalid names
// collapse to a fixed stem so no user string can introduce a separator or a
// traversal sequence into a path or a keyring user.
func safeName(name string) string {
	if name == "" || name == DefaultName {
		return DefaultName
	}
	if ValidateName(name) != nil {
		return "invalid"
	}
	return name
}

// KeyringUser is the OS keyring user for an account's refresh token. The
// default account keeps the Python CLI's legacy "oauth_token" user so existing
// installs stay signed in; named accounts use "oauth_token:<name>".
func KeyringUser(name string) string {
	if safeName(name) == DefaultName {
		return "oauth_token"
	}
	return "oauth_token:" + safeName(name)
}

// TokenFileName is the 0600 fallback token file for an account. The default
// account keeps the legacy oauth_token.json path.
func TokenFileName(name string) string {
	if safeName(name) == DefaultName {
		return "oauth_token.json"
	}
	return "oauth_token_" + safeName(name) + ".json"
}

// SessionsFileName is the isolated session store for an account. The default
// account keeps sessions.json so existing sessions are visible unchanged.
func SessionsFileName(name string) string {
	if safeName(name) == DefaultName {
		return "sessions.json"
	}
	return "sessions_" + safeName(name) + ".json"
}
