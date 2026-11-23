package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/history"
	"github.com/20Youssef10/golab/internal/testutil/fakecolab"
	"github.com/20Youssef10/golab/internal/version"
)

// Ports upstream tests/test_cli.py::test_cli_ls.
func TestLs(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	fc.dirs["content"] = []string{"z.txt", "sub/", "a.txt"}

	if code := h.run(t, "", "ls", "-s", "s1", "content"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	want := "sub/\na.txt\nz.txt\n"
	if h.stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", h.stdout.String(), want)
	}
}

// Ports upstream tests/test_cli.py::test_cli_rm, test_cli_upload and
// test_cli_download.
func TestFileMutations(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	fc := newFakeContents(t)
	h.attachContents(t, fc)
	fc.addFile("remote.txt", "remote bytes")

	local := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(local, []byte("local bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run(t, "", "upload", "-s", "s1", local, "uploaded.txt"); code != 0 {
		t.Fatalf("upload exit=%d stderr=%q", code, h.stderr.String())
	}
	if got := fc.file("uploaded.txt"); got != "local bytes" {
		t.Fatalf("uploaded = %q", got)
	}
	if !strings.Contains(h.stderr.String(), "Uploaded") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}

	h.stdout.Reset()
	h.stderr.Reset()
	dst := filepath.Join(t.TempDir(), "downloaded.txt")
	if code := h.run(t, "", "download", "-s", "s1", "remote.txt", dst); code != 0 {
		t.Fatalf("download exit=%d stderr=%q", code, h.stderr.String())
	}
	b, err := os.ReadFile(dst)
	if err != nil || string(b) != "remote bytes" {
		t.Fatalf("downloaded = %q, %v", b, err)
	}

	h.stdout.Reset()
	h.stderr.Reset()
	if code := h.run(t, "", "rm", "-s", "s1", "remote.txt"); code != 0 {
		t.Fatalf("rm exit=%d stderr=%q", code, h.stderr.String())
	}
	if fc.hasFile("remote.txt") {
		t.Fatal("file not removed")
	}
}

// Ports upstream tests/test_cli.py::test_cli_edit_no_changes and
// test_cli_edit_with_changes.
func TestEdit(t *testing.T) {
	t.Run("no changes", func(t *testing.T) {
		t.Setenv("EDITOR", writeEditorScript(t, ""))
		h := newHarness(t)
		h.putSession(t, "s1")
		fc := newFakeContents(t)
		h.attachContents(t, fc)
		fc.addFile("note.txt", "unchanged")
		if code := h.run(t, "", "edit", "-s", "s1", "note.txt"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stderr.String(), "No changes made") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
		if len(fc.puts) != 0 {
			t.Fatalf("unexpected uploads: %v", fc.puts)
		}
	})
	t.Run("changed", func(t *testing.T) {
		t.Setenv("EDITOR", writeEditorScript(t, "!"))
		h := newHarness(t)
		h.putSession(t, "s1")
		fc := newFakeContents(t)
		h.attachContents(t, fc)
		fc.addFile("note.txt", "original")
		if code := h.run(t, "", "edit", "-s", "s1", "note.txt"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stderr.String(), "Edited and uploaded") {
			t.Fatalf("stderr = %q", h.stderr.String())
		}
		if got := fc.file("note.txt"); got != "original!" {
			t.Fatalf("uploaded = %q", got)
		}
	})
	t.Run("404 is a new file", func(t *testing.T) {
		t.Setenv("EDITOR", writeEditorScript(t, "new"))
		h := newHarness(t)
		h.putSession(t, "s1")
		fc := newFakeContents(t)
		h.attachContents(t, fc)
		if code := h.run(t, "", "edit", "-s", "s1", "fresh.txt"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		if got := fc.file("fresh.txt"); got != "new" {
			t.Fatalf("uploaded = %q", got)
		}
	})
}

// Ports upstream tests/test_cli.py::test_cli_usage and test_cli_usage_fetch_failure.
func TestUsage(t *testing.T) {
	t.Run("format", func(t *testing.T) {
		h := newHarness(t)
		h.ctrl.SetConsumption(123.45, 0.07, 1)
		if code := h.run(t, "", "usage"); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
		want := "Current balance: 123.45 compute units\nUsage rate: 0.07/hr\nActive assignments: 1"
		if !strings.Contains(h.stdout.String(), want) {
			t.Fatalf("stdout = %q", h.stdout.String())
		}
	})
	t.Run("fetch failure", func(t *testing.T) {
		h := newHarness(t)
		h.ctrl.SetFault(fakecolab.Fault{Method: "GET", PathContains: "/ccu-info", Status: 500})
		code := h.run(t, "", "usage")
		if code != 1 || !strings.Contains(h.stderr.String(), "failed to fetch compute-unit info") {
			t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
		}
	})
}

// Ports upstream tests/test_url.py::test_url_explicit_session,
// test_url_custom_host and test_url_output_is_pipeable.
func TestURL(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	if code := h.run(t, "", "url", "-s", "s1"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	want := "https://colab.research.google.com/notebooks/empty.ipynb?dbu=%2Ftun%2Fm%2Fendpoint-test#datalabBackendUrl=https://colab.research.google.com/tun/m/endpoint-test"
	if strings.TrimSpace(h.stdout.String()) != want {
		t.Fatalf("stdout = %q, want %q", h.stdout.String(), want)
	}
	if h.stderr.String() != "" {
		t.Fatalf("stderr = %q", h.stderr.String())
	}

	h.stdout.Reset()
	if code := h.run(t, "", "url", "-s", "s1", "--host", "https://colab.example.com/"); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(h.stdout.String(), "https://colab.example.com/notebooks/empty.ipynb") ||
		!strings.Contains(h.stdout.String(), "datalabBackendUrl=https://colab.example.com/tun/m/endpoint-test") {
		t.Fatalf("stdout = %q", h.stdout.String())
	}
}

// Ports upstream tests/test_version.py::test_version_installed.
func TestVersion(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "version"); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if strings.TrimSpace(h.stdout.String()) != version.String() {
		t.Fatalf("stdout = %q", h.stdout.String())
	}
}

// Ports upstream tests/test_cli.py::test_cli_help,
// test_cli_help_commands_sorted_alphabetically and test_cli_no_args.
func TestHelp(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "help"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	for _, cmd := range []string{"completion", "download", "exec", "new", "repl", "run", "sessions", "status", "stop", "usage", "url", "version"} {
		if !strings.Contains(h.stdout.String(), cmd) {
			t.Fatalf("help missing %q: %q", cmd, h.stdout.String())
		}
	}
	if strings.Index(h.stdout.String(), "completion") > strings.Index(h.stdout.String(), "download") {
		t.Fatalf("commands not alphabetical: %q", h.stdout.String())
	}

	h.stdout.Reset()
	h.stderr.Reset()
	if code := h.run(t, "", "help", "new"); code != 0 || !strings.Contains(h.stdout.String(), "--high-mem") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, h.stdout.String(), h.stderr.String())
	}

	h.stdout.Reset()
	h.stderr.Reset()
	if code := h.run(t, "", "help", "nope"); code != 2 || !strings.Contains(h.stderr.String(), "No such command 'nope'") {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
}

// Ports docs/05: shell completion command (new in the Go port).
func TestCompletion(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			h := newHarness(t)
			if code := h.run(t, "", "completion", shell); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
			}
			if h.stdout.Len() == 0 {
				t.Fatal("no completion script produced")
			}
		})
	}
	h := newHarness(t)
	if code := h.run(t, "", "completion", "tcsh"); code != 2 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
}

// Ports upstream tests/test_cli_log.py::test_cli_log_list.
func TestLogList(t *testing.T) {
	h := newHarness(t)
	h.addHistory(t, "s1", history.Event{EventType: history.EventExecution})
	if code := h.run(t, "", "log"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "s1") {
		t.Fatalf("stdout = %q", h.stdout.String())
	}
}
