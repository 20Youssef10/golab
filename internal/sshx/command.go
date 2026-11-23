package sshx

import (
	"fmt"
	"runtime"
	"strings"

	shellquote "github.com/kballard/go-shellquote"
)

// Mode is the SSH mode tag sent in the second colab-client-agent query value.
type Mode string

const (
	ModeInteractive Mode = "interactive"
	ModeProxy       Mode = "proxy"
	ModeCmd         Mode = "cmd"
)

// NormalizeMode maps unknown modes to proxy, as upstream does for its hidden
// --colab-internal-mode option.
func NormalizeMode(mode Mode) Mode {
	switch mode {
	case ModeInteractive, ModeCmd, ModeProxy:
		return mode
	default:
		return ModeProxy
	}
}

// CommandLine renders CMD for the remote shell. One argument is a shell line
// (as with OpenSSH); several are POSIX-quoted into an exact argv (upstream
// shlex.join).
func CommandLine(cmd []string) string {
	switch len(cmd) {
	case 0:
		return ""
	case 1:
		return cmd[0]
	default:
		return shellquote.Join(cmd...)
	}
}

// RemoteInteractiveCommand lands the login shell in Colab's working dir.
const remoteInteractiveCommand = "cd /content 2>/dev/null; exec ${SHELL:-/bin/bash} -l"

// RemoteInteractiveCommand is the remote command for an interactive session.
func RemoteInteractiveCommand() string { return remoteInteractiveCommand }

// RemoteCommand builds the remote command for `colab ssh -- CMD`.
func RemoteCommand(cmd []string) string {
	return "cd /content 2>/dev/null; " + CommandLine(cmd)
}

// ProxyCommand quotes argv for /bin/sh (POSIX) so OpenSSH can invoke this CLI
// as a ProxyCommand. Every word survives word-splitting verbatim.
func ProxyCommand(argv []string) string {
	return shellquote.Join(argv...)
}

// WindowsProxyCommand quotes argv with the CommandLineToArgvW rules used by
// Windows CreateProcessW. On Windows, callers should use this instead of
// ProxyCommand (upstream subprocess.list2cmdline).
func WindowsProxyCommand(argv []string) string {
	var b strings.Builder
	for i, arg := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		needsQuote := arg == "" || strings.ContainsAny(arg, " \t\r\n\v\f\"")
		if !needsQuote {
			b.WriteString(arg)
			continue
		}
		b.WriteByte('"')
		backslashes := 0
		for _, ch := range arg {
			if ch == '\\' {
				backslashes++
				continue
			}
			if ch == '"' {
				b.WriteString(strings.Repeat(`\`, backslashes*2+1))
				b.WriteByte('"')
				backslashes = 0
				continue
			}
			b.WriteString(strings.Repeat(`\`, backslashes))
			b.WriteRune(ch)
			backslashes = 0
		}
		// Backslashes immediately before the closing quote must be doubled.
		b.WriteString(strings.Repeat(`\`, backslashes*2))
		b.WriteByte('"')
	}
	return b.String()
}

// PlatformProxyCommand picks the quoting rule for the current OS.
func PlatformProxyCommand(argv []string) string {
	if runtime.GOOS == "windows" {
		return WindowsProxyCommand(argv)
	}
	return ProxyCommand(argv)
}

// SSHConfigBlock emits a ready-to-paste ~/.ssh/config block for the abstract
// host colab-runtime, with a correctly quoted ProxyCommand (doc 04 §4).
func SSHConfigBlock(proxyCommand string) string {
	null := "/dev/null"
	if runtime.GOOS == "windows" {
		null = "NUL"
	}
	return fmt.Sprintf("Host colab-runtime\n"+
		"    HostName colab-runtime\n"+
		"    User root\n"+
		"    StrictHostKeyChecking no\n"+
		"    UserKnownHostsFile %s\n"+
		"    LogLevel ERROR\n"+
		"    ProxyCommand %s\n", null, proxyCommand)
}

// SSHConfigValue quotes a value for an OpenSSH config file line.
func SSHConfigValue(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\"'\\#$") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
	}
	return v
}
