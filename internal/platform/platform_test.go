package platform

import (
	"flag"
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

// The COLAB_TEST_*_TTY overrides are a test-only escape hatch. Swap in a
// FlagSet without "test.v" to simulate a release binary: there the overrides
// must have no effect and real isatty detection must decide. Compare against
// the natural (unset) result so the assertion holds whether or not the test
// harness happens to attach a TTY to any stream.
func TestTTYOverridesOnlyInTestBinary(t *testing.T) {
	checks := []struct {
		name string
		set  func(string)
		read func() bool
	}{
		{"stdin", func(v string) { t.Setenv("COLAB_TEST_STDIN_TTY", v) }, IsStdinTTY},
		{"stdout", func(v string) { t.Setenv("COLAB_TEST_TTY", v) }, IsStdoutTTY},
		{"stderr", func(v string) { t.Setenv("COLAB_TEST_STDERR_TTY", v) }, IsStderrTTY},
	}

	// In the test binary the overrides are honoured.
	for _, c := range checks {
		c.set("1")
		if !c.read() {
			t.Errorf("%s override %q ignored in test binary", c.name, "1")
		}
		c.set("0")
		if c.read() {
			t.Errorf("%s override %q ignored in test binary", c.name, "0")
		}
	}

	// A release binary has no "test.v" flag; the overrides must be ignored.
	original := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet("release", flag.ContinueOnError)
	t.Cleanup(func() { flag.CommandLine = original })

	for _, c := range checks {
		c.set("")
		natural := c.read()
		c.set("1")
		if got := c.read(); got != natural {
			t.Errorf("%s override %q used in release-like binary: got %v, want natural %v", c.name, "1", got, natural)
		}
		c.set("0")
		if got := c.read(); got != natural {
			t.Errorf("%s override %q used in release-like binary: got %v, want natural %v", c.name, "0", got, natural)
		}
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
