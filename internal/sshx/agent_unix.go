//go:build !windows

package sshx

import (
	"context"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// systemAgentSigners talks to the OpenSSH agent over SSH_AUTH_SOCK.
func systemAgentSigners() []ssh.Signer {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", sock)
	if err != nil {
		return nil
	}
	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		_ = conn.Close()
		return nil
	}
	// The signers retain the agent client (and its connection); keep it open
	// for the lifetime of the process, like OpenSSH does.
	return signers
}
