package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// githubIssuer is the OIDC issuer embedded in Fulcio certificates issued to
// GitHub Actions workloads. It is pinned; no other issuer can satisfy the
// identity policy below.
const githubIssuer = "https://token.actions.githubusercontent.com"

// verifyChecksum compares the artifact SHA-256 against the (hex) digest from
// the signed release metadata.
func verifyChecksum(artifact []byte, checksum string) error {
	checksum = strings.TrimSpace(strings.ToLower(checksum))
	if checksum == "" {
		return ErrMissingChecksum
	}
	checksum = strings.TrimPrefix(checksum, "sha256:")
	got := fmt.Sprintf("%x", sha256.Sum256(artifact))
	if len(checksum) != 64 {
		return fmt.Errorf("%w: malformed digest", ErrBadRelease)
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(checksum)) != 1 {
		return &ChecksumError{Got: got, Want: checksum}
	}
	return nil
}

// verifyArtifact verifies a downloaded artifact: checksum first, then the
// mandatory cosign keyless signature against the pinned owner/repo identity.
func verifyArtifact(ctx context.Context, cfg Config, artifact []byte, rel *Release) error {
	if err := verifyChecksum(artifact, rel.Checksum); err != nil {
		return err
	}
	if strings.TrimSpace(rel.SignatureURL) == "" {
		return ErrMissingSignature
	}
	bundleBytes, err := getBytes(ctx, httpClient(cfg), rel.SignatureURL, maxBundleBytes)
	if err != nil {
		return fmt.Errorf("fetch signature bundle: %w", err)
	}
	return verifySignature(ctx, cfg, artifact, bundleBytes)
}

// verifySignature loads a Sigstore bundle produced by cosign and verifies it
// with sigstore-go against the Sigstore public good trusted root. Verification
// covers the Fulcio CA chain, the Rekor inclusion proof / signed entry
// timestamp, the signing time against the certificate validity window, the
// OIDC issuer and the release workflow identity, and finally the in-toto
// statement's SHA-256 subject digest against the artifact bytes.
//
// There is deliberately no "structural checks only" path: if the bundle, its
// verification material or the trusted root is missing or invalid, the update
// is refused with a typed error.
func verifySignature(ctx context.Context, cfg Config, artifact, bundleBytes []byte) error {
	if len(bytes.TrimSpace(bundleBytes)) == 0 {
		return ErrMissingSignature
	}
	entity, err := loadBundle(bundleBytes)
	if err != nil {
		return err
	}
	_, err = verifyBundle(ctx, cfg, artifact, entity)
	return err
}

// loadBundle parses a cosign JSON bundle and maps parse failures onto our
// typed errors so callers never have to inspect sigstore error strings.
func loadBundle(bundleBytes []byte) (*bundle.Bundle, error) {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleBytes); err != nil {
		switch {
		case errors.Is(err, bundle.ErrMissingEnvelope),
			errors.Is(err, bundle.ErrMissingVerificationMaterial):
			return nil, fmt.Errorf("%w: %w", ErrMissingSignature, err)
		default:
			return nil, fmt.Errorf("%w: parse bundle: %w", ErrInvalidSignature, err)
		}
	}
	return &b, nil
}

// verifyBundle performs the full keyless verification of one signed entity.
// It is the internal seam used by both the signature and SBOM verifiers and by
// the virtual-Sigstore tests.
func verifyBundle(ctx context.Context, cfg Config, artifact []byte, entity verify.SignedEntity) (*verify.VerificationResult, error) {
	sigContent, err := entity.SignatureContent()
	if err != nil || sigContent == nil || len(sigContent.Signature()) == 0 {
		return nil, ErrMissingSignature
	}
	verification, err := entity.VerificationContent()
	if err != nil || verification == nil || verification.Certificate() == nil {
		return nil, fmt.Errorf("%w: DSSE signing certificate is missing", ErrMissingSignature)
	}

	trustedMaterial, err := trustedMaterialLoader(ctx)
	if err != nil {
		return nil, err // already typed by the loader
	}
	if trustedMaterial == nil {
		return nil, fmt.Errorf("%w: no Sigstore trust anchors available", ErrTrustedRoot)
	}

	// Fulcio chain + SCT-free offline verification: the transparency log entry
	// (inclusion proof and SET) and the RFC3161 timestamp establish the time at
	// which the short-lived certificate is validated. Requiring an observer
	// timestamp also rejects bundles with no proof of logging.
	sev, err := verify.NewVerifier(trustedMaterial,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: configure verifier: %w", ErrInvalidSignature, err)
	}

	sanRegex := fmt.Sprintf(`^https://github\.com/%s/%s/\.github/workflows/release\.yml@`,
		regexp.QuoteMeta(cfg.Owner), regexp.QuoteMeta(cfg.Repo))
	certID, err := verify.NewShortCertificateIdentity(githubIssuer, "", "", sanRegex)
	if err != nil {
		return nil, fmt.Errorf("%w: build identity policy: %w", ErrIdentityMismatch, err)
	}

	digest := sha256.Sum256(artifact)
	result, err := sev.Verify(entity, verify.NewPolicy(
		verify.WithArtifactDigest("sha256", digest[:]),
		verify.WithCertificateIdentity(certID),
	))
	if err != nil {
		var mismatch *verify.ErrNoMatchingCertificateIdentity
		if errors.As(err, &mismatch) {
			return nil, fmt.Errorf("%w: %w", ErrIdentityMismatch, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	return result, nil
}
