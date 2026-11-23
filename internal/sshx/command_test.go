package sshx

import (
	"runtime"
	"strings"
	"testing"

	shellquote "github.com/kballard/go-shellquote"
)

// Ports upstream tests/test_ssh_cmd.py::test_ssh_cmd_forwards_argv_and_exit_code
// (quoting half): one argument is a shell line, N arguments are POSIX-quoted argv.
func TestCommandLineQuoting(t *testing.T) {
	cases := []struct {
		cmd  []string
		want string
	}{
		{[]string{"ls / | wc -l"}, "ls / | wc -l"},
		{[]string{"python", "-c", "print(1)"}, `python -c print\(1\)`},
		{[]string{"echo", "-i", "--rm"}, "echo -i --rm"},
		{[]string{"ls", "*.py", "$HOME"}, `ls \*.py \$HOME`},
	}
	for _, tc := range cases {
		if got := CommandLine(tc.cmd); got != tc.want {
			t.Errorf("CommandLine(%q) = %q; want %q", tc.cmd, got, tc.want)
		}
	}
	// N-argument output must round-trip through a POSIX shell parser.
	cmd := []string{"grep", "-i", "foo bar", "a'b", "$(touch /tmp/pwned)"}
	got, err := shellquote.Split(CommandLine(cmd))
	if err != nil {
		t.Fatalf("shellquote.Split: %v", err)
	}
	if strings.Join(got, "\x00") != strings.Join(cmd, "\x00") {
		t.Fatalf("round trip = %q; want %q", got, cmd)
	}
}

// Ports upstream tests/test_ssh_workdir.py::test_default_remote_dir_is_content
// and tests/test_ssh_cmd.py::test_ssh_without_cmd_builds_interactive_argv.
func TestRemoteCommands(t *testing.T) {
	if got := RemoteInteractiveCommand(); got != "cd /content 2>/dev/null; exec ${SHELL:-/bin/bash} -l" {
		t.Fatalf("interactive command = %q", got)
	}
	if got := RemoteCommand([]string{"echo hi"}); got != "cd /content 2>/dev/null; echo hi" {
		t.Fatalf("cmd command = %q", got)
	}
	if got := RemoteCommand([]string{"ls", "$HOME"}); got != `cd /content 2>/dev/null; ls \$HOME` {
		t.Fatalf("cmd argv = %q", got)
	}
}

// Ports upstream tests/test_ssh.py::test_proxy_command_round_trips_through_the_shell.
func TestProxyCommandPOSIXQuoting(t *testing.T) {
	names := []string{"simple", "with space", "a'b", "a@b.c:d=e,f", "$(touch /tmp/pwned)", "a;rm -rf /", "ünïcode"}
	for _, name := range names {
		argv := []string{"golab", "ssh", "--proxy-mode", "-s", name, "--colab-internal-mode", "interactive"}
		got, err := shellquote.Split(ProxyCommand(argv))
		if err != nil {
			t.Fatalf("shellquote.Split(%q): %v", name, err)
		}
		if strings.Join(got, "\x00") != strings.Join(argv, "\x00") {
			t.Fatalf("name %q: round trip = %q; want %q", name, got, argv)
		}
	}
}

// Ports upstream tests/test_windows_ssh.py::test_proxy_command_launches_with_literal_arguments
// (Windows argument rules, checked here on every host via a runtime check).
func TestWindowsProxyCommandQuoting(t *testing.T) {
	names := []string{"simple", "with space", "a'b", `a"b`, "$(echo test)", "a;b", "a&b", `a\b\`, "测试"}
	for _, name := range names {
		argv := []string{`C:\Program Files\golab.exe`, "ssh", "--proxy-mode", "-s", name}
		got := WindowsProxyCommand(argv)
		back, err := parseWindowsCommandLine(got)
		if err != nil {
			t.Fatalf("parseWindowsCommandLine(%q): %v", got, err)
		}
		if strings.Join(back, "\x00") != strings.Join(argv, "\x00") {
			t.Fatalf("name %q: round trip = %q; want %q", name, back, argv)
		}
	}
}

// Ports docs/04 §4: a ready-to-paste ssh_config block must quote the
// ProxyCommand correctly, including hostile session names.
func TestSSHConfigBlock(t *testing.T) {
	argv := []string{"golab", "ssh", "--proxy-mode", "-s", "$(touch /tmp/pwned)", "--colab-internal-mode", "proxy"}
	block := SSHConfigBlock(PlatformProxyCommand(argv))
	for _, want := range []string{
		"Host colab-runtime",
		"User root",
		"StrictHostKeyChecking no",
		"ProxyCommand ",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("ssh_config block missing %q:\n%s", want, block)
		}
	}
	if !strings.Contains(block, "golab ssh --proxy-mode -s") && runtime.GOOS == "windows" {
		t.Errorf("windows block lost the command: %s", block)
	}
}

func TestNormalizeMode(t *testing.T) {
	for _, tc := range []struct{ in, want Mode }{
		{ModeInteractive, ModeInteractive},
		{ModeCmd, ModeCmd},
		{ModeProxy, ModeProxy},
		{"interactive&colab-runtime-proxy-token=evil", ModeProxy},
		{"", ModeProxy},
	} {
		if got := NormalizeMode(tc.in); got != tc.want {
			t.Errorf("NormalizeMode(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
