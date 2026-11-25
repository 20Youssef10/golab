package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/asn1"
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

// oidSCTList is the X.509 extension that carries embedded Signed Certificate
// Timestamps (RFC 6962 §3.3.1 / RFC 9162).
var oidSCTList = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11129, 2, 4, 2}

// certHasSCT is a structural presence check only. The cryptographic SCT
// verification is always delegated to sigstore-go's
// verify.WithSignedCertificateTimestamps option, never faked here.
func certHasSCT(cert *x509.Certificate) bool {
	if cert == nil {
		return false
	}
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oidSCTList) && len(ext.Value) > 0 {
			return true
		}
	}
	return false
}

// sctRequired reports whether the SCT verifier option must be enabled.
// Production (nil) always requires one. The documented test-CA opt-out only
// relaxes the requirement when the bundle has no SCT at all: a present SCT is
// still verified, so the opt-out can never turn a real SCT check off.
func sctRequired(cfg Config, cert *x509.Certificate) bool {
	if cfg.RequireSCT == nil || *cfg.RequireSCT {
		return true
	}
	return certHasSCT(cert)
}

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
// covers the Fulcio CA chain and its Signed Certificate Timestamp, the Rekor
// inclusion proof / signed entry timestamp, the signing time against the
// certificate validity window, the OIDC issuer and the release workflow
// identity, and finally the in-toto statement's SHA-256 subject digest against
// the artifact bytes. SCT verification is required by default (see
// Config.RequireSCT).
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

	// Fulcio chain + offline verification: the transparency log entry
	// (inclusion proof and SET) and the RFC3161 timestamp establish the time
	// at which the short-lived certificate is validated. Requiring an observer
	// timestamp also rejects bundles with no proof of logging.
	opts := []verify.VerifierOption{
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	}
	// AUDIT-02 S-8: verify the Fulcio SCT with the trusted CT log key. The
	// library performs the actual RFC 9162 check; we only decide whether to
	// enable it. Production always requires it; the test-CA opt-out applies
	// only to SCT-less bundles and never skips a present SCT.
	if sctRequired(cfg, verification.Certificate()) {
		opts = append(opts, verify.WithSignedCertificateTimestamps(1))
	}
	sev, err := verify.NewVerifier(trustedMaterial, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: configure verifier: %w", ErrInvalidSignature, err)
	}

	// AUDIT gap 2.7 (docs/gap-report.md §2.7): pin the ref as well as the
	// workflow path. sigstore-go matches this pattern unanchored at the end, so
	// a bare ".yml@" accepts a certificate minted by *any* run of release.yml
	// (branch push, workflow_dispatch, ...), not just an official tag release.
	// The regex deliberately stops at "refs/tags/v" rather than ending in "$":
	// legitimate SANs may carry a version and trailing segments after it.
	sanRegex := fmt.Sprintf(`^https://github\.com/%s/%s/\.github/workflows/release\.yml@refs/tags/v`,
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
