package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigDirOverride(t *testing.T) {
	t.Setenv("COLAB_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))
	got, err := ConfigDir()
	if err != nil || got != os.Getenv("COLAB_CONFIG_DIR") {
		t.Fatalf("ConfigDir() = %q, %v", got, err)
	}
}

func TestSessionsPathOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "custom.json")
	t.Setenv("COLAB_SESSIONS_FILE", p)
	got, err := SessionsPath()
	if err != nil || got != p {
		t.Fatalf("SessionsPath() = %q, %v", got, err)
	}
}

func TestTempDirPrefersTMPDIR(t *testing.T) {
	p := t.TempDir()
	t.Setenv("TMPDIR", p)
	if got := TempDir(); got != p {
		t.Fatalf("TempDir() = %q, want %q", got, p)
	}
}

func TestTTYMockable(t *testing.T) {
	t.Setenv("COLAB_TEST_TTY", "0")
	if IsStdoutTTY() {
		t.Fatal("stdout reported as TTY")
	}
	t.Setenv("COLAB_TEST_TTY", "1")
	if !IsStdoutTTY() {
		t.Fatal("stdout not reported as TTY")
	}
}

func TestRemotePathUsesForwardSlashes(t *testing.T) {
	got := RemotePath("content", "a", "b.txt")
	if got != "content/a/b.txt" {
		t.Fatalf("RemotePath = %q", got)
	}
}

func TestMakePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("0600 is a no-op on Windows")
	}
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MakePrivate(p); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestReadNameservers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(p, []byte("# c\nnameserver 1.1.1.1\nnameserver 8.8.8.8:5353\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readNameservers(p)
	if len(got) != 2 || got[0] != "1.1.1.1:53" || got[1] != "8.8.8.8:5353" {
		t.Fatalf("nameservers = %v", got)
	}
}
