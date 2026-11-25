package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/auth"
	"github.com/20Youssef10/golab/internal/logx"
)

// RegisterLogoutCommands is the orchestrator hook: call it from
// registerCommands (register.go is owned by another work package).
func RegisterLogoutCommands(root *cobra.Command) {
	root.AddCommand(logoutCommand())
}

// logoutCommand ports upstream PR #266. It clears local credentials only:
//
//   - OAuth2: keyring (google-colab-cli / oauth_token), the 0600 fallback and
//     Python's legacy token.json are removed via auth.TokenClearer.
//   - sessions.json: cached proxy tokens and expiry times are blanked so a
//     stale data-plane token cannot bypass re-authentication with -s NAME.
//   - settings.json: cached API capability probes are reset.
//
// It deliberately does NOT call Google's revoke endpoint (the bundled OAuth
// client is shared with the Google Cloud SDK) and does NOT stop any VM. Under
// --auth=adc it only prints gcloud's revocation guidance. No token is ever
// printed. The command is idempotent.
func logoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear local credentials to sign out or switch accounts",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := depsOf(cmd)
			if d.Config.AuthProvider == auth.AuthADC {
				fmt.Fprintln(d.Stderr, "[colab] Using --auth=adc. Run 'gcloud auth application-default revoke' to sign out.")
				return nil
			}

			clearer, ok := d.Auth.(auth.TokenClearer)
			if !ok {
				// Injectable non-OAuth providers (fakes) hold no local token.
				fmt.Fprintln(d.Stderr, "[colab] Already logged out.")
				return nil
			}
			res, err := clearer.ClearToken(cmd.Context())
			if err != nil {
				return exitf(1, "[colab] Failed to clear OAuth token (%s).", logx.Redact(err.Error()))
			}
			if res.Removed {
				fmt.Fprintln(d.Stderr, "[colab] Logged out.")
			} else {
				fmt.Fprintln(d.Stderr, "[colab] Already logged out.")
			}

			// Capability flags are only a cache; a best-effort reset is fine.
			settings, err := d.SettingsStore.Load()
			if err != nil {
				fmt.Fprintf(d.Stderr, "[colab] Warning: Could not read settings cache (%s).\n", logx.Redact(err.Error()))
			} else if settings.PublicAPISupported != nil || settings.TunnelAPISupported != nil {
				settings.PublicAPISupported = nil
				settings.TunnelAPISupported = nil
				if err := d.SettingsStore.Save(settings); err != nil {
					fmt.Fprintf(d.Stderr, "[colab] Warning: Could not reset settings cache (%s).\n", logx.Redact(err.Error()))
				}
			}

			sessions, err := d.Store.List()
			if err != nil {
				fmt.Fprintf(d.Stderr, "[colab] Warning: Could not update local session cache (%s).\n", logx.Redact(err.Error()))
				return exitf(1, "[colab] Failed to clear session proxy tokens: could not read the local session cache.")
			}
			// A blanked token that fails to persist is a security failure: exit
			// non-zero so callers do not carry a stale data-plane token forward.
			// Accumulate every failure instead of returning on the first one.
			failed := 0
			for name, s := range sessions {
				if s.Token.Reveal() == "" && s.TokenExpiresAt == nil {
					continue
				}
				s.Token = logx.NewSecret("")
				s.TokenExpiresAt = nil
				if err := d.Store.Put(s); err != nil {
					fmt.Fprintf(d.Stderr, "[colab] Warning: Could not clear token for session '%s' (%s).\n", name, logx.Redact(err.Error()))
					failed++
				}
			}
			if len(sessions) > 0 {
				fmt.Fprintf(d.Stderr, "[colab] Warning: %d local session(s) may still be running on the previous account.\n", len(sessions))
			}
			if failed > 0 {
				return exitf(1, "[colab] Failed to clear proxy tokens for %d local session(s); a stale token may remain on disk.", failed)
			}
			return nil
		},
	}
}
