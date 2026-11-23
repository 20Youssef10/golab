package sshx

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/testutil/fakesshd"
)

// Ports upstream tests/test_ssh_cmd.py::test_ssh_cmd_forwards_argv_and_exit_code.
func TestRunCommandExitStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   int
	}{
		{"ok", 0, 0},
		{"error", 7, 7},
		{"signal", -1, 255},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := fakesshd.Options{ExitStatus: tc.status, Output: []byte("EXACT-OUTPUT\n")}
			if tc.status < 0 {
				opts.ExitStatus = 0
				opts.ExitSignal = "KILL"
			}
			srv := newFakeServer(t, opts)
			var out, errBuf bytes.Buffer
			rc, err := newRunner(t, Config{
				ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeCmd,
				Identity: writeKey(t), Command: []string{"python", "-c", "print(1)"},
				Stdin:    strings.NewReader(""),
				Stdout:   &out,
				Stderr:   &errBuf,
				StdinTTY: boolPtr(false), StdoutTTY: boolPtr(false),
			}).Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if rc != tc.want {
				t.Fatalf("exit code = %d; want %d (err: %q)", rc, tc.want, errBuf.String())
			}
			if out.String() != "EXACT-OUTPUT\n" {
				t.Fatalf("stdout = %q; want byte-exact %q", out.String(), "EXACT-OUTPUT\n")
			}
			// The remote command must land in /content.
			var execCmd string
			for _, ev := range srv.Events() {
				if ev.Type == "exec" {
					execCmd = ev.Command
				}
			}
			if !strings.Contains(execCmd, "cd /content 2>/dev/null; ") {
				t.Fatalf("remote command = %q", execCmd)
			}
			if !strings.Contains(execCmd, `python -c print\(1\)`) {
				t.Fatalf("argv not POSIX-quoted: %q", execCmd)
			}
		})
	}
}

// Ports upstream tests/test_ssh_cmd.py::test_ssh_cmd_with_closed_stdio_runs_without_pty.
func TestRunCommandPTYOnlyWhenBothTTY(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stdin   bool
		stdout  bool
		wantPTY bool
	}{
		{"both-tty", true, true, true},
		{"stdout-piped", true, false, false},
		{"stdin-piped", false, true, false},
		{"both-piped", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeServer(t, fakesshd.Options{})
			_, err := newRunner(t, Config{
				ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeCmd,
				Identity: writeKey(t), Command: []string{"true"},
				Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
				StdinTTY: boolPtr(tc.stdin), StdoutTTY: boolPtr(tc.stdout),
			}).Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			gotPTY := false
			for _, ev := range srv.Events() {
				if ev.Type == "pty-req" {
					gotPTY = true
				}
			}
			if gotPTY != tc.wantPTY {
				t.Fatalf("pty-req = %v; want %v (events: %+v)", gotPTY, tc.wantPTY, srv.Events())
			}
		})
	}
}

// Ports upstream tests/test_ssh_cmd.py::test_ssh_cmd_forwards_argv_and_exit_code
// (byte-exactness half): a single argument is passed as a shell line verbatim.
func TestRunCommandSingleArgShellLine(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{})
	_, err := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeCmd,
		Identity: writeKey(t), Command: []string{"ls / | wc -l"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		StdinTTY: boolPtr(false), StdoutTTY: boolPtr(false),
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var cmd string
	for _, ev := range srv.Events() {
		if ev.Type == "exec" {
			cmd = ev.Command
		}
	}
	if cmd != "cd /content 2>/dev/null; ls / | wc -l" {
		t.Fatalf("remote command = %q", cmd)
	}
}

func TestRunCommandTransportError(t *testing.T) {
	srv := newFakeServer(t, fakesshd.Options{Failure: &fakesshd.Failure{Status: 502, Body: "sshd unreachable"}})
	_, err := newRunner(t, Config{
		ProxyURL: srv.URL(), Token: logx.NewSecret("TOK"), Mode: ModeCmd,
		Identity: writePub(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIuser host"),
		Command:  []string{"true"}, Stdin: strings.NewReader(""),
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	}).Run(context.Background())
	var he *HandshakeError
	if !errors.As(err, &he) || he.Status != 502 {
		t.Fatalf("want *HandshakeError 502, got %T: %v", err, err)
	}
}

func boolPtr(b bool) *bool { return &b }
