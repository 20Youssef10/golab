//go:build windows

package sshx

import (
	pageant "github.com/davidmz/go-pageant"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// systemAgentSigners consults the Windows OpenSSH agent named pipe and then
// Pageant (doc 04 §2, ADR-005).
func systemAgentSigners() []ssh.Signer {
	var out []ssh.Signer
	if conn, err := dialNamedPipe(`\\.\pipe\openssh-ssh-agent`); err == nil {
		if signers, err := agent.NewClient(conn).Signers(); err == nil {
			out = append(out, signers...)
		} else {
			_ = conn.Close()
		}
	}
	if pageant.Available() {
		if signers, err := pageant.New().Signers(); err == nil {
			out = append(out, signers...)
		}
	}
	return out
}
