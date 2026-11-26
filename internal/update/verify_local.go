package update

// VerifyLocal validates a release artifact that already exists on disk. It is
// the CI gate behind `golab update --verify-only PATH`: no download, no install
// and no weaker verification path. Checksum, cosign keyless signature and the
// signed SBOM attestation must all bind to the exact artifact bytes and to the
// pinned release identity.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalVerifyOptions overrides the default sibling-file locations. Empty
// fields use checksums.txt and the two GoReleaser bundle suffixes.
type LocalVerifyOptions struct {
	ChecksumFile  string
	SignatureFile string
	SBOMFile      string
}

// LocalVerifyResult names the files that were actually verified.
type LocalVerifyResult struct {
	Artifact      string `json:"artifact"`
	SHA256        string `json:"sha256"`
	ChecksumFile  string `json:"checksum_file"`
	SignatureFile string `json:"signature_file"`
	SBOMFile      string `json:"sbom_file"`
}

// VerifyLocal performs the full verification chain for a local artifact.
func VerifyLocal(ctx context.Context, cfg Config, artifactPath string, opts LocalVerifyOptions) (LocalVerifyResult, error) {
	var out LocalVerifyResult
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if strings.TrimSpace(artifactPath) == "" {
		return out, ErrBadRelease
	}
	artifact, err := readBoundedFile(artifactPath, maxArtifact)
	if err != nil {
		return out, err
	}
	out.Artifact = artifactPath

	dir := filepath.Dir(artifactPath)
	checksumFile := opts.ChecksumFile
	if checksumFile == "" {
		checksumFile = filepath.Join(dir, "checksums.txt")
	}
	checksumDoc, err := readBoundedFile(checksumFile, maxChecksumDoc)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, ErrMissingChecksum
		}
		return out, err
	}
	out.ChecksumFile = checksumFile
	checksum, err := checksumForFile(checksumDoc, filepath.Base(artifactPath))
	if err != nil {
		return out, err
	}
	if err := verifyChecksum(artifact, checksum); err != nil {
		return out, err
	}
	out.SHA256 = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(checksum), "sha256:"))

	sigFile := opts.SignatureFile
	if sigFile == "" {
		sigFile = artifactPath + ".sigstore.json"
	}
	bundleBytes, err := readBoundedFile(sigFile, maxBundleBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, ErrMissingSignature
		}
		return out, err
	}
	out.SignatureFile = sigFile
	if err := verifySignature(ctx, cfg, artifact, bundleBytes); err != nil {
		return out, err
	}

	sbomFile := opts.SBOMFile
	if sbomFile == "" {
		sbomFile = artifactPath + ".sbom.sigstore.json"
	}
	sbomBytes, err := readBoundedFile(sbomFile, maxBundleBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, ErrMissingSBOM
		}
		return out, err
	}
	out.SBOMFile = sbomFile
	if err := verifySBOM(ctx, cfg, artifact, sbomBytes); err != nil {
		return out, err
	}
	return out, nil
}

func checksumForFile(doc []byte, name string) (string, error) {
	for _, line := range strings.Split(string(doc), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		hexSum := strings.TrimPrefix(strings.ToLower(fields[0]), "sha256:")
		if _, err := hex.DecodeString(hexSum); err != nil || len(hexSum) != 64 {
			return "", fmt.Errorf("%w: malformed digest for %s", ErrBadRelease, name)
		}
		return hexSum, nil
	}
	return "", fmt.Errorf("%w: no checksum for %s", ErrMissingChecksum, name)
}

func readBoundedFile(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %s", ErrTooLarge, p)
	}
	return b, nil
}
