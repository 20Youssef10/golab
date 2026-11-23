package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/auditsec"
	"github.com/20Youssef10/golab/internal/auth"
	"github.com/20Youssef10/golab/internal/platform"
)

// NewAuditCommand builds the local-only security audit. No request is issued
// and no credential value is printed.
func NewAuditCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Report credential storage, file modes and cached security state",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			configDir, err := platform.ConfigDir()
			if err != nil {
				return err
			}
			settingsPath, err := platform.SettingsPath()
			if err != nil {
				return err
			}
			cfg := auditsec.Config{
				ConfigDir:       configDir,
				SessionsPath:    d.Store.Path(),
				SettingsPath:    settingsPath,
				LegacyTokenPath: filepath.Join(configDir, auth.LegacyTokenFileName),
				OAuthTokenPath:  filepath.Join(configDir, auth.OAuthTokenFileName),
				HistoryDir:      d.HistoryDir,
				AuthProvider:    string(d.Config.AuthProvider),
				Owner:           d.Config.Owner,
				Repo:            d.Config.Repo,
			}
			if settings, err := d.SettingsStore.Load(); err == nil {
				cfg.Capabilities = auditsec.Capabilities{
					PublicAPISupported: settings.PublicAPISupported,
					TunnelAPISupported: settings.TunnelAPISupported,
				}
			}
			rep, err := auditsec.Audit(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if asJSON {
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(d.Stdout, string(b))
			} else {
				fmt.Fprintf(d.Stdout, "[audit] token store: %s (keyring probe: %s)\n",
					rep.TokenStore.Tier, rep.TokenStore.Probe)
				for _, p := range rep.Paths {
					fmt.Fprintf(d.Stdout, "[audit] path %s=%s mode=%s\n", p.Name, p.Path, p.Mode)
				}
				fmt.Fprintf(d.Stdout, "[audit] capabilities: public=%s tunnel=%s\n",
					rep.Capabilities.PublicAPI, rep.Capabilities.TunnelAPI)
				fmt.Fprintf(d.Stdout, "[audit] update verification: %s %s\n", rep.Update.Status, rep.Update.Detail)
				for _, f := range rep.Findings {
					line := fmt.Sprintf("[audit] severity=%s id=%s detail=%s", f.Severity, f.ID, f.Detail)
					if f.Remediation != "" {
						line += " remediation=" + strings.ReplaceAll(f.Remediation, "\n", " ")
					}
					fmt.Fprintln(d.Stdout, line)
				}
			}
			code := auditExitCode(rep.Findings)
			if code != 0 {
				return exitf(code, "")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the audit report as a single JSON document")
	return cmd
}

// RegisterAuditCommands adds `audit` to the root command tree.
func RegisterAuditCommands(root *cobra.Command) { root.AddCommand(NewAuditCommand()) }

func auditExitCode(findings []auditsec.Finding) int {
	code := 0
	for _, f := range findings {
		switch f.Severity {
		case "error":
			return 2
		case "warning":
			code = 1
		}
	}
	return code
}
