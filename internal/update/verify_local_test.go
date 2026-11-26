package update

// `update --verify-only PATH` is a CI gate for release artifacts: checksum,
// cosign signature and signed SBOM must all verify against the pinned
// identity, with no download and no installation.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeLocalRelease(t *testing.T, artifact []byte, bundle, sbom []byte) (string, Config) {
	t.Helper()
	dir := t.TempDir()
	name := "golab_1.2.3_linux_amd64.tar.gz"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, artifact, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifact)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}
	if bundle != nil {
		if err := os.WriteFile(path+".sigstore.json", bundle, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if sbom != nil {
		if err := os.WriteFile(path+".sbom.sigstore.json", sbom, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path, fakeConfig(nil)
}

func TestVerifyLocalAcceptsSignedArtifact(t *testing.T) {
	artifact := []byte("release artifact bytes")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true})
	sbom := makeSBOMBundle(t, artifact, sbomPredicateCycloneDX)
	path, cfg := writeLocalRelease(t, artifact, bundle, sbom)
	res, err := VerifyLocal(context.Background(), cfg, path, LocalVerifyOptions{})
	if err != nil {
		t.Fatalf("verify-only failed: %v", err)
	}
	if res.Artifact != path || res.SHA256 == "" || res.SBOMFile == "" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestVerifyLocalRejectsTamperingAndMissingMaterial(t *testing.T) {
	artifact := []byte("release artifact bytes")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true})
	sbom := makeSBOMBundle(t, artifact, sbomPredicateCycloneDX)

	t.Run("checksum", func(t *testing.T) {
		path, cfg := writeLocalRelease(t, artifact, bundle, sbom)
		if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyLocal(context.Background(), cfg, path, LocalVerifyOptions{}); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("tampered artifact = %v, want checksum mismatch", err)
		}
	})
	t.Run("missing-signature", func(t *testing.T) {
		path, cfg := writeLocalRelease(t, artifact, nil, sbom)
		if _, err := VerifyLocal(context.Background(), cfg, path, LocalVerifyOptions{}); !errors.Is(err, ErrMissingSignature) {
			t.Fatalf("missing bundle = %v", err)
		}
	})
	t.Run("missing-sbom", func(t *testing.T) {
		path, cfg := writeLocalRelease(t, artifact, bundle, nil)
		if _, err := VerifyLocal(context.Background(), cfg, path, LocalVerifyOptions{}); !errors.Is(err, ErrMissingSBOM) {
			t.Fatalf("missing sbom = %v", err)
		}
	})
	t.Run("wrong-identity", func(t *testing.T) {
		bad := makeBundle(t, artifact, bundleOptions{subject: "https://example.com/evil", goodSig: true})
		path, cfg := writeLocalRelease(t, artifact, bad, sbom)
		if _, err := VerifyLocal(context.Background(), cfg, path, LocalVerifyOptions{}); !errors.Is(err, ErrIdentityMismatch) {
			t.Fatalf("wrong signer = %v", err)
		}
	})
}
