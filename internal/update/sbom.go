package update

import (
	"context"
	"fmt"
	"strings"
)

// in-toto predicate types used by cosign's SBOM attestations. A release must
// carry a signed attestation with one of these predicate types; provenance or
// arbitrary attestations do not satisfy the SBOM requirement.
const (
	sbomPredicateCycloneDX = "https://cyclonedx.org/schema/bom-1.5"
	sbomPredicateSPDX      = "https://spdx.dev/Document"
)

// isSBOMPredicateType accepts the CycloneDX and SPDX predicate families.
func isSBOMPredicateType(predicateType string) bool {
	switch {
	case predicateType == "https://cyclonedx.org/schema/bom",
		strings.HasPrefix(predicateType, "https://cyclonedx.org/schema/bom-"):
		return true
	case predicateType == sbomPredicateSPDX,
		strings.HasPrefix(predicateType, "https://spdx.org/"):
		return true
	default:
		return false
	}
}

// verifySBOM verifies a signed SBOM attestation from the same release and the
// same pinned identity as the checksum signature. It uses the same sigstore-go
// verifier (Fulcio chain, Rekor proof/SET, signing time, issuer, workflow
// identity) and additionally requires the in-toto subject digest to bind to
// the exact artifact bytes and the predicate to be a CycloneDX or SPDX SBOM.
func verifySBOM(ctx context.Context, cfg Config, artifact, sbomBundleBytes []byte) error {
	if len(strings.TrimSpace(string(sbomBundleBytes))) == 0 {
		return ErrMissingSBOM
	}
	entity, err := loadBundle(sbomBundleBytes)
	if err != nil {
		return err
	}
	result, err := verifyBundle(ctx, cfg, artifact, entity)
	if err != nil {
		return err
	}
	if result.Statement == nil || !isSBOMPredicateType(result.Statement.PredicateType) {
		return fmt.Errorf("%w: SBOM attestation has an unsupported predicate type", ErrInvalidSignature)
	}
	return nil
}
