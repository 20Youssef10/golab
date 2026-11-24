package update

// Ported from upstream tests/test_update.py resilience intent
// (test_pypi_fetch_failure_omits_latest) plus the harder security requirements
// in docs/08 §3 and ADR-014: checksum AND cosign keyless identity verification
// are mandatory, with no silent fallback to an unverified install.

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// AUDIT-02 S-8: the production default must require a Fulcio SCT. The virtual
// test CA cannot embed one, so fakeConfig opts out; clearing the field here
// exercises the production policy and must reject the SCT-less bundle.
func TestVerifyBundleRequiresSCTInProductionMode(t *testing.T) {
	artifact := []byte("fake binary contents")
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true})
	s, _ := newReleaseServer(t, releaseFixture{artifact: artifact, bundle: bundle})
	defer s.Close()
	cfg := fakeConfig(s)
	cfg.RequireSCT = nil // production default

	err := verifyArtifact(context.Background(), cfg, artifact, releaseFromFixture(t, artifact, ""))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for a bundle without an SCT, got %v", err)
	}
	if !strings.Contains(err.Error(), "signed certificate timestamp") {
		t.Fatalf("error must name the SCT check, got %q", err.Error())
	}
}

// The opt-out is narrow: a present SCT is still verified, and production always
// requires one regardless of what the test CA can emit.
func TestSCTRequiredDecision(t *testing.T) {
	withSCT := &x509.Certificate{Extensions: []pkix.Extension{{
		Id:    oidSCTList,
		Value: []byte{0x01},
	}}}
	withoutSCT := &x509.Certificate{}
	off := false
	on := true
	for _, tc := range []struct {
		name string
		cfg  Config
		cert *x509.Certificate
		want bool
	}{
		{"production default", Config{}, withoutSCT, true},
		{"explicit on", Config{RequireSCT: &on}, withoutSCT, true},
		{"test CA opt-out without SCT", Config{RequireSCT: &off}, withoutSCT, false},
		{"test CA opt-out with SCT", Config{RequireSCT: &off}, withSCT, true},
		{"production default with SCT", Config{}, withSCT, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sctRequired(tc.cfg, tc.cert); got != tc.want {
				t.Errorf("sctRequired = %v, want %v", got, tc.want)
			}
		})
	}
}

// Cross-check: update errors must not echo credentials embedded in a release
// URL (the GitHub API response supplies those URLs).
func TestUpdateErrorsRedactSecrets(t *testing.T) {
	canaries := []string{"AUTHCODEC7f3a", "IDTOKC7f3a", "PASSWDC7f3a"}
	url := "https://api.github.com/cb?code=AUTHCODEC7f3a&id_token=IDTOKC7f3a&password=PASSWDC7f3a"

	e := &HTTPError{StatusCode: 401, URL: url}
	for _, c := range canaries {
		if strings.Contains(e.Error(), c) {
			t.Errorf("HTTPError leaked %q: %q", c, e.Error())
		}
	}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer s.Close()
	if _, err := getBytes(context.Background(), s.Client(), s.URL+"?code=AUTHCODEC7f3a", maxReleaseJSON); err != nil {
		if strings.Contains(err.Error(), "AUTHCODEC7f3a") {
			t.Errorf("getBytes status error leaked the code: %q", err.Error())
		}
	}

	client := &http.Client{Transport: errTransport{}}
	if _, err := getBytes(context.Background(), client, url, maxReleaseJSON); err != nil {
		for _, c := range canaries {
			if strings.Contains(err.Error(), c) {
				t.Errorf("getBytes transport error leaked %q: %q", c, err.Error())
			}
		}
	}
}

type errTransport struct{}

func (errTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("dial %s: connection refused", req.URL.String())
}

func TestVerifyAndInstallWrongChecksum(t *testing.T) {
	s, _ := newReleaseServer(t, releaseFixture{badChecksum: true})
	defer s.Close()
	cfg := fakeConfig(s)

	err := VerifyAndInstall(context.Background(), cfg, releaseFromFixture(t, fakeArtifact(t), "0000000000000000000000000000000000000000000000000000000000000000"))
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
	rel := releaseFromFixture(t, fakeArtifact(t), "")
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

	err := VerifyAndInstall(context.Background(), cfg, releaseFromFixture(t, fakeArtifact(t), ""))
	if !errors.Is(err, ErrTruncatedDownload) {
		t.Fatalf("expected ErrTruncatedDownload, got %v", err)
	}
}
