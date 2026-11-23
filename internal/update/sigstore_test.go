package update

// Ports the security checks required by docs/08 §3 ("checksum and cosign
// identity verification") and ADR-014 to sigstore-go's real keyless verifier.
// Every bundle below is produced by sigstore-go's virtual Sigstore: a Fulcio
// CA chain, a Rekor entry with an inclusion proof and SET and an RFC3161
// timestamp. Its test CA cannot emit SCTs, so fakeConfig opts out of that one
// check; production still requires and verifies an SCT. No test contacts TUF,
// Rekor, Fulcio or GitHub.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
)

func TestVerifySignatureAcceptsRealBundle(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()

	if err := verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, "")); err != nil {
		t.Fatalf("valid cosign bundle rejected: %v", err)
	}
}

func TestVerifySignatureRejectsUntrustedFulcioChain(t *testing.T) {
	artifact := []byte("fake binary contents")
	other, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	// Signed by a different Fulcio CA: the leaf must not chain to our roots.
	bundle := makeBundleSignedBy(t, other, makeStatement(t, artifact, bundleOptions{}), bundleOptions{})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()

	err = verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for untrusted Fulcio chain, got %v", err)
	}
}

type rekorSwappedMaterial struct {
	root.TrustedMaterial
	logs map[string]*root.TransparencyLog
}

func (m rekorSwappedMaterial) RekorLogs() map[string]*root.TransparencyLog { return m.logs }

func TestVerifySignatureRejectsUntrustedRekorProof(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true})
	other, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	// Same Fulcio/TSA/CT trust, but the Rekor log key is from another instance:
	// neither the inclusion proof nor the SET can be validated.
	withTrustedMaterial(t, rekorSwappedMaterial{TrustedMaterial: testVS, logs: other.RekorLogs()})

	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()
	err = verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for untrusted Rekor key, got %v", err)
	}
}

func TestVerifySignatureRejectsCertificateTimeOutsideValidity(t *testing.T) {
	artifact := []byte("fake binary contents")
	// The integrated (observer) timestamp predates the Fulcio certificate's
	// NotBefore, so path validation at the signature time must fail.
	bundle := makeBundle(t, artifact, bundleOptions{
		goodSig:        true,
		integratedTime: time.Now().Add(-10 * time.Hour),
	})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()

	err := verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for out-of-validity certificate, got %v", err)
	}
}

func TestVerifySignatureAcceptsSignedEntryTimestamp(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true, withSET: true})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()

	if err := verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, "")); err != nil {
		t.Fatalf("bundle with a valid SET rejected: %v", err)
	}
}

func TestVerifySignatureRejectsTamperedSignedEntryTimestamp(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true, withSET: true, tamperSET: true})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()

	err := verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for a tampered SET, got %v", err)
	}
}

func TestVerifySignatureRejectsMissingTlog(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true, noTlog: true})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()

	err := verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for missing Rekor entry, got %v", err)
	}
}

func TestVerifySignatureRejectsMissingCertificate(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true, noCert: true})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()

	err := verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrMissingSignature) {
		t.Fatalf("expected ErrMissingSignature for absent signing certificate, got %v", err)
	}
}

func TestVerifySignatureRejectsNonStatement(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true, noStatement: true})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()

	err := verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for non-in-toto payload, got %v", err)
	}
}

func TestVerifySignatureRejectsWrongWorkflow(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{
		goodSig: true,
		subject: "https://github.com/" + testOwner + "/" + testRepo + "/.github/workflows/other.yml@refs/tags/" + testTag,
	})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()

	err := verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch for a different workflow, got %v", err)
	}
}

func TestVerifySBOM(t *testing.T) {
	artifact := []byte("fake binary contents")

	t.Run("valid cyclonedx attestation", func(t *testing.T) {
		bundle := makeSBOMBundle(t, artifact, sbomPredicateCycloneDX)
		if err := verifySBOM(context.Background(), fakeConfig(nil), artifact, bundle); err != nil {
			t.Fatalf("valid SBOM attestation rejected: %v", err)
		}
	})

	t.Run("valid spdx attestation", func(t *testing.T) {
		bundle := makeSBOMBundle(t, artifact, sbomPredicateSPDX)
		if err := verifySBOM(context.Background(), fakeConfig(nil), artifact, bundle); err != nil {
			t.Fatalf("valid SPDX attestation rejected: %v", err)
		}
	})

	t.Run("digest does not bind to artifact", func(t *testing.T) {
		bundle := makeSBOMBundle(t, []byte("different binary"), sbomPredicateCycloneDX)
		err := verifySBOM(context.Background(), fakeConfig(nil), artifact, bundle)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("expected ErrInvalidSignature for mismatched SBOM subject, got %v", err)
		}
	})

	t.Run("predicate is not an SBOM", func(t *testing.T) {
		bundle := makeSBOMBundle(t, artifact, "https://slsa.dev/provenance/v1")
		err := verifySBOM(context.Background(), fakeConfig(nil), artifact, bundle)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("expected ErrInvalidSignature for non-SBOM predicate, got %v", err)
		}
	})

	t.Run("signer identity mismatch", func(t *testing.T) {
		sum := fmt.Sprintf("%x", sha256.Sum256(artifact))
		statement := []byte(`{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"` + sbomPredicateCycloneDX + `","subject":[{"name":"` + assetName + `","digest":{"sha256":"` + sum + `"}}]}`)
		bundle := makeBundleFromStatement(t, statement, bundleOptions{
			goodSig: true,
			subject: "https://github.com/attacker/evil/.github/workflows/release.yml@refs/tags/v1.2.3",
		})
		err := verifySBOM(context.Background(), fakeConfig(nil), artifact, bundle)
		if !errors.Is(err, ErrIdentityMismatch) {
			t.Fatalf("expected ErrIdentityMismatch for SBOM signer, got %v", err)
		}
	})
}

func TestVerifyAndInstallRequiresSBOM(t *testing.T) {
	artifact := []byte("fake binary contents")
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact})
	defer s.Close()
	rel := releaseFromFixture(t, artifact, "")
	rel.SBOMURL = ""

	err := VerifyAndInstall(context.Background(), fakeConfig(s), rel)
	if !errors.Is(err, ErrMissingSBOM) {
		t.Fatalf("expected ErrMissingSBOM, got %v", err)
	}
}
