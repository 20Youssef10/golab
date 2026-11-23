package update

// A GoReleaser release has one archive per OS/arch, each with its own cosign
// signature and SBOM bundle. Selecting the first bundle globally can bind the
// verification to a different artifact, so selection must be by exact stem.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func platformArchive(osName, arch string) string {
	return fmt.Sprintf("golab_1.2.3_%s_%s.tar.gz", osName, arch)
}

// selectPlatformBundles must never fall back to a bundle for another archive.
func TestSelectPlatformBundles(t *testing.T) {
	archive := platformArchive("linux", "amd64")
	stem := strings.TrimSuffix(archive, ".tar.gz")
	other := platformArchive("windows", "arm64")

	assets := []githubAsset{
		{Name: other + ".sigstore.json"},
		{Name: other + ".sbom.sigstore.json"},
		{Name: "checksums.txt"},
		{Name: stem + ".sbom.sigstore.json"}, // short SBOM form from the brief
		{Name: archive + ".sigstore.json"},
		{Name: archive + ".sbom.sigstore.json"}, // long form emitted by release.yml
	}

	sig, sbom := selectPlatformBundles(assets, archive)
	if sig == nil || sig.Name != archive+".sigstore.json" {
		t.Fatalf("signature = %+v, want %s", sig, archive+".sigstore.json")
	}
	if sbom == nil || sbom.Name != stem+".sbom.sigstore.json" {
		t.Fatalf("SBOM = %+v, want %s", sbom, stem+".sbom.sigstore.json")
	}

	// No fallback: a release whose only bundles belong to another platform
	// must be rejected, not silently verified against the wrong artifact.
	sig, sbom = selectPlatformBundles([]githubAsset{
		{Name: other + ".sigstore.json"},
		{Name: other + ".sbom.sigstore.json"},
	}, archive)
	if sig != nil || sbom != nil {
		t.Fatalf("cross-platform bundles selected: sig=%+v sbom=%+v", sig, sbom)
	}

	// Generic bundle names without a matching stem are not accepted either.
	sig, sbom = selectPlatformBundles([]githubAsset{
		{Name: "checksums.txt.sigstore.json"},
		{Name: "checksums.txt.sbom.sigstore.json"},
	}, archive)
	if sig != nil || sbom != nil {
		t.Fatalf("unmatched bundles selected: sig=%+v sbom=%+v", sig, sbom)
	}

	// Reversed asset order must give the same answer.
	for i, j := 0, len(assets)-1; i < j; i, j = i+1, j-1 {
		assets[i], assets[j] = assets[j], assets[i]
	}
	sig, sbom = selectPlatformBundles(assets, archive)
	if sig == nil || sbom == nil || sig.Name != archive+".sigstore.json" {
		t.Fatalf("order dependence: sig=%+v sbom=%+v", sig, sbom)
	}
}

type testPlatform struct {
	osName, arch string
	artifact     []byte
	sig, sbom    []byte
}

// multiPlatformServer publishes bundles BEFORE archives so a "first bundle"
// implementation picks the wrong platform. The asset list is deliberately
// not in archive order.
func multiPlatformServer(t *testing.T, platforms []testPlatform) (*httptest.Server, Config) {
	t.Helper()
	assets := []map[string]any{}
	bodies := map[string][]byte{}
	var checksums strings.Builder
	for _, p := range platforms {
		name := platformArchive(p.osName, p.arch)
		sigName := name + ".sigstore.json"
		sbomName := name + ".sbom.sigstore.json"
		sum := sha256.Sum256(p.artifact)
		fmt.Fprintf(&checksums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		bodies[name] = p.artifact
		bodies[sigName] = p.sig
		bodies[sbomName] = p.sbom
		assets = append(assets,
			map[string]any{"name": sigName, "browser_download_url": "https://github.com/" + testOwner + "/" + testRepo + "/download/" + sigName},
			map[string]any{"name": sbomName, "browser_download_url": "https://github.com/" + testOwner + "/" + testRepo + "/download/" + sbomName},
		)
	}
	assets = append(assets, map[string]any{"name": "checksums.txt", "browser_download_url": "https://github.com/" + testOwner + "/" + testRepo + "/download/checksums.txt"})
	for _, p := range platforms {
		name := platformArchive(p.osName, p.arch)
		assets = append(assets, map[string]any{"name": name, "browser_download_url": "https://github.com/" + testOwner + "/" + testRepo + "/download/" + name})
	}
	// Bundle ordering across platforms is the reverse of archive ordering.
	for i, j := 0, len(platforms)-1; i < j; i, j = i+1, j-1 {
		assets[i], assets[j] = assets[j], assets[i]
	}
	bodies["checksums.txt"] = []byte(checksums.String())

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/repos/%s/%s/releases/latest", testOwner, testRepo), func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     testTag,
			"published_at": "2026-01-02T03:04:05Z",
			"assets":       assets,
		})
	})
	mux.HandleFunc(pathPrefix+"/download/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, pathPrefix+"/download/")
		body, ok := bodies[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, fakeConfig(s)
}

func TestCheckSelectsBundlesForChosenArchive(t *testing.T) {
	first := []byte("linux amd64 artifact")
	second := []byte("windows arm64 artifact")
	_, cfg := multiPlatformServer(t, []testPlatform{
		{osName: "linux", arch: "amd64", artifact: first,
			sig:  makeBundle(t, first, bundleOptions{goodSig: true}),
			sbom: makeSBOMBundle(t, first, sbomPredicateCycloneDX)},
		{osName: "windows", arch: "arm64", artifact: second,
			sig:  makeBundle(t, second, bundleOptions{goodSig: true}),
			sbom: makeSBOMBundle(t, second, sbomPredicateCycloneDX)},
	})

	rel, due, err := Check(context.Background(), cfg)
	if err != nil || !due || rel == nil {
		t.Fatalf("Check: rel=%+v due=%v err=%v", rel, due, err)
	}
	wantArchive := platformArchive("linux", "amd64")
	if !strings.HasSuffix(rel.URL, "/"+wantArchive) {
		t.Fatalf("archive URL = %q, want %s", rel.URL, wantArchive)
	}
	if !strings.HasSuffix(rel.SignatureURL, "/"+wantArchive+".sigstore.json") {
		t.Fatalf("signature URL = %q", rel.SignatureURL)
	}
	if !strings.HasSuffix(rel.SBOMURL, "/"+wantArchive+".sbom.sigstore.json") {
		t.Fatalf("SBOM URL = %q", rel.SBOMURL)
	}

	// The selected pair must actually verify the selected artifact.
	artifact, err := getBytes(context.Background(), httpClient(cfg), rel.URL, maxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyArtifact(context.Background(), cfg, artifact, rel); err != nil {
		t.Fatalf("selected signature did not verify: %v", err)
	}
	sbomBytes, err := getBytes(context.Background(), httpClient(cfg), rel.SBOMURL, maxBundleBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySBOM(context.Background(), cfg, artifact, sbomBytes); err != nil {
		t.Fatalf("selected SBOM did not verify: %v", err)
	}
}

func TestCheckRefusesCrossPlatformBundle(t *testing.T) {
	first := []byte("linux amd64 artifact")
	second := []byte("windows arm64 artifact")
	// The linux archive is signed with a bundle for different bytes: selecting
	// the first bundle globally would verify (or at least bind) the wrong one.
	_, cfg := multiPlatformServer(t, []testPlatform{
		{osName: "linux", arch: "amd64", artifact: first,
			sig:  makeBundle(t, second, bundleOptions{goodSig: true}),
			sbom: makeSBOMBundle(t, second, sbomPredicateCycloneDX)},
		{osName: "windows", arch: "arm64", artifact: second,
			sig:  makeBundle(t, second, bundleOptions{goodSig: true}),
			sbom: makeSBOMBundle(t, second, sbomPredicateCycloneDX)},
	})
	rel, _, err := Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := getBytes(context.Background(), httpClient(cfg), rel.URL, maxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyArtifact(context.Background(), cfg, artifact, rel); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("mismatched signature accepted: %v", err)
	}
	sbomBytes, err := getBytes(context.Background(), httpClient(cfg), rel.SBOMURL, maxBundleBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySBOM(context.Background(), cfg, artifact, sbomBytes); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("mismatched SBOM accepted: %v", err)
	}
}

// The composition root now pins 20Youssef10/golab; the workflow SAN regex built
// from Config must accept exactly that identity and reject a different owner.
func TestPinnedWorkflowIdentityMatchesGolabRepo(t *testing.T) {
	artifact := []byte("fake binary contents")
	cfg := fakeConfig(nil)
	cfg.Owner, cfg.Repo = "20Youssef10", "golab"

	good := "https://github.com/20Youssef10/golab/.github/workflows/release.yml@refs/tags/v1.2.3"
	bundle := makeBundle(t, artifact, bundleOptions{goodSig: true, subject: good})
	if err := verifySignature(context.Background(), cfg, artifact, bundle); err != nil {
		t.Fatalf("pinned identity rejected: %v", err)
	}

	bad := "https://github.com/someone-else/golab/.github/workflows/release.yml@refs/tags/v1.2.3"
	bundle = makeBundle(t, artifact, bundleOptions{goodSig: true, subject: bad})
	if err := verifySignature(context.Background(), cfg, artifact, bundle); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("wrong owner accepted: %v", err)
	}
}
