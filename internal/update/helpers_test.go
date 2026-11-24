package update

// Ported from upstream tests/test_update.py (google-colab-cli). Upstream used
// pytest fixtures (fake_settings, mock_pypi, app_version); here we build an
// httptest fake GitHub Releases server plus real Sigstore bundles produced by
// sigstore-go's virtual Sigstore (Fulcio CA chain, Rekor entry with an
// inclusion proof and SET, RFC3161 timestamp). Its test CA cannot embed SCTs,
// so fakeConfig opts out of the SCT requirement for these bundles only.
// Verification is exercised
// end-to-end without ever touching the network or the real GitHub/TUF APIs.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	testOwner  = "testowner"
	testRepo   = "testrepo"
	testTag    = "v1.2.3"
	testVer    = "1.2.3"
	assetName  = "golab_Linux_x86_64.tar.gz"
	assetPath  = "/download/asset/golab_Linux_x86_64.tar.gz"
	pathPrefix = "/" + testOwner + "/" + testRepo

	// Real releases use one bundle per archive (see .github/workflows/release.yml).
	bundleAssetName = assetName + ".sigstore.json"
	sbomAssetName   = assetName + ".sbom.sigstore.json"
)

// testVS is the trusted (virtual) Sigstore used by every test. It issues real
// Fulcio certificates and signs real Rekor entries, so sigstore-go performs
// the same chain/proof/timestamp checks it would against the public good
// instance. Tests that need a different trust anchor override it temporarily.
var testVS *ca.VirtualSigstore

// setTrustedMaterialForTests is deliberately defined in a _test.go file: no
// production caller can inject its own trust anchors.
func setTrustedMaterialForTests(fn func(context.Context) (root.TrustedMaterial, error)) {
	trustedMaterialLoader = fn
}

func TestMain(m *testing.M) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		panic(err)
	}
	testVS = vs
	setTrustedMaterialForTests(func(context.Context) (root.TrustedMaterial, error) { return vs, nil })
	os.Exit(m.Run())
}

// withTrustedMaterial swaps the trusted root for the duration of a test.
func withTrustedMaterial(t *testing.T, tm root.TrustedMaterial) {
	t.Helper()
	previous := trustedMaterialLoader
	setTrustedMaterialForTests(func(context.Context) (root.TrustedMaterial, error) { return tm, nil })
	t.Cleanup(func() { setTrustedMaterialForTests(previous) })
}

// bundleOptions controls how a fake Sigstore bundle is built in tests.
type bundleOptions struct {
	subject        string    // URI SAN identity; defaults to a matching release workflow
	issuer         string    // Fulcio issuer extension; defaults to GitHub Actions
	goodSig        bool      // when false, corrupt the ECDSA signature
	noSig          bool      // omit the signature entry entirely
	noTlog         bool      // omit the Rekor entry
	noCert         bool      // omit the signing certificate
	wrongHash      bool      // sign a statement with a digest for different bytes
	noStatement    bool      // produce a payload that is not an in-toto statement
	withSET        bool      // include a signed entry timestamp in the Rekor entry
	tamperSET      bool      // corrupt the signed entry timestamp
	integratedTime time.Time // Rekor integrated time; defaults to now+5m
}

func defaultWorkflowSAN() string {
	return fmt.Sprintf("https://github.com/%s/%s/.github/workflows/release.yml@refs/tags/%s", testOwner, testRepo, testTag)
}

// makeBundle builds a Sigstore JSON bundle for artifact. The bundle is a real
// DSSE attestation signed by a Fulcio-issued certificate whose URI SAN carries
// the workload identity and whose extension carries the OIDC issuer; its Rekor
// entry carries an inclusion proof, a SET and an RFC3161 timestamp.
func makeBundle(t *testing.T, artifact []byte, opts bundleOptions) []byte {
	t.Helper()
	statement := makeStatement(t, artifact, opts)
	return makeBundleFromStatement(t, statement, opts)
}

func makeStatement(t *testing.T, artifact []byte, opts bundleOptions) []byte {
	t.Helper()
	sum := sha256.Sum256(artifact)
	if opts.wrongHash {
		sum = sha256.Sum256([]byte("some other bytes"))
	}
	payload := fmt.Sprintf(`{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"customFoo","subject":[{"name":%q,"digest":{"sha256":"%s"}}],"predicate":{}}`,
		assetName, hex.EncodeToString(sum[:]))
	if opts.noStatement {
		payload = `{"not":"a statement"}`
	}
	return []byte(payload)
}

func makeBundleFromStatement(t *testing.T, statement []byte, opts bundleOptions) []byte {
	t.Helper()
	return makeBundleSignedBy(t, testVS, statement, opts)
}

// makeBundleSignedBy creates a bundle signed by a specific virtual Sigstore.
// Using a second instance lets tests simulate a different Fulcio/Rekor CA.
func makeBundleSignedBy(t *testing.T, vs *ca.VirtualSigstore, statement []byte, opts bundleOptions) []byte {
	t.Helper()
	subject := opts.subject
	if subject == "" {
		subject = defaultWorkflowSAN()
	}
	issuer := opts.issuer
	if issuer == "" {
		issuer = githubIssuer
	}
	integratedTime := opts.integratedTime
	if integratedTime.IsZero() {
		integratedTime = time.Now().Add(5 * time.Minute)
	}

	entity, err := vs.AttestAtTime(subject, issuer, statement, integratedTime, true)
	if err != nil {
		t.Fatal(err)
	}
	return bundleJSON(t, vs, entity, opts)
}

// makeSBOMBundle creates a signed CycloneDX SBOM attestation for artifact.
func makeSBOMBundle(t *testing.T, artifact []byte, predicateType string) []byte {
	t.Helper()
	sum := sha256.Sum256(artifact)
	statement := fmt.Sprintf(`{"_type":"https://in-toto.io/Statement/v0.1","predicateType":%q,"subject":[{"name":%q,"digest":{"sha256":"%s"}}],"predicate":{"bomFormat":"CycloneDX","specVersion":"1.5"}}`,
		predicateType, assetName, hex.EncodeToString(sum[:]))
	return makeBundleFromStatement(t, []byte(statement), bundleOptions{goodSig: true})
}

// bundleJSON serialises a sigstore-go TestEntity into a standard v0.3 Sigstore
// bundle JSON document. Every field below comes from the verified entity, so
// the result can be fed straight to bundle.Bundle.UnmarshalJSON.
func bundleJSON(t *testing.T, vs *ca.VirtualSigstore, entity *ca.TestEntity, opts bundleOptions) []byte {
	t.Helper()
	sigContent, err := entity.SignatureContent()
	if err != nil {
		t.Fatal(err)
	}
	env := sigContent.EnvelopeContent().RawEnvelope()
	// dsse.Envelope.Payload is base64 text; the protobuf field carries the raw
	// statement bytes (bundle.parseEnvelope base64-encodes them again).
	rawPayload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		t.Fatal(err)
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

	protoEntry := &protodsse.Envelope{
		Payload:     rawPayload,
		PayloadType: env.PayloadType,
	}
	if !opts.noSig {
		for _, sig := range env.Signatures {
			raw, err := base64.StdEncoding.DecodeString(sig.Sig)
			if err != nil {
				t.Fatal(err)
			}
			if !opts.goodSig && len(raw) > 4 {
				raw[len(raw)-4] ^= 0xff // corrupt the signature bytes
			}
			protoEntry.Signatures = append(protoEntry.Signatures, &protodsse.Signature{
				Sig: raw, Keyid: sig.KeyID,
			})
		}
	}

	material := &v1.VerificationMaterial{
		TlogEntries:               nil,
		TimestampVerificationData: &v1.TimestampVerificationData{},
	}
	if !opts.noCert {
		material.Content = &v1.VerificationMaterial_Certificate{
			Certificate: &protocommon.X509Certificate{RawBytes: verification.Certificate().Raw},
		}
	}
	if !opts.noTlog {
		for _, entry := range entries {
			tle := entry.TransparencyLogEntry()
			// tlog.NewEntry (used by the virtual sigstore) leaves KindVersion
			// unset; the bundle proto requires it for every tlog entry.
			var proposed struct {
				Kind       string `json:"kind"`
				APIVersion string `json:"apiVersion"`
			}
			if err := json.Unmarshal(tle.CanonicalizedBody, &proposed); err != nil {
				t.Fatal(err)
			}
			if tle.KindVersion == nil {
				tle.KindVersion = &protorekor.KindVersion{Kind: proposed.Kind, Version: proposed.APIVersion}
			}
			if opts.withSET || opts.tamperSET {
				payload := tlog.RekorPayload{
					Body:           entry.Body(),
					IntegratedTime: tle.IntegratedTime,
					LogIndex:       tle.LogIndex,
					LogID:          hex.EncodeToString([]byte(entry.LogKeyID())),
				}
				set, err := vs.RekorSignPayload(payload)
				if err != nil {
					t.Fatal(err)
				}
				if opts.tamperSET && len(set) > 4 {
					set[len(set)-4] ^= 0xff
				}
				tle.InclusionPromise = &protorekor.InclusionPromise{SignedEntryTimestamp: set}
			}
			material.TlogEntries = append(material.TlogEntries, tle)
		}
	}
	for _, ts := range timestamps {
		material.TimestampVerificationData.Rfc3161Timestamps = append(
			material.TimestampVerificationData.Rfc3161Timestamps,
			&protocommon.RFC3161SignedTimestamp{SignedTimestamp: ts})
	}

	protoBundle := &v1.Bundle{
		MediaType:            "application/vnd.dev.sigstore.bundle.v0.3+json",
		VerificationMaterial: material,
		Content:              &v1.Bundle_DsseEnvelope{DsseEnvelope: protoEntry},
	}
	out, err := protojson.Marshal(protoBundle)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// redirectTransport points requests aimed at api.github.com at a local
// httptest server. This keeps production URLs pinned while letting the fake
// server serve every response.
type redirectTransport struct {
	base *url.URL
	rt   http.RoundTripper
}

func (r redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = r.base.Scheme
	clone.URL.Host = r.base.Host
	clone.Host = r.base.Host
	rt := r.rt
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(clone)
}

func clientForServer(s *httptest.Server) *http.Client {
	u, _ := url.Parse(s.URL)
	return &http.Client{Transport: redirectTransport{base: u}}
}

// releaseFixture describes the fake GitHub release served by newReleaseServer.
type releaseFixture struct {
	tag         string
	version     string
	prerelease  bool
	artifact    []byte
	checksum    string // hex checksum for the artifact; empty => "checksums.txt" omits it
	noChecksum  bool
	noSignature bool
	noSBOM      bool
	badSBOM     bool // SBOM attestation binds to different bytes
	badChecksum bool
	bundle      []byte // when nil, a valid bundle is generated from artifact
	lieLength   bool   // advertise a Content-Length larger than the body
	assetRename string // override the artifact asset name
}

// fakeExecutable is deliberately ELF-prefixed: if a test ever installs the
// archive bytes instead of the extracted binary, the failure is unmistakable.
var fakeExecutable = []byte("\x7fELF\x02\x01\x01\x00golab-fake-executable-payload")

// archiveEntry describes one member of a test .tar.gz. A zero Typeflag means a
// regular file; Linkname is only meaningful for symlink/hardlink entries.
type archiveEntry struct {
	name     string
	data     []byte
	mode     int64
	typeflag byte
	linkname string
}

// makeTarGz builds the flat GoReleaser archive layout shipped in real releases:
// the executable plus documentation files, gzip-compressed.
func makeTarGz(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     mode,
			Size:     int64(len(e.data)),
			Typeflag: typeflag,
			Linkname: e.linkname,
			ModTime:  time.Unix(0, 0),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header for %q: %v", e.name, err)
		}
		if typeflag == tar.TypeReg || typeflag == tar.TypeRegA {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatalf("write tar member %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}

// fakeArtifact returns a production-shaped release asset: a GoReleaser-style
// .tar.gz containing golab plus the documentation files listed in
// .goreleaser.yaml. Fake releases must look like what production downloads.
func fakeArtifact(t *testing.T) []byte {
	t.Helper()
	binaryName := "golab"
	if runtime.GOOS == "windows" {
		binaryName = "golab.exe"
	}
	return makeTarGz(t,
		archiveEntry{name: binaryName, data: fakeExecutable, mode: 0o755},
		archiveEntry{name: "CHANGELOG.md", data: []byte("# changelog\n")},
		archiveEntry{name: "LICENSE", data: []byte("Apache-2.0\n")},
		archiveEntry{name: "NOTICE", data: []byte("notice\n")},
		archiveEntry{name: "README.md", data: []byte("# golab\n")},
	)
}

// redirectExecutable points the self-update install target at path for the
// duration of a test. os.Executable() is the running test binary, so calling
// VerifyAndInstall without this would destroy the test process on disk.
func redirectExecutable(t *testing.T, path string) {
	t.Helper()
	previous := executablePath
	executablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { executablePath = previous })
}

func (f releaseFixture) withDefaults(t *testing.T) releaseFixture {
	if f.tag == "" {
		f.tag = testTag
	}
	if f.version == "" {
		f.version = testVer
	}
	if f.artifact == nil {
		f.artifact = fakeArtifact(t)
	}
	if f.assetRename == "" {
		f.assetRename = assetName
	}
	return f
}

// newReleaseServer serves a pinned-owner GitHub Releases "latest" document and
// the referenced assets. It counts asset downloads so throttle tests can
// assert that no network traffic happens when a check is not due.
func newReleaseServer(t *testing.T, f releaseFixture) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	f = f.withDefaults(t)
	var downloads atomic.Int32

	checksum := f.checksum
	if checksum == "" && !f.badChecksum {
		sum := sha256.Sum256(f.artifact)
		checksum = hex.EncodeToString(sum[:])
	}
	if f.badChecksum {
		checksum = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	bundle := f.bundle
	if bundle == nil {
		bundle = makeBundle(t, f.artifact, bundleOptions{goodSig: true})
	}
	sbom := makeSBOMBundle(t, f.artifact, sbomPredicateCycloneDX)
	if f.badSBOM {
		sbom = makeSBOMBundle(t, []byte("different binary"), sbomPredicateCycloneDX)
	}
	checksumBody := fmt.Sprintf("%s  %s\n", checksum, f.assetRename)
	if f.noChecksum {
		checksumBody = "some other file\n"
	}

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/repos/%s/%s/releases/latest", testOwner, testRepo), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var assets []map[string]any
		assets = append(assets, map[string]any{
			"name":                 f.assetRename,
			"browser_download_url": "https://github.com/" + testOwner + "/" + testRepo + assetPath,
			"size":                 len(f.artifact),
		})
		if !f.noChecksum {
			assets = append(assets, map[string]any{
				"name":                 "checksums.txt",
				"browser_download_url": "https://github.com/" + testOwner + "/" + testRepo + "/download/checksums.txt",
			})
		}
		if !f.noSignature {
			assets = append(assets, map[string]any{
				"name":                 bundleAssetName,
				"browser_download_url": "https://github.com/" + testOwner + "/" + testRepo + "/download/" + bundleAssetName,
			})
		}
		if !f.noSBOM {
			assets = append(assets, map[string]any{
				"name":                 sbomAssetName,
				"browser_download_url": "https://github.com/" + testOwner + "/" + testRepo + "/download/" + sbomAssetName,
			})
		}
		resp := map[string]any{
			"tag_name":     f.tag,
			"html_url":     "https://github.com/" + testOwner + "/" + testRepo + "/releases/tag/" + f.tag,
			"published_at": "2026-01-02T03:04:05Z",
			"prerelease":   f.prerelease,
			"assets":       assets,
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc(pathPrefix+assetPath, func(w http.ResponseWriter, r *http.Request) {
		if f.lieLength {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(f.artifact)+64))
		}
		_, _ = w.Write(f.artifact)
	})
	mux.HandleFunc(pathPrefix+"/download/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksumBody))
	})
	mux.HandleFunc(pathPrefix+"/download/"+bundleAssetName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bundle)
	})
	mux.HandleFunc(pathPrefix+"/download/"+sbomAssetName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(sbom)
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		mux.ServeHTTP(w, r)
	})
	return httptest.NewServer(handler), &downloads
}

func fakeConfig(s *httptest.Server) Config {
	client := &http.Client{}
	if s != nil {
		client = clientForServer(s)
	}
	return Config{
		Owner:          testOwner,
		Repo:           testRepo,
		AssetPattern:   `golab_.*\.tar\.gz`,
		CurrentVersion: "0.9.0",
		Enabled:        true,
		HTTPClient:     client,
		// The virtual test CA cannot embed SCTs in its Fulcio leaf
		// certificates. Opt out of the SCT requirement for test bundles only;
		// production leaves this nil and therefore requires an SCT.
		RequireSCT: func() *bool { v := false; return &v }(),
	}
}

// releaseFromFixture builds a Release whose asset, signature and SBOM URLs
// point at the fake server. checksum may be overridden; an empty value derives
// the correct SHA-256 from artifact.
func releaseFromFixture(t *testing.T, artifact []byte, checksum string) *Release {
	t.Helper()
	if checksum == "" {
		sum := sha256.Sum256(artifact)
		checksum = hex.EncodeToString(sum[:])
	}
	return &Release{
		Version:      testVer,
		Tag:          testTag,
		URL:          "https://github.com/" + testOwner + "/" + testRepo + assetPath,
		Checksum:     checksum,
		SignatureURL: "https://github.com/" + testOwner + "/" + testRepo + "/download/" + bundleAssetName,
		SBOMURL:      "https://github.com/" + testOwner + "/" + testRepo + "/download/" + sbomAssetName,
		PublishedAt:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}
