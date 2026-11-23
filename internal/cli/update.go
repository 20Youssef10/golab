package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/update"
)

// persistUpdateCheck writes the fresh LastCheck (and cached LatestVersion)
// through the atomic settings store. It is a no-op when the check was throttled.
func persistUpdateCheck(d *app.Deps, rel *update.Release) {
	if d.SettingsStore == nil {
		return
	}
	settings, err := d.SettingsStore.Load()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	settings.LastCheck = &now
	if rel != nil && rel.Version != "" {
		settings.LatestVersion = rel.Version
	}
	_ = d.SettingsStore.Save(settings)
}

// applyUpdate reports the check result, persists throttle state and optionally
// performs the signed self-update. Kept separate from Check so the whole flow
// can be exercised against a local fake release server in tests.
func applyUpdate(ctx context.Context, d *app.Deps, cfg update.Config, rel *update.Release, due, doInstall bool) error {
	if due {
		persistUpdateCheck(d, rel)
	}
	if rel == nil {
		fmt.Fprintf(d.Stdout, "[colab] Colab CLI is up to date (version: %s).\n", cfg.CurrentVersion)
	} else {
		fmt.Fprintf(d.Stdout, "A new version is available: %s (current: %s).\n", rel.Version, cfg.CurrentVersion)
		fmt.Fprintf(d.Stdout, "Run 'golab update --install' to upgrade in place.\n")
	}
	if !doInstall || rel == nil {
		return nil
	}
	if err := update.VerifyAndInstall(ctx, cfg, rel); err != nil {
		return exitCausef(1, err, "[colab] Update verification failed: %s: %s",
			logx.Redact(err.Error()), strings.Join(updateErrorSentinels(err), ", "))
	}
	fmt.Fprintf(d.Stdout, "[colab] Installed version %s.\n", rel.Version)
	return nil
}

// updateErrorSentinels lists the typed reasons a verification can fail. It
// never inspects a status string and never includes a token.
func updateErrorSentinels(err error) []string {
	var out []string
	for _, tc := range []struct {
		sentinel error
		label    string
	}{
		{update.ErrChecksumMismatch, "checksum mismatch"},
		{update.ErrMissingChecksum, "missing checksum"},
		{update.ErrMissingSignature, "missing signature"},
		{update.ErrInvalidSignature, "invalid signature"},
		{update.ErrMissingSBOM, "missing SBOM attestation"},
		{update.ErrDowngrade, "downgrade refused"},
		{update.ErrIdentityMismatch, "signer identity mismatch"},
	} {
		if errors.Is(err, tc.sentinel) {
			out = append(out, tc.label)
		}
	}
	return out
}

// runUpdate performs the throttled release check and applies its result. The
// pinned source and current version come from app.UpdateConfig; only the
// verified LastCheck/LatestVersion state is written back.
func runUpdate(ctx context.Context, d *app.Deps, cfg update.Config, doInstall bool) error {
	rel, due, err := update.Check(ctx, cfg)
	if err != nil {
		return exitf(1, "[colab] Update check failed: %s", logx.Redact(err.Error()))
	}
	return applyUpdate(ctx, d, cfg, rel, due, doInstall)
}

func updateCommand() *cobra.Command {
	var install bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for a newer version (and optionally install it)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			cfg, err := d.UpdateConfig(cmd.Context())
			if err != nil {
				return err
			}
			return runUpdate(cmd.Context(), d, cfg, install)
		},
	}
	cmd.Flags().BoolVar(&install, "install", false, "Verify and install the newer release")
	return cmd
}

func registerUpdateCommand(root *cobra.Command) { root.AddCommand(updateCommand()) }
