package update

// Ported from upstream tests/test_update.py resilience intent
// (test_pypi_fetch_failure_omits_latest) plus the harder security requirements
// in docs/08 §3 and ADR-014: checksum AND cosign keyless identity verification
// are mandatory, with no silent fallback to an unverified install.

import (
	"context"
	"errors"
	"testing"
)

func TestVerifyAndInstallWrongChecksum(t *testing.T) {
	s, _ := newReleaseServer(t, releaseFixture{badChecksum: true})
	defer s.Close()
	cfg := fakeConfig(s)

	err := VerifyAndInstall(context.Background(), cfg, releaseFromFixture(t, []byte("fake binary contents"), "0000000000000000000000000000000000000000000000000000000000000000"))
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
}

func TestVerifyAndInstallMissingChecksum(t *testing.T) {
	cfg := fakeConfig(nil)
	err := VerifyAndInstall(context.Background(), cfg, &Release{
		Version: testVer, Tag: testTag, URL: "https://example.invalid/asset",
	})
	if !errors.Is(err, ErrMissingChecksum) {
		t.Fatalf("expected ErrMissingChecksum, got %v", err)
	}
}

func TestVerifyAndInstallMissingSignature(t *testing.T) {
	s, _ := newReleaseServer(t, releaseFixture{})
	defer s.Close()
	cfg := fakeConfig(s)
	rel := releaseFromFixture(t, []byte("fake binary contents"), "")
	rel.SignatureURL = "" // deliberately missing

	err := VerifyAndInstall(context.Background(), cfg, rel)
	if !errors.Is(err, ErrMissingSignature) {
		t.Fatalf("expected ErrMissingSignature, got %v", err)
	}
}

func TestVerifyAndInstallWrongSignature(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: false})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()
	cfg := fakeConfig(s)

	err := verifyArtifact(context.Background(), cfg, artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestVerifyAndInstallMissingSignatureEntry(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true, noSig: true})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()
	cfg := fakeConfig(s)

	err := verifyArtifact(context.Background(), cfg, artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrMissingSignature) {
		t.Fatalf("expected ErrMissingSignature, got %v", err)
	}
}

func TestVerifyAndInstallIdentityMismatch(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{
		goodSig: true,
		subject: "https://github.com/attacker/evil/.github/workflows/release.yml@refs/tags/v1.2.3",
	})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()
	cfg := fakeConfig(s)

	err := verifyArtifact(context.Background(), cfg, artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch, got %v", err)
	}
}

func TestVerifyAndInstallIssuerMismatch(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{
		goodSig: true,
		issuer:  "https://evil.example.com",
	})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()
	cfg := fakeConfig(s)

	err := verifyArtifact(context.Background(), cfg, artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch for wrong OIDC issuer, got %v", err)
	}
}

func TestVerifyAndInstallWrongPayloadDigest(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true, wrongHash: true})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()
	cfg := fakeConfig(s)

	err := verifyArtifact(context.Background(), cfg, artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for digest mismatch, got %v", err)
	}
}

func TestVerifyAndInstallTruncatedDownload(t *testing.T) {
	// If the server lies about Content-Length, the download must be rejected
	// rather than installing a truncated binary.
	s, _ := newReleaseServer(t, releaseFixture{lieLength: true})
	defer s.Close()
	cfg := fakeConfig(s)

	err := VerifyAndInstall(context.Background(), cfg, releaseFromFixture(t, []byte("fake binary contents"), ""))
	if !errors.Is(err, ErrTruncatedDownload) {
		t.Fatalf("expected ErrTruncatedDownload, got %v", err)
	}
}
