package sshx

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// KeyMaterial is the result of key discovery. PublicLine is sent verbatim in
// X-Colab-Ssh-Pubkey; Signers are used by the embedded SSH client. A public-only
// identity yields no signers (the agent, if present, supplies them).
type KeyMaterial struct {
	PublicLine string
	Source     string
	Signers    []ssh.Signer
}

const unsupportedKeyHint = "RSA keys (ssh-rsa) are not accepted by Colab; " +
	"generate an Ed25519 key with `ssh-keygen -t ed25519` and re-run with --identity."

// PublicKeyType returns the accepted key-type token at the start of line, or
// ErrUnsupportedKeyType for RSA and unknown algorithms. It does not otherwise
// validate the base64 body: upstream sends the line verbatim and lets the
// server validate it.
func PublicKeyType(line string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return "", fmt.Errorf("%w: empty public key", ErrUnsupportedKeyType)
	}
	typ := fields[0]
	switch typ {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		return typ, nil
	case ssh.KeyAlgoRSA:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedKeyType, unsupportedKeyHint)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedKeyType, typ)
	}
}

// ResolveKey implements the documented discovery order (ADR-005):
// --identity → ssh-agent → ~/.ssh/id_ed25519 → id_ecdsa.
// Private-key permission warnings are written to os.Stderr.
func ResolveKey(identity string) (KeyMaterial, error) {
	return ResolveKeyWithWriter(identity, os.Stderr)
}

// ResolveKeyWithWriter is ResolveKey with a caller-supplied destination for
// private-key permission warnings; a nil writer falls back to os.Stderr.
func ResolveKeyWithWriter(identity string, warn io.Writer) (KeyMaterial, error) {
	if warn == nil {
		warn = os.Stderr
	}
	if identity != "" {
		return resolveIdentity(identity, warn)
	}
	// Agent keys come before key files, matching ADR-005.
	if signers := agentSigners(); len(signers) > 0 {
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signers[0].PublicKey())))
		typ, err := PublicKeyType(line)
		if err == nil {
			_ = typ
			return KeyMaterial{PublicLine: line, Source: "agent", Signers: signers}, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return KeyMaterial{}, fmt.Errorf("%w: home directory: %w", ErrNoSSHKey, err)
	}
	for _, name := range []string{"id_ed25519", "id_ecdsa"} {
		priv := filepath.Join(home, ".ssh", name)
		pub := priv + ".pub"
		if info, err := os.Stat(priv); err == nil && !info.IsDir() {
			warnLooseKeyPerms(warn, priv, info.Mode())
			line, err := DerivePublicLine(priv)
			if err != nil {
				return KeyMaterial{}, err
			}
			km := KeyMaterial{PublicLine: line, Source: name}
			b, err := mustRead(priv)
			if err != nil {
				return KeyMaterial{}, err
			}
			if signer, err := ssh.ParsePrivateKey(b); err == nil {
				km.Signers = append(km.Signers, signer)
			} else {
				km.Signers = append(km.Signers, agentSigners()...)
			}
			return km, nil
		}
		if info, err := os.Stat(pub); err == nil && !info.IsDir() {
			b, err := mustRead(pub)
			if err != nil {
				return KeyMaterial{}, err
			}
			line := strings.TrimSpace(string(b))
			if line == "" {
				continue
			}
			if _, err := PublicKeyType(line); err != nil {
				return KeyMaterial{}, err
			}
			return KeyMaterial{
				PublicLine: line, Source: name, Signers: agentSigners(),
			}, nil
		}
	}
	return KeyMaterial{}, fmt.Errorf("%w: no SSH public key found in ~/.ssh/. "+
		"Run `ssh-keygen -t ed25519` to generate one, or pass --identity", ErrNoSSHKey)
}

func resolveIdentity(identity string, warn io.Writer) (KeyMaterial, error) {
	expanded := expandHome(identity)
	var privPath, pubPath string
	if strings.HasSuffix(strings.ToLower(expanded), ".pub") {
		pubPath = expanded
		privPath = strings.TrimSuffix(expanded, filepath.Ext(expanded))
	} else {
		privPath = expanded
		pubPath = expanded + ".pub"
	}
	if b, err := os.ReadFile(pubPath); err == nil {
		if line := strings.TrimSpace(string(b)); line != "" {
			if _, err := PublicKeyType(line); err != nil {
				return KeyMaterial{}, err
			}
			km := KeyMaterial{PublicLine: line, Source: "identity", Signers: agentSigners()}
			// A private key alongside the .pub is preferred for signing.
			if info, err := os.Stat(privPath); err == nil {
				warnLooseKeyPerms(warn, privPath, info.Mode())
				b, err := mustRead(privPath)
				if err != nil {
					return KeyMaterial{}, err
				}
				if signer, err := ssh.ParsePrivateKey(b); err == nil {
					km.Signers = append([]ssh.Signer{signer}, km.Signers...)
				}
			}
			return km, nil
		}
	}
	if info, err := os.Stat(privPath); err == nil {
		warnLooseKeyPerms(warn, privPath, info.Mode())
		line, err := DerivePublicLine(privPath)
		if err != nil {
			return KeyMaterial{}, err
		}
		km := KeyMaterial{PublicLine: line, Source: "identity"}
		b, err := mustRead(privPath)
		if err != nil {
			return KeyMaterial{}, err
		}
		if signer, err := ssh.ParsePrivateKey(b); err == nil {
			km.Signers = append(km.Signers, signer)
		} else {
			km.Signers = append(km.Signers, agentSigners()...)
		}
		return km, nil
	}
	if _, err := os.Stat(identity); err != nil {
		return KeyMaterial{}, &KeyError{Path: identity, Reason: "file not found."}
	}
	return KeyMaterial{}, &KeyError{Path: identity, Reason: "no public key could be derived."}
}

// warnLooseKeyPerms warns without refusing the key: group/other-accessible
// private keys are an information-disclosure risk, but refusing them would
// break legitimate shared-home setups.
func warnLooseKeyPerms(w io.Writer, path string, mode os.FileMode) {
	if mode.Perm()&0o077 == 0 {
		return
	}
	fmt.Fprintf(w, "[colab] warning: private key %s has mode %04o (group/other can access it); "+
		"run `chmod 600 %s` to restrict access\n", path, mode.Perm(), path)
}

// mustRead reads a key file and reports failures as a typed *KeyError instead
// of silently returning nil bytes.
func mustRead(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, &KeyError{Path: path, Reason: "unable to read key file.", Err: err}
	}
	return b, nil
}

func expandHome(p string) string {
	if p == "~" {
		h, _ := os.UserHomeDir()
		return h
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}

// DerivePublicLine derives the public-key line natively from a private key —
// no ssh-keygen subprocess (ADR-005). It handles OpenSSH's native format
// (including bcrypt-encrypted keys, whose public blob is stored in cleartext),
// PKCS#8 and SEC1 EC keys.
func DerivePublicLine(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", &KeyError{Path: path, Reason: "file not found."}
	}
	pub, err := publicKeyFromPrivateBytes(b)
	if err != nil {
		return "", &KeyError{Path: path, Reason: err.Error(), Err: err}
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if _, err := PublicKeyType(strings.Fields(line)[0]); err != nil {
		return "", &KeyError{Path: path, Reason: err.Error(), Err: err}
	}
	return line, nil
}

func publicKeyFromPrivateBytes(b []byte) (ssh.PublicKey, error) {
	trimmed := strings.TrimSpace(string(b))
	if strings.HasPrefix(trimmed, "ssh-") || strings.HasPrefix(trimmed, "ecdsa-") {
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(trimmed))
		return pub, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("not a recognised private key file")
	}
	switch block.Type {
	case "OPENSSH PRIVATE KEY":
		return opensshPublicBlob(block.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		if _, ok := k.(*rsa.PrivateKey); ok {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedKeyType, unsupportedKeyHint)
		}
		return ssh.NewPublicKey(k)
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		return ssh.NewPublicKey(k)
	case "RSA PRIVATE KEY":
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedKeyType, unsupportedKeyHint)
	default:
		return nil, fmt.Errorf("unsupported PEM block %q", block.Type)
	}
}

// opensshPublicBlob extracts the public key from the "openssh-key-v1" header.
// The nkeys public blobs are stored unencrypted even when the private section
// is protected by bcrypt, so no passphrase is needed for the header.
func opensshPublicBlob(b []byte) (ssh.PublicKey, error) {
	magic := []byte("openssh-key-v1\x00")
	if len(b) < len(magic) || string(b[:len(magic)]) != string(magic) {
		return nil, errors.New("bad OpenSSH private key magic")
	}
	off := len(magic)
	if _, err := sshReadString(b, &off); err != nil { // ciphername
		return nil, err
	}
	if _, err := sshReadString(b, &off); err != nil { // kdfname
		return nil, err
	}
	if _, err := sshReadString(b, &off); err != nil { // kdfoptions
		return nil, err
	}
	if off+4 > len(b) {
		return nil, io.ErrUnexpectedEOF
	}
	n := binary.BigEndian.Uint32(b[off : off+4])
	off += 4
	if n < 1 {
		return nil, errors.New("OpenSSH key has no public key")
	}
	blob, err := sshReadString(b, &off)
	if err != nil {
		return nil, err
	}
	pub, err := ssh.ParsePublicKey(blob)
	if err != nil {
		return nil, err
	}
	if _, err := PublicKeyType(strings.Fields(string(ssh.MarshalAuthorizedKey(pub)))[0]); err != nil {
		return nil, err
	}
	return pub, nil
}

func sshReadString(b []byte, off *int) ([]byte, error) {
	if *off+4 > len(b) {
		return nil, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint32(b[*off : *off+4]))
	*off += 4
	if n < 0 || *off+n > len(b) {
		return nil, io.ErrUnexpectedEOF
	}
	out := b[*off : *off+n]
	*off += n
	return out, nil
}

// agentSigners returns the agent's usable signers. systemAgentSigners is
// build-tagged so Windows can also consult the named pipe and Pageant.
// agentSignersHook is a test seam; production code always leaves it nil.
var agentSignersHook func() []ssh.Signer

// suppressAgentForTest reports whether the test-only COLAB_TEST_AGENT control
// is active. It requires a Go test binary ("test.v") or an explicit
// COLAB_TESTING=1 opt-in: an ambient environment variable must not be able to
// flip agent behaviour in a release binary (§4.11).
func suppressAgentForTest() bool {
	if flag.CommandLine.Lookup("test.v") == nil && os.Getenv("COLAB_TESTING") != "1" {
		return false
	}
	return os.Getenv("COLAB_TEST_AGENT") == "0"
}

func agentSigners() []ssh.Signer {
	if agentSignersHook != nil {
		return agentSignersHook()
	}
	if suppressAgentForTest() {
		return nil
	}
	var out []ssh.Signer
	for _, s := range systemAgentSigners() {
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
		if _, err := PublicKeyType(line); err == nil {
			out = append(out, s)
		}
	}
	return out
}
