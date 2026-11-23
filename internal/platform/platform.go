// Package platform contains OS-specific paths, TTY detection and the Termux
// DNS/CA shim. It is the only package allowed to branch on GOOS for user-facing
// behaviour (runtime.GOOS checks here replace build-tagged files where possible).
package platform

import (
	"context"
	"errors"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	_ "golang.org/x/crypto/x509roots/fallback" // embedded roots when the system store is empty
)

const configDirName = "colab-cli"

// ConfigDir returns the directory holding state, settings, history and logs.
func ConfigDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("COLAB_CONFIG_DIR")); v != "" {
		return v, nil
	}
	if runtime.GOOS == "windows" {
		if base := os.Getenv("APPDATA"); base != "" {
			return filepath.Join(base, configDirName), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// macOS deliberately uses the same path as the Python CLI (not ~/Library).
	return filepath.Join(home, ".config", configDirName), nil
}

// SettingsPath returns the settings.json path.
func SettingsPath() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "settings.json"), nil
}

// SessionsPath returns the sessions.json path, honouring the --config override.
// The CLI sets COLAB_SESSIONS_FILE when --config is supplied.
func SessionsPath() (string, error) {
	if v := strings.TrimSpace(os.Getenv("COLAB_SESSIONS_FILE")); v != "" {
		return v, nil
	}
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "sessions.json"), nil
}

// HistoryDir returns the directory holding per-session JSONL logs.
func HistoryDir() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "history"), nil
}

// CacheDir returns a private cache directory.
func CacheDir() (string, error) {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, configDirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", configDirName), nil
}

// TempDir prefers TMPDIR and Termux's $PREFIX/tmp over os.TempDir().
func TempDir() string {
	if v := os.Getenv("TMPDIR"); v != "" {
		return v
	}
	if IsTermux() {
		if p := os.Getenv("PREFIX"); p != "" {
			return filepath.Join(p, "tmp")
		}
	}
	return os.TempDir()
}

// IsTermux reports whether we are running under Termux.
func IsTermux() bool {
	if runtime.GOOS == "android" {
		return true
	}
	if os.Getenv("TERMUX_VERSION") != "" {
		return true
	}
	if p := os.Getenv("PREFIX"); strings.Contains(p, "com.termux") {
		return true
	}
	if _, err := os.Stat("/data/data/com.termux"); err == nil {
		return true
	}
	return false
}

// IsStdinTTY reports whether stdin is a terminal. Mockable in tests via the
// COLAB_TEST_TTY environment variable ("1"/"0").
func IsStdinTTY() bool { return tty(os.Stdin.Fd(), "COLAB_TEST_STDIN_TTY") }

// IsStdoutTTY reports whether stdout is a terminal.
func IsStdoutTTY() bool { return tty(os.Stdout.Fd(), "COLAB_TEST_TTY") }

// IsStderrTTY reports whether stderr is a terminal.
func IsStderrTTY() bool { return tty(os.Stderr.Fd(), "COLAB_TEST_STDERR_TTY") }

func tty(fd uintptr, env string) bool {
	if v := os.Getenv(env); v != "" {
		return v == "1" || strings.EqualFold(v, "true")
	}
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// MakePrivate restricts a file to its owner. On Windows 0600 is a no-op; the
// file lives in %APPDATA% which is already user-profile ACL restricted.
func MakePrivate(p string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chmod(p, 0o600)
}

// RemotePath joins VM-side path components with Linux semantics on every host.
func RemotePath(elem ...string) string { return path.Join(elem...) }

var netFixOnce sync.Once

// InstallNetFix installs the Termux resolver and CA-bundle shim. It is a no-op
// on other platforms and on Termux does nothing twice.
func InstallNetFix() { netFixOnce.Do(installNetFix) }

func installNetFix() {
	if !IsTermux() {
		return
	}
	if os.Getenv("SSL_CERT_FILE") == "" {
		p := os.Getenv("PREFIX")
		if p != "" {
			candidate := filepath.Join(p, "etc", "tls", "cert.pem")
			if _, err := os.Stat(candidate); err == nil {
				_ = os.Setenv("SSL_CERT_FILE", candidate)
			}
		}
	}
	resolv := "/etc/resolv.conf"
	if p := os.Getenv("PREFIX"); p != "" {
		resolv = filepath.Join(p, "etc", "resolv.conf")
	}
	nameservers := readNameservers(resolv)
	if len(nameservers) == 0 {
		nameservers = []string{"1.1.1.1:53", "8.8.8.8:53"}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var errs []error
			for _, ns := range nameservers {
				conn, err := dialer.DialContext(ctx, network, ns)
				if err == nil {
					return conn, nil
				}
				errs = append(errs, err)
			}
			return nil, errors.Join(errs...)
		},
	}
}

func readNameservers(p string) []string {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		host := fields[1]
		if !strings.Contains(host, ":") {
			host = net.JoinHostPort(host, "53")
		}
		out = append(out, host)
	}
	return out
}
