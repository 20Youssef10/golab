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
	"strings"
	"testing"
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
