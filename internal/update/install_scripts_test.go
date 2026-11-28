package update

// The installers must never fetch-and-run unverified code. Static assertions
// pin the policy; the local --verify-only mode of install.sh is exercised with
// a fake cosign so the checksum/signature gate is tested, not just documented.
// The same file guards the packaging configs and the release/docker pipelines
// that produce the bytes those installers consume.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// nextVersion is the published, signed release that every third-party channel
// manifest pins. It carries the 13 CLI .tar.gz archives, the 3 winget .zip
// repacks, and the standalone golab-mcp archives, all signed at the same tag.
const nextVersion = "0.3.0"

// publishedV030 is an independent transcription of the SIGNED v0.3.0
// checksums.txt (all 16 lines: 13 tar.gz + 3 windows zip), downloaded from
// https://github.com/20Youssef10/golab/releases/download/v0.3.0/checksums.txt
// (SHA-256 of that file: 7305d6456cdf4f52003bab064f7994678845f6da3e07c6152228d02ad4714198).
// The packaging manifests are checked against THIS fixture, never against
// their own contents, so a wrong-platform or placeholder hash cannot pass.
var publishedV030 = map[string]string{
	"golab_0.3.0_darwin_amd64.tar.gz":  "7026201245238ce5f518438adee2407e04fd6ea454ebb65071237eb6e7a512c4",
	"golab_0.3.0_darwin_arm64.tar.gz":  "3a8c35b41206460bf66cd7c5b4e6f6da965dc17abf4bea174255d6ebf89f5e03",
	"golab_0.3.0_freebsd_386.tar.gz":   "fe9a4eb8b5861bbb2b066fcfe0782689228a61e8f3120fbb76ed3295ef04a3fd",
	"golab_0.3.0_freebsd_amd64.tar.gz": "0b6e7c53cc87034c789ee8a55f1b4cc82bba800e9872982acb50d73bdfc75ecb",
	"golab_0.3.0_freebsd_arm64.tar.gz": "5e1f09a98587cf0383768ff954eff464823cc45484a7e3cae830cea252e66f53",
	"golab_0.3.0_linux_386.tar.gz":     "b737d8540d82ff326f264a886188a1231c2636beb35e472db9130c0a79c05eee",
	"golab_0.3.0_linux_amd64.tar.gz":   "248d3099e7fb5b9ce13e24d32768b0aff1eaecc50b96f8e5e5037af3c2d76f88",
	"golab_0.3.0_linux_arm.tar.gz":     "b277bdb0ed82122b56a75acfdac3d4ac2f934b36584aa860c12ef8e334ebd726",
	"golab_0.3.0_linux_arm64.tar.gz":   "a0f92813997fdad425bbf83b649aee78511278c4c587f1678d49d5245bc649de",
	"golab_0.3.0_linux_riscv64.tar.gz": "0e9d2b5563ac8f5d7ffaaa24892189a93867f80827032ce64cc721baff732b23",
	"golab_0.3.0_windows_386.tar.gz":   "547ab75039cff395e74a8f24458ee56c22e321f88c28714ff04d087dde6d0bf8",
	"golab_0.3.0_windows_386.zip":      "42ae6b195c07a82c7b203491c72ef801105eadd8c5a711b6d15bff9141cd00c3",
	"golab_0.3.0_windows_amd64.tar.gz": "5fdeaa7ecda7136cc333f96109917e8b957c5b2f7008cdf27ff88d4bfb30bb02",
	"golab_0.3.0_windows_amd64.zip":    "d217650dfd4fe300b9282f2d3068ca8f80acb10031278c0be304805edc05b7e9",
	"golab_0.3.0_windows_arm64.tar.gz": "a279ad1a1356d3aa79fc0321132c21ff5872aeef33979e4fb54b39fae4f77703",
	"golab_0.3.0_windows_arm64.zip":    "ef6e4861c9d8f3f10af47248e1849bdd5564285cf181cc2757c1acc14d709918",
}

// staleV020Hashes are the v0.2.0 digests that used to live in brew/scoop.
// None of them may survive the v0.3.0 bump: reusing a v0.2.0 hash on a v0.3.0
// URL would make every channel fail verification.
var staleV020Hashes = []string{
	"689c593a910ecd9bddbc855745af54fb3679cd46c122f276b38b9cc9e1f35c04",
	"25f75b771b63dafddbf50876c53ff79cb4795b0d6ae73a4e0fbaf0598d4654c6",
	"27d58b4f0317644050d03c0f161d4f4a8afa0abe1fa965224a74bd6de7eb746e",
	"d974a79a9818edb14dbab192011078146bc9d26a1d2ec8265201db4dddfe1ed9",
	"a75742ec1e03d55cc38687f68056bff4595d639e7571349aca2f675442046af5",
	"83350bc3c1572b9e231706845220b1b5656ebb74e8e3fc444888a70e31e97b7c",
	"38f874d57623179442b7f47b2860a5e3612ded2f0a6a5c27ce21c757858ec7b8",
}

// cosignStub writes a fake cosign that reports plainOut for `cosign version`
// (jsonOut for `cosign version --output json` when non-empty) and exits
// signExit for `cosign verify-blob`. It makes the version gate and the
// signature gate independently testable without a real cosign binary.
func cosignStub(t *testing.T, binDir, plainOut, jsonOut string, signExit int) {
	t.Helper()
	body := fmt.Sprintf(`#!/usr/bin/env bash
if [[ ${1:-} == "version" ]]; then
  if [[ ${2:-} == "--output" ]]; then printf '%%s\n' %q; else printf '%%s\n' %q; fi
  exit 0
fi
exit %d
`, jsonOut, plainOut, signExit)
	if err := os.WriteFile(filepath.Join(binDir, "cosign"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInstallScriptVerificationGate(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	root := filepath.Join("..", "..")
	script := filepath.Join(root, "install.sh")
	syntax := exec.Command(bash, "-n", script)
	if out, err := syntax.CombinedOutput(); err != nil {
		t.Fatalf("install.sh syntax: %v (%s)", err, out)
	}
	raw, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, needle := range []string{
		"cosign verify-blob", "--certificate-identity-regexp", "--certificate-oidc-issuer",
		"sha256sum", "checksums.txt", "20Youssef10/golab", "--verify-only",
		// Both binaries and the flat skills bundle ship in the signed archive.
		"golab-mcp", "--no-skills", "--skills-dir", "XDG_DATA_HOME",
		"golab-skills.tar.gz", "golab-operations", "colab-mcp",
		"tar -xzf \"$tmp/$asset\" -C \"$tmp\" golab-skills.tar.gz",
		"install -m 0755", "--help", "-x \"$install_dir/$binary\"",
		// cosign >= v2.6.5 is required before verification; older clients
		// cannot read v0.3 --new-bundle-format bundles.
		"MIN_COSIGN_VERSION", "2.6.5", "require_cosign_version",
		"cosign version", "GitVersion:", "too old", "go install github.com/sigstore/cosign/v2/cmd/cosign@latest",
	} {
		if !strings.Contains(s, needle) {
			t.Fatalf("install.sh is missing %q", needle)
		}
	}
	if strings.Contains(s, "curl -fsSL") && strings.Contains(s, "| bash") {
		t.Fatal("install.sh fetches and runs unverified content")
	}
	// Verification must happen before any extraction or installation.
	verifyAt := strings.Index(s, `verify_local "$tmp/$asset" "$tmp/${asset}.sigstore.json" "$tmp/checksums.txt"`)
	extractAt := strings.Index(s, "tar -xzf")
	if verifyAt < 0 || extractAt < 0 || verifyAt > extractAt {
		t.Fatal("install.sh extracts before verifying the archive")
	}
	if runtime.GOOS == "windows" {
		// install.sh is a Unix installer: its own OS switch says "use
		// install.ps1 on Windows". Git Bash's CRLF handling and sha256sum
		// output differ from Unix, so the shell-level checksum/cosign gate
		// is exercised on Unix runners only. The static policy assertions
		// above and TestPowerShellInstallScriptPolicy still run everywhere.
		t.Skip("install.sh --verify-only requires a Unix shell")
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "golab_1.2.3_linux_amd64.tar.gz")
	artifactBytes := []byte("fake release archive")
	if err := os.WriteFile(artifact, artifactBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifactBytes)
	name := filepath.Base(artifact)
	checksums := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(checksums,
		[]byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "release.sigstore.json")
	if err := os.WriteFile(bundle, []byte("bundle"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeCosign := func(exitCode int) {
		cosignStub(t, binDir, "GitVersion: v2.6.5", "", exitCode)
	}
	runVerify := func() error {
		cmd := exec.Command(bash, script, "--verify-only", artifact, bundle, checksums)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}
	writeCosign(0)
	if err := runVerify(); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
	writeCosign(1)
	if err := runVerify(); err == nil {
		t.Fatal("invalid cosign signature was accepted")
	}
	writeCosign(0)
	if err := os.WriteFile(artifact, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runVerify(); err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
}

// TestInstallScriptCosignMinimumVersion reproduces the live finding: cosign
// v2.4.1 cannot read v0.3 --new-bundle-format bundles and fails with a
// confusing "signature verification failed". The installer must reject it
// before verification with an upgrade hint, accept v2.6.5, compare numerically
// (2.10.0 > 2.6.5), and read both `GitVersion:` and JSON output forms.
func TestInstallScriptCosignMinimumVersion(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	if runtime.GOOS == "windows" {
		t.Skip("install.sh --verify-only requires a Unix shell")
	}
	script := filepath.Join("..", "..", "install.sh")

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "golab_1.2.3_linux_amd64.tar.gz")
	artifactBytes := []byte("fake release archive")
	if err := os.WriteFile(artifact, artifactBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifactBytes)
	checksums := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(checksums,
		[]byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(artifact))), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "release.sigstore.json")
	if err := os.WriteFile(bundle, []byte("bundle"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func() (string, error) {
		cmd := exec.Command(bash, script, "--verify-only", artifact, bundle, checksums)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	cases := []struct {
		name        string
		plain, json string
		wantErr     bool
		wantText    []string
	}{
		{"old plain v2.4.1", "GitVersion: v2.4.1", "", true, []string{"too old", "v2.6.5", "upgrade"}},
		{"new plain v2.6.5", "GitVersion: v2.6.5", "", false, nil},
		{"new json v2.6.5", "", `{"GitVersion": "v2.6.5"}`, false, nil},
		{"old json v2.4.1", "", `{"GitVersion": "v2.4.1"}`, true, []string{"too old", "v2.6.5"}},
		{"numeric not lexical v2.10.0", "GitVersion: v2.10.0", "", false, nil},
		{"unparseable", "no version here", "", true, []string{"could not parse", "v2.6.5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cosignStub(t, binDir, tc.plain, tc.json, 0)
			out, err := run()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("old/unknown cosign was accepted: %s", out)
				}
				for _, needle := range tc.wantText {
					if !strings.Contains(strings.ToLower(out), strings.ToLower(needle)) {
						t.Errorf("error %q is missing %q", out, needle)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("valid cosign was rejected: %v: %s", err, out)
			}
		})
	}
}

// TestPackagingConfigsPinSignedRelease guards the package-channel configs:
// brew/scoop/nfpm and winget must all pin the PUBLISHED signed v0.3.0 release.
// Every download hash must equal publishedV030's value for its OWN asset name,
// so a right-hash/wrong-platform pairing cannot pass. nfpm packages a locally
// built binary and therefore carries no release hash.
func TestPackagingConfigsPinSignedRelease(t *testing.T) {
	base := "https://github.com/20Youssef10/golab/releases/download/v" + nextVersion

	// Build the valid digest set and the winget zip digests from the fixture,
	// never from the manifests under test.
	valid := make(map[string]bool, len(publishedV030))
	tarOnly := make(map[string]bool)
	publishedZips := make(map[string]string)
	for asset, sum := range publishedV030 {
		valid[sum] = true
		if strings.HasSuffix(asset, ".zip") {
			publishedZips[asset] = sum
		} else {
			tarOnly[sum] = true
		}
	}
	if len(publishedZips) != 3 {
		t.Fatalf("fixture has %d zip assets, want 3", len(publishedZips))
	}

	root := filepath.Join("..", "..", "packaging")
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("packaging/%s: %v", name, err)
		}
		return string(raw)
	}
	files := []string{
		"golab.rb", "golab.json", "nfpm.yaml",
		"20Youssef10.golab.installer.yaml",
		"20Youssef10.golab.locale.en-US.yaml",
		"20Youssef10.golab.yaml",
	}
	for _, name := range files {
		s := read(name)
		if !strings.Contains(s, nextVersion) {
			t.Errorf("packaging/%s is not pinned to version %s", name, nextVersion)
		}
		for _, stale := range staleV020Hashes {
			if strings.Contains(s, stale) {
				t.Errorf("packaging/%s still has stale v0.2.0 hash %s", name, stale)
			}
		}
		if strings.Contains(s, "REPLACE_AT_RELEASE") {
			t.Errorf("packaging/%s still has a release-time hash placeholder", name)
		}
	}

	// Only channels that download a release asset carry a canonical URL:
	// nfpm builds locally, and the winget version/locale manifests are metadata.
	for _, name := range []string{"golab.rb", "golab.json", "20Youssef10.golab.installer.yaml"} {
		if !strings.Contains(read(name), base) {
			t.Errorf("packaging/%s has no canonical pinned release URL %s", name, base)
		}
	}

	hex64 := regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)

	// Homebrew: four digests, each bound to its own darwin/linux asset.
	brew := read("golab.rb")
	if !strings.Contains(brew, "version '"+nextVersion+"'") {
		t.Error("packaging/golab.rb version field is not pinned to " + nextVersion)
	}
	brewPair := regexp.MustCompile(`url '(https://[^']+/golab_0\.3\.0_([a-z0-9_]+)\.tar\.gz)'\s*\n\s*sha256 '([0-9a-fA-F]{64})'`)
	brewPairs := brewPair.FindAllStringSubmatch(brew, -1)
	if len(brewPairs) != 4 {
		t.Fatalf("brew has %d url/hash pairs, want 4: %v", len(brewPairs), brewPairs)
	}
	for _, pair := range brewPairs {
		asset := "golab_0.3.0_" + pair[2] + ".tar.gz"
		hash := strings.ToLower(pair[3])
		want, ok := publishedV030[asset]
		if !ok {
			t.Errorf("brew references unpublished asset %s", asset)
			continue
		}
		if hash != want {
			t.Errorf("brew hash for %s = %s, want published %s", asset, hash, want)
		}
	}

	// Scoop: three digests for the Windows tar.gz repacks it installs.
	scoop := read("golab.json")
	if !strings.Contains(scoop, `"version": "`+nextVersion+`"`) {
		t.Error("packaging/golab.json version field is not pinned to " + nextVersion)
	}
	scoopPair := regexp.MustCompile(`"url": "[^"]+/golab_0\.3\.0_(windows_[a-z0-9_]+)\.tar\.gz",\s*"hash": "([0-9a-fA-F]{64})"`)
	scoopPairs := scoopPair.FindAllStringSubmatch(scoop, -1)
	if len(scoopPairs) != 3 {
		t.Fatalf("scoop has %d url/hash pairs, want 3: %v", len(scoopPairs), scoopPairs)
	}
	for _, pair := range scoopPairs {
		asset := "golab_0.3.0_" + pair[1] + ".tar.gz"
		hash := strings.ToLower(pair[2])
		want, ok := publishedV030[asset]
		if !ok {
			t.Errorf("scoop references unpublished asset %s", asset)
			continue
		}
		if hash != want {
			t.Errorf("scoop hash for %s = %s, want published %s", asset, hash, want)
		}
	}

	// nfpm builds the binary locally from the pinned tag; a release hash there
	// would claim a verification it cannot perform.
	nfpm := read("nfpm.yaml")
	if !strings.Contains(nfpm, "does not download") {
		t.Error("nfpm.yaml lost its no-download honesty note")
	}
	if !strings.Contains(nfpm, "version: "+nextVersion) {
		t.Error("nfpm.yaml version field is not pinned to " + nextVersion)
	}
	if got := hex64.FindAllString(nfpm, -1); len(got) != 0 {
		t.Errorf("nfpm.yaml has %d release hashes, want 0 (local build): %v", len(got), got)
	}

	// Generic guard: no channel may carry a digest absent from the fixture.
	for _, name := range files {
		for _, sum := range hex64.FindAllString(read(name), -1) {
			if !valid[strings.ToLower(sum)] {
				t.Errorf("packaging/%s has unpublished checksum %s", name, sum)
			}
		}
	}

	// winget: the three .zip repacks, each bound to its own canonical asset URL.
	winget := read("20Youssef10.golab.installer.yaml")
	if got := hex64.FindAllString(winget, -1); len(got) != 3 {
		t.Errorf("winget installer has %d SHA-256 values, want 3: %v", len(got), got)
	}
	zipPair := regexp.MustCompile(`InstallerUrl:\s*\S+/(golab_[^/\s]+\.zip)\s*\n\s*InstallerSha256:\s*([0-9a-fA-F]{64})`)
	pairs := zipPair.FindAllStringSubmatch(winget, -1)
	if len(pairs) != 3 {
		t.Fatalf("winget installer has %d URL/hash pairs, want 3", len(pairs))
	}
	for _, pair := range pairs {
		asset, hash := pair[1], strings.ToLower(pair[2])
		want, ok := publishedZips[asset]
		if !ok {
			t.Errorf("winget asset %s is not a published v0.3.0 .zip", asset)
			continue
		}
		if hash != want {
			t.Errorf("winget hash for %s = %s, want %s from signed checksums.txt", asset, hash, want)
		}
		if tarOnly[hash] {
			t.Errorf("winget hash for %s is a .tar.gz digest, not its .zip digest", asset)
		}
	}
	for _, line := range strings.Split(winget, "\n") {
		if strings.Contains(line, "InstallerUrl:") && !strings.HasSuffix(strings.TrimSpace(line), ".zip") {
			t.Errorf("winget asset must be a .zip: %s", strings.TrimSpace(line))
		}
	}
	if !strings.Contains(winget, "InstallerType: zip") {
		t.Error("winget installer must use InstallerType: zip")
	}
	for _, alias := range []string{"PortableCommandAlias: golab", "PortableCommandAlias: golab-mcp"} {
		if !strings.Contains(winget, alias) {
			t.Errorf("winget installer is missing %q", alias)
		}
	}

	// Coverage: every arch each channel installs is still present.
	coverage := map[string][]string{
		"golab.rb":                         {"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"},
		"golab.json":                       {"windows_386", "windows_amd64", "windows_arm64"},
		"20Youssef10.golab.installer.yaml": {"windows_386", "windows_amd64", "windows_arm64"},
	}
	for name, assets := range coverage {
		raw := read(name)
		ext := ".tar.gz"
		if name == "20Youssef10.golab.installer.yaml" {
			ext = ".zip"
		}
		for _, asset := range assets {
			want := "golab_" + nextVersion + "_" + asset + ext
			if !strings.Contains(raw, want) {
				t.Errorf("packaging/%s is missing asset %s", name, want)
			}
		}
	}
}

func TestPowerShellInstallScriptPolicy(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, needle := range []string{
		"Get-FileHash", "cosign verify-blob", "--certificate-identity-regexp",
		"--certificate-oidc-issuer", "checksums.txt", "20Youssef10/golab",
		// Both binaries, opt-out skills flag and the documented skill path.
		"golab-mcp.exe", "-NoSkills", "$SkillsDir", "LOCALAPPDATA",
		"golab-skills.tar.gz", "SKILL.md", "--help",
		"tar.exe -xzf $artifactPath -C $tmp golab-skills.tar.gz",
		// cosign >= v2.6.5 is required before verification. This is a static
		// check: no PowerShell runtime is available in this authoring env.
		"cosign >= v2.6.5", "Assert-CosignVersion", "cosign version",
		"GitVersion", "too old", "go install github.com/sigstore/cosign/v2/cmd/cosign@latest",
	} {
		if !strings.Contains(s, needle) {
			t.Fatalf("install.ps1 is missing %q", needle)
		}
	}
	if !strings.Contains(s, "Invoke-WebRequest") && !strings.Contains(s, "Invoke-RestMethod") {
		t.Fatal("install.ps1 has no download mechanism")
	}
	for _, forbidden := range []string{"Invoke-Expression", "iex "} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("install.ps1 executes downloaded text via %q", forbidden)
		}
	}
	verifyAt := strings.Index(s, "Verify-Release -ArtifactPath $artifactPath")
	extractAt := strings.Index(s, "tar.exe -xzf")
	if verifyAt < 0 || extractAt < 0 || verifyAt > extractAt {
		t.Fatal("install.ps1 extracts before verifying the archive")
	}
}

// TestDistributionPipelineWiring pins the release/Docker contract: the
// archive must contain both binaries and the skills, winget's .zip must come
// from the same build, every published asset must be signed and attested, and
// actions must be pinned to immutable commit SHAs.
func TestDistributionPipelineWiring(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(rel string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	goreleaser := read(".goreleaser.yaml")
	for _, needle := range []string{
		"main: ./cmd/golab-mcp", "ids: [golab, golab-mcp]",
		"formats: [tar.gz]", "formats: [tar.gz, zip]",
		"scripts/make-skills-archive.sh", "golab-skills.tar.gz",
		"name_template: checksums.txt",
		// A second archive publishes golab-mcp on its own, with the same
		// reproducible build settings and under the shared checksums.txt.
		"id: mcp-standalone", "ids: [golab-mcp]",
		`name_template: "golab-mcp_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`,
	} {
		if !strings.Contains(goreleaser, needle) {
			t.Errorf(".goreleaser.yaml is missing %q", needle)
		}
	}
	// The standalone archive must not repack the CLI binary, the skills bundle
	// or the changelog - only the MCP binary plus LICENSE/NOTICE/README.
	start := strings.Index(goreleaser, "  - id: mcp-standalone")
	end := strings.Index(goreleaser, "\nchecksum:")
	if start < 0 || end < 0 || end <= start {
		t.Fatal("cannot locate the mcp-standalone archive block")
	}
	standalone := goreleaser[start:end]
	for _, forbidden := range []string{"golab-skills.tar.gz", "CHANGELOG.md", "ids: [golab, golab-mcp]"} {
		if strings.Contains(standalone, forbidden) {
			t.Errorf("mcp-standalone archive must not contain %q", forbidden)
		}
	}
	for _, required := range []string{"LICENSE", "NOTICE", "README.md", "goos: windows"} {
		if !strings.Contains(standalone, required) {
			t.Errorf("mcp-standalone archive is missing %q", required)
		}
	}
	release := read(filepath.Join(".github", "workflows", "release.yml"))
	for _, needle := range []string{
		"dist/golab_*.zip", `^golab_.*\.zip$`, "dist/golab_*.zip.sigstore.json",
		"cosign attest-blob", "sha256sum -c checksums.txt",
		// The standalone MCP archives must be signed, attested, uploaded and
		// proved with the same identity as the CLI archives.
		"mcp_tarballs=(dist/golab-mcp_*.tar.gz)", "mcp_zips=(dist/golab-mcp_*.zip)",
		`"${mcp_tarballs[@]}"`, `'^golab-mcp_.*\.tar\.gz$'`,
		`'^golab-mcp_.*\.sbom\.sigstore\.json$'`,
		"dist/golab-mcp_*.tar.gz.sigstore.json", "dist/golab-mcp_*.sbom.sigstore.json",
		"cosign verify-blob-attestation",
	} {
		if !strings.Contains(release, needle) {
			t.Errorf("release.yml is missing %q", needle)
		}
	}
	// The updater must keep ignoring the hyphenated standalone assets: its
	// anchored pattern requires `golab_` followed by a version digit.
	app := read(filepath.Join("internal", "app", "app.go"))
	if !strings.Contains(app, `^golab_v?\d+\.\d+\.\d+.*_`) {
		t.Error("internal/app updateAssetPattern lost its anchored golab_ prefix")
	}
	dockerfile := read("Dockerfile")
	for _, needle := range []string{
		"CGO_ENABLED=0", "./cmd/golab-mcp", "/usr/share/golab/skills",
		"AS verify", "static-debian12:nonroot",
	} {
		if !strings.Contains(dockerfile, needle) {
			t.Errorf("Dockerfile is missing %q", needle)
		}
	}
	docker := read(filepath.Join(".github", "workflows", "docker.yml"))
	for _, needle := range []string{
		"contents: read", "packages: write", "provenance: mode=max",
		"ghcr.io/20youssef10/golab", "--target verify",
	} {
		if !strings.Contains(docker, needle) {
			t.Errorf("docker.yml is missing %q", needle)
		}
	}
	sha := regexp.MustCompile(`@[0-9a-f]{40}\s+# v`)
	for _, name := range []string{
		filepath.Join(".github", "workflows", "release.yml"),
		filepath.Join(".github", "workflows", "docker.yml"),
	} {
		s := read(name)
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "uses:") && !sha.MatchString(line) {
				t.Errorf("%s has an unpinned action: %s", name, strings.TrimSpace(line))
			}
		}
	}
}
