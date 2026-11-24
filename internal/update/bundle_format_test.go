package update

// Regression tests for the cosign bundle-format mismatch found in the live
// v0.1.6 release: cosign v2.6.5 emitted LEGACY bundles (base64Signature / cert
// / rekorBundle), while verify.go parses sigstore v0.3 protobuf bundles only.
// The legacy shape must stay rejected with the typed ErrInvalidSignature;
// these tests document the exact wire-level difference without weakening any
// verification policy.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	v1 "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"google.golang.org/protobuf/encoding/protojson"
)

// legacyCosignBundle is the minimal shape written by cosign v2.6.5 without
// --new-bundle-format. All values are inert placeholders (no real key, cert
// or transparency-log material); the point is the field names, which are
// unknown to the v0.3 protobuf parser used by loadBundle.
const legacyCosignBundle = `{
  "base64Signature": "MEQCIFRlc3RMZWdhY3lTaWduYXR1cmVCeXRlcw==",
  "cert": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCkluZXJ0Q2VydEhlcmUKLS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQo=",
  "rekorBundle": {
    "SignedEntryTimestamp": "MEQCIUxlZ2FjeVNldEJ5dGVz",
    "Payload": {
      "body": "aW5lcnRCb2R5",
      "integratedTime": 1735689600,
      "logIndex": 1,
      "logID": "abcdef0123456789"
    }
  }
}`

// TestLoadBundleRejectsLegacyCosignBundle pins the live defect: a legacy
// cosign bundle is not a v0.3 bundle, so every entry point that consumes a
// downloaded signature must fail closed with ErrInvalidSignature. No legacy
// fallback may be added in production code to make this test pass.
func TestLoadBundleRejectsLegacyCosignBundle(t *testing.T) {
	legacy := []byte(legacyCosignBundle)

	b, err := loadBundle(legacy)
	if b != nil {
		t.Fatalf("loadBundle returned a bundle for a legacy cosign bundle: %#v", b)
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("loadBundle: expected ErrInvalidSignature, got %v", err)
	}
	if !strings.Contains(err.Error(), "base64Signature") {
		t.Fatalf("loadBundle error must name the legacy field mismatch, got %q", err.Error())
	}

	// verifySignature uses the same parse path; no trust material is fetched
	// because parsing fails first.
	if err := verifySignature(context.Background(), Config{}, []byte("artifact"), legacy); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("verifySignature: expected ErrInvalidSignature, got %v", err)
	}

	// verifyArtifact is the path used by `colab update --install`: it fetches
	// the signature asset from the release and then verifies it.
	artifact := []byte("fake binary contents")
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: legacy})
	defer s.Close()
	err = verifyArtifact(context.Background(), fakeConfig(s), artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("verifyArtifact: expected ErrInvalidSignature, got %v", err)
	}
}

// makeMessageSignatureBundle builds the v0.3 bundle shape produced by
// `cosign sign-blob --new-bundle-format`: messageSignature content (raw ECDSA
// signature + SHA2_256 digest), a Fulcio leaf certificate, an RFC3161
// timestamp, and a hashedrekord tlog entry carrying both a signed entry
// timestamp (inclusion promise) and an inclusion proof. sigstore-go's bundle
// validator requires the inclusion proof for v0.3 bundles with tlog entries.
func makeMessageSignatureBundle(t *testing.T, artifact []byte) []byte {
	t.Helper()

	entity, err := testVS.SignAtTime(defaultWorkflowSAN(), githubIssuer, artifact, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	sigContent, err := entity.SignatureContent()
	if err != nil {
		t.Fatal(err)
	}
	msg := sigContent.MessageSignatureContent()
	if msg == nil {
		t.Fatal("virtual sigstore sign entity has no message signature content")
	}
	verification, err := entity.VerificationContent()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := entity.TlogEntries()
	if err != nil {
		t.Fatal(err)
	}
	timestamps, err := entity.Timestamps()
	if err != nil {
		t.Fatal(err)
	}

	material := &v1.VerificationMaterial{
		TimestampVerificationData: &v1.TimestampVerificationData{},
		Content: &v1.VerificationMaterial_Certificate{
			Certificate: &protocommon.X509Certificate{RawBytes: verification.Certificate().Raw},
		},
	}
	for _, entry := range entries {
		tle := entry.TransparencyLogEntry()
		if tle.KindVersion == nil {
			// tlog.NewEntry (used by the virtual Sigstore) leaves KindVersion
			// unset; the bundle proto requires it for every tlog entry.
			var proposed struct {
				Kind       string `json:"kind"`
				APIVersion string `json:"apiVersion"`
			}
			_ = json.Unmarshal(tle.CanonicalizedBody, &proposed)
			kind, version := proposed.Kind, proposed.APIVersion
			if kind == "" {
				kind = "hashedrekord"
			}
			if version == "" {
				version = "0.0.1"
			}
			tle.KindVersion = &protorekor.KindVersion{Kind: kind, Version: version}
		}

		proof, err := testVS.GetInclusionProof(tle.CanonicalizedBody)
		if err != nil {
			t.Fatal(err)
		}
		// The virtual Sigstore's sign helper uses log index 1000 but its test
		// proof describes a one-leaf tree. Align the entry with the proof and
		// re-sign the SET over the aligned payload; otherwise inclusion
		// verification fails with "index is beyond size".
		tle.LogIndex = *proof.LogIndex
		set, err := testVS.RekorSignPayload(tlog.RekorPayload{
			Body:           entry.Body(),
			IntegratedTime: tle.IntegratedTime,
			LogIndex:       tle.LogIndex,
			LogID:          hex.EncodeToString([]byte(entry.LogKeyID())),
		})
		if err != nil {
			t.Fatal(err)
		}
		tle.InclusionPromise = &protorekor.InclusionPromise{SignedEntryTimestamp: set}

		rootHash, err := hex.DecodeString(*proof.RootHash)
		if err != nil {
			t.Fatal(err)
		}
		hashes := make([][]byte, 0, len(proof.Hashes))
		for _, h := range proof.Hashes {
			decoded, err := hex.DecodeString(h)
			if err != nil {
				t.Fatal(err)
			}
			hashes = append(hashes, decoded)
		}
		tle.InclusionProof = &protorekor.InclusionProof{
			LogIndex:   tle.LogIndex,
			RootHash:   rootHash,
			TreeSize:   *proof.TreeSize,
			Hashes:     hashes,
			Checkpoint: &protorekor.Checkpoint{Envelope: *proof.Checkpoint},
		}
		material.TlogEntries = append(material.TlogEntries, tle)
	}
	for _, ts := range timestamps {
		material.TimestampVerificationData.Rfc3161Timestamps = append(
			material.TimestampVerificationData.Rfc3161Timestamps,
			&protocommon.RFC3161SignedTimestamp{SignedTimestamp: ts})
	}

	protoBundle := &v1.Bundle{
		MediaType:            "application/vnd.dev.sigstore.bundle.v0.3+json",
		VerificationMaterial: material,
		Content: &v1.Bundle_MessageSignature{MessageSignature: &protocommon.MessageSignature{
			MessageDigest: &protocommon.HashOutput{
				Algorithm: protocommon.HashAlgorithm_SHA2_256,
				Digest:    msg.Digest(),
			},
			Signature: msg.Signature(),
		}},
	}
	out, err := protojson.Marshal(protoBundle)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestLoadBundleAcceptsV03MessageSignatureBundle proves that a v0.3
// messageSignature bundle is parsed and that its signature content and tlog
// material survive the parse. Full verification of this shape (including the
// artifact-digest policy) is covered separately in
// TestVerifyBundleAcceptsV03MessageSignatureBundle.
func TestLoadBundleAcceptsV03MessageSignatureBundle(t *testing.T) {
	artifact := []byte("fake binary contents")
	b, err := loadBundle(makeMessageSignatureBundle(t, artifact))
	if err != nil {
		t.Fatalf("loadBundle rejected a v0.3 messageSignature bundle: %v", err)
	}
	if _, ok := b.Content.(*v1.Bundle_MessageSignature); !ok {
		t.Fatalf("expected messageSignature content, got %T", b.Content)
	}
	if !b.MinVersion("v0.3") {
		t.Fatalf("expected bundle version >= v0.3, got %q", b.MediaType)
	}

	sigContent, err := b.SignatureContent()
	if err != nil {
		t.Fatalf("SignatureContent: %v", err)
	}
	msg := sigContent.MessageSignatureContent()
	if msg == nil {
		t.Fatal("MessageSignatureContent is nil after parsing a messageSignature bundle")
	}
	if len(sigContent.Signature()) == 0 {
		t.Fatal("signature content is empty after parsing a messageSignature bundle")
	}
	wantDigest := sha256.Sum256(artifact)
	if !bytes.Equal(msg.Digest(), wantDigest[:]) {
		t.Fatalf("message digest = %x, want %x", msg.Digest(), wantDigest)
	}
	if got := msg.DigestAlgorithm(); got != "SHA2_256" {
		t.Fatalf("digest algorithm = %q, want SHA2_256", got)
	}

	verification, err := b.VerificationContent()
	if err != nil || verification == nil || verification.Certificate() == nil {
		t.Fatalf("VerificationContent missing certificate: err=%v content=%v", err, verification)
	}
	entries, err := b.TlogEntries()
	if err != nil {
		t.Fatalf("TlogEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d tlog entries, want 1", len(entries))
	}
	if !entries[0].HasInclusionPromise() {
		t.Error("tlog entry lost its signed entry timestamp")
	}
	if !entries[0].HasInclusionProof() {
		t.Error("tlog entry lost its inclusion proof")
	}
}

// TestVerifyBundleAcceptsV03MessageSignatureBundle exercises the complete
// verifyBundle path on the new bundle shape: Fulcio chain, SCT opt-out for the
// test CA, Rekor SET + inclusion proof, RFC3161 timestamp, pinned identity and
// the artifact digest. Production still requires a real SCT (see
// TestVerifyBundleRequiresSCTInProductionMode).
func TestVerifyBundleAcceptsV03MessageSignatureBundle(t *testing.T) {
	artifact := []byte("fake binary contents")
	b, err := loadBundle(makeMessageSignatureBundle(t, artifact))
	if err != nil {
		t.Fatalf("loadBundle: %v", err)
	}
	if _, err := verifyBundle(context.Background(), fakeConfig(nil), artifact, b); err != nil {
		t.Fatalf("verifyBundle rejected a valid v0.3 messageSignature bundle: %v", err)
	}

	// Guard against a vacuous pass: the same bundle must reject different
	// artifact bytes at the digest policy.
	if _, err := verifyBundle(context.Background(), fakeConfig(nil), []byte("tampered artifact"), b); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("verifyBundle accepted tampered artifact bytes: %v", err)
	}
}
