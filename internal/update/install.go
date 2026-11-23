package update

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// VerifyAndInstall downloads, verifies and installs a release. Verification
// (checksum AND cosign keyless signature bound to the pinned identity) is
// mandatory and happens before any filesystem mutation. A downgrade and any
// missing verification material are rejected before any network request.
func VerifyAndInstall(ctx context.Context, cfg Config, rel *Release) error {
	if rel == nil || rel.Version == "" || rel.URL == "" {
		return ErrBadRelease
	}
	if err := verifyNotDowngrade(cfg.CurrentVersion, rel.Version); err != nil {
		return err
	}
	if strings.TrimSpace(rel.Checksum) == "" {
		return ErrMissingChecksum
	}
	if strings.TrimSpace(rel.SignatureURL) == "" {
		return ErrMissingSignature
	}
	if strings.TrimSpace(rel.SBOMURL) == "" {
		return ErrMissingSBOM
	}

	artifact, err := getBytes(ctx, httpClient(cfg), rel.URL, maxArtifact)
	if err != nil {
		return err
	}
	if err := verifyArtifact(ctx, cfg, artifact, rel); err != nil {
		return err
	}
	sbomBundle, err := getBytes(ctx, httpClient(cfg), rel.SBOMURL, maxBundleBytes)
	if err != nil {
		return fmt.Errorf("fetch SBOM attestation: %w", err)
	}
	if err := verifySBOM(ctx, cfg, artifact, sbomBundle); err != nil {
		return err
	}

	target, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate running executable: %w", err)
	}
	if err := replaceBinary(target, artifact, runtime.GOOS, 0o755); err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	return nil
}

// replaceBinary installs data at target. On Windows a running .exe cannot be
// overwritten, so it is renamed to .old first; the new binary takes its place
// and the .old file is removed on the next start by CleanupOld. The currently
// running executable is never deleted. On Unix an atomic rename over the target
// is sufficient and preserves the running process.
func replaceBinary(target string, data []byte, goos string, mode fs.FileMode) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".golab-update-*")
	if err != nil {
		return fmt.Errorf("create update temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the temp file has been renamed

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write update temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod update temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close update temp file: %w", err)
	}

	if goos == "windows" {
		old := target + ".old"
		// Remove a stale .old from a previous update first. This is never the
		// running executable, so it is safe to delete.
		if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale backup: %w", err)
		}
		if err := os.Rename(target, old); err != nil {
			return fmt.Errorf("rename running executable to backup: %w", err)
		}
		if err := os.Rename(tmpName, target); err != nil {
			// Roll back so the installation is not left without an executable.
			_ = os.Rename(old, target)
			return fmt.Errorf("install new executable: %w", err)
		}
		return nil
	}

	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("install new executable: %w", err)
	}
	return nil
}

// CleanupOld removes the .old backup left by a Windows self-update. The CLI
// composition root calls this at startup. It is idempotent and never removes
// the currently installed executable.
func CleanupOld(target string) error {
	err := os.Remove(target + ".old")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
