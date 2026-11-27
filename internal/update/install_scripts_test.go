package update

// The installers must never fetch-and-run unverified code. Static assertions
// pin the policy; the local --verify-only mode of install.sh is exercised with
// a fake cosign so the checksum/signature gate is tested, not just documented.

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

	"github.com/20Youssef10/golab/internal/version"
)

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
	} {
		if !strings.Contains(s, needle) {
			t.Fatalf("install.sh is missing %q", needle)
		}
	}
	if strings.Contains(s, "curl -fsSL") && strings.Contains(s, "| bash") {
		t.Fatal("install.sh fetches and runs unverified content")
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
		body := fmt.Sprintf("#!/usr/bin/env bash\nexit %d\n", exitCode)
		if err := os.WriteFile(filepath.Join(binDir, "cosign"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
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

// TestPackagingConfigsPinSignedRelease guards the package-channel configs:
// every hash must be one actually published in v<version>/checksums.txt and
// every download URL must be the canonical pinned release URL. It is the
// static companion to TestInstallScriptVerificationGate - a channel must not
// silently drift to an unverified URL or a placeholder checksum.
func TestPackagingConfigsPinSignedRelease(t *testing.T) {
	// Published checksums.txt for the current version, recorded here so the
	// test needs no network. Bump these together with internal/version.Number
	// and the packaging configs when a new release is cut.
	published := []string{
		"689c593a910ecd9bddbc855745af54fb3679cd46c122f276b38b9cc9e1f35c04",
		"25f75b771b63dafddbf50876c53ff79cb4795b0d6ae73a4e0fbaf0598d4654c6",
		"c663f1e9657db5e19616686f86263f37391e865ca5e5f21d386b583841fd5c66",
		"3b5e88f9edb8af9a6142083359e621674dd5f5e17880859925ff24b4d38bf9ce",
		"89d6c99b4ff1e82ebcdaf42facfaf0b9252d7dcb3f8f9b5d8fc1d0a269a25dc5",
		"8c074d239cbc62419e4cd246ae67f46222ea2e4bfe6bac74379b4174e4442aa0",
		"27d58b4f0317644050d03c0f161d4f4a8afa0abe1fa965224a74bd6de7eb746e",
		"3fc7bf066d097c46ea5a7cb9b604acfe0fcf31f3d5b9a8f9063991d7dcef4867",
		"d974a79a9818edb14dbab192011078146bc9d26a1d2ec8265201db4dddfe1ed9",
		"1da316b67a03acb752d675d20815d69b2a707d038cd3df925155a60d9a07420f",
		"38f874d57623179442b7f47b2860a5e3612ded2f0a6a5c27ce21c757858ec7b8",
		"a75742ec1e03d55cc38687f68056bff4595d639e7571349aca2f675442046af5",
		"83350bc3c1572b9e231706845220b1b5656ebb74e8e3fc444888a70e31e97b7c",
	}
	valid := make(map[string]bool, len(published))
	for _, sum := range published {
		valid[sum] = true
	}

	ver := version.Number
	base := "https://github.com/20Youssef10/golab/releases/download/v" + ver
	files := []string{
		"golab.rb", "golab.json",
		"20Youssef10.golab.installer.yaml",
		"20Youssef10.golab.locale.en-US.yaml",
		"20Youssef10.golab.yaml", "nfpm.yaml",
	}
	// Only channels that download a release asset carry a URL: nfpm packages
	// the locally built binary, and the winget version/locale manifests hold
	// metadata only.
	downloads := map[string]bool{
		"golab.rb": true, "golab.json": true,
		"20Youssef10.golab.installer.yaml": true,
	}
	hex64 := regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", name))
		if err != nil {
			t.Errorf("packaging/%s: %v", name, err)
			continue
		}
		s := string(raw)
		if !strings.Contains(s, ver) {
			t.Errorf("packaging/%s is not pinned to version %s", name, ver)
		}
		if downloads[name] && !strings.Contains(s, base) {
			t.Errorf("packaging/%s has no canonical pinned release URL %s", name, base)
		}
		for _, sum := range hex64.FindAllString(s, -1) {
			if !valid[strings.ToLower(sum)] {
				t.Errorf("packaging/%s has unpublished checksum %s", name, sum)
			}
		}
	}
	coverage := map[string][]string{
		"golab.rb":                         {"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"},
		"golab.json":                       {"windows_386", "windows_amd64", "windows_arm64"},
		"20Youssef10.golab.installer.yaml": {"windows_386", "windows_amd64", "windows_arm64"},
	}
	for name, assets := range coverage {
		raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", name))
		if err != nil {
			continue // already reported above
		}
		for _, asset := range assets {
			if !strings.Contains(string(raw), "golab_"+ver+"_"+asset+".tar.gz") {
				t.Errorf("packaging/%s is missing asset %s", name, asset)
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
}
