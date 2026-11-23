package sshx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// newSSHClientConfig builds the embedded client's config. Host keys are
// ephemeral per Colab runtime, so accept-any is intentional (ADR-005). The
// channel is already TLS-authenticated to Google with a bearer token and we
// ship no known_hosts file.
func newSSHClientConfig(signers []ssh.Signer) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		// #nosec G106 -- ephemeral VM host keys; documented in ADR-005.
		//nolint:gosec // Colab runtime host keys are regenerated per runtime.
		HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { return nil },
		ClientVersion:   "SSH-2.0-golab",
		Timeout:         15 * time.Second,
	}
}

func runCommand(ctx context.Context, nc net.Conn, key KeyMaterial, cfg Config, piped bool) (int, error) {
	fd := uintptr(0)
	if f, ok := cfg.Stdin.(*os.File); ok {
		fd = f.Fd()
	}
	return runSSH(ctx, nc, key, cfg, RemoteCommand(cfg.Command), !piped, fd)
}

func runInteractive(ctx context.Context, nc net.Conn, key KeyMaterial, cfg Config) (int, error) {
	fd := uintptr(0)
	rawOK := false
	tty := cfg.StdinTTY == nil || *cfg.StdinTTY
	if f, ok := cfg.Stdin.(*os.File); ok && tty {
		if candidate, ok := rawFD(f); ok {
			fd, rawOK = candidate, true
		}
	}
	if rawOK {
		restoreVT := enableVirtualTerminal(fd)
		defer restoreVT()
		state, err := enterRawMode(fd)
		if err != nil {
			return 255, fmt.Errorf("raw terminal mode: %w", err)
		}
		// A defer runs on normal return and on panic. On Ctrl-C the signal
		// handler in Runner.Run cancels ctx, which unblocks the session and
		// reaches this defer too; console-close is covered by the Windows
		// console handler installed there.
		defer func() { _ = state.restore() }()
	}
	return runSSH(ctx, nc, key, cfg, RemoteInteractiveCommand(), true, fd)
}

func runSSH(ctx context.Context, nc net.Conn, key KeyMaterial, cfg Config,
	command string, wantPTY bool, fd uintptr) (int, error) {

	cc, chans, reqs, err := ssh.NewClientConn(nc, "colab-runtime:22", newSSHClientConfig(key.Signers))
	if err != nil {
		return 255, fmt.Errorf("SSH handshake failed: %w", err)
	}
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return 255, fmt.Errorf("SSH session failed: %w", err)
	}
	defer session.Close()

	if wantPTY {
		width, height := terminalSize(fd)
		if err := session.RequestPty("xterm-256color", height, width, nil); err != nil {
			return 255, fmt.Errorf("pty-req failed: %w", err)
		}
		sendWindowChange(session, width, height)
		if fd != 0 {
			sizes := make(chan windowSize, 1)
			go startResizeWatcher(ctx, fd, sizes)
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case sz := <-sizes:
						sendWindowChange(session, sz.width, sz.height)
					}
				}
			}()
		}
	}

	session.Stdin = cfg.Stdin
	session.Stdout = cfg.Stdout
	session.Stderr = cfg.Stderr

	err = session.Run(command)
	if err == nil {
		return 0, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.Signal() != "" {
			// OpenSSH reports a signal-killed remote command as 255.
			return 255, nil
		}
		return exitErr.ExitStatus(), nil
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return 255, nil
	}
	return 255, err
}

// sendWindowChange emits the initial and subsequent window-change requests.
func sendWindowChange(session *ssh.Session, width, height int) {
	_, _ = session.SendRequest("window-change", false,
		marshalUint32s(uint32(width), uint32(height), 0, 0))
}

func marshalUint32s(vals ...uint32) []byte {
	b := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.BigEndian.PutUint32(b[i*4:], v)
	}
	return b
}
