package sshx

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/platform"
)

// Config is the plain-value input to Runner. It deliberately has no session
// store or CLI dependency: the caller resolves the session and auto-create.
type Config struct {
	// ProxyURL is the runtime proxy base URL (https://… or http://… for tests).
	ProxyURL string
	// Token is the data-plane runtime proxy token.
	Token logx.Secret
	// Mode selects interactive / cmd / proxy (unknown ⇒ proxy).
	Mode Mode
	// Identity is --identity (a .pub or private key path); empty discovers keys.
	Identity string
	// Command is `[-- CMD…]`: one word is a shell line, many are POSIX argv.
	Command []string

	// Stdin/Stdout/Stderr default to the process streams. In proxy mode stdout
	// is the raw SSH byte stream; all diagnostics go to stderr.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// StdinTTY/StdoutTTY override platform TTY detection (nil ⇒ auto-detect).
	// `-- CMD` uses a PTY only when BOTH are terminals.
	StdinTTY  *bool
	StdoutTTY *bool

	// Teardown is the --rm callback. sshx accepts a function rather than
	// importing internal/state. Diagnostics from it are written to Stderr.
	Teardown func(context.Context) error

	// ClientAgent defaults to DefaultClientAgent(). HandshakeTimeout bounds the
	// WebSocket upgrade (default 30 s). HTTPClient overrides the default one.
	ClientAgent      string
	HandshakeTimeout time.Duration
	HTTPClient       *http.Client

	// Logger defaults to logx.Logger().
	Logger *slog.Logger

	// HostKeyStore supplies TOFU host-key persistence for the embedded SSH
	// client. When nil, a file store in the config dir is used. It is an
	// interface of plain values so sshx stays free of state/CLI dependencies.
	HostKeyStore HostKeyStore
}

// Runner executes one SSH connection. Construct a fresh Runner per command.
type Runner struct {
	cfg    Config
	logger *slog.Logger
}

// New validates and fills in defaults.
func New(cfg Config) (*Runner, error) {
	if cfg.Stdin == nil {
		cfg.Stdin = os.Stdin
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.ClientAgent == "" {
		cfg.ClientAgent = DefaultClientAgent()
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 30 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = logx.Logger()
	}
	return &Runner{cfg: cfg, logger: cfg.Logger}, nil
}

func (r *Runner) stdinTTY() bool {
	if r.cfg.StdinTTY != nil {
		return *r.cfg.StdinTTY
	}
	return platform.IsStdinTTY()
}

func (r *Runner) stdoutTTY() bool {
	if r.cfg.StdoutTTY != nil {
		return *r.cfg.StdoutTTY
	}
	return platform.IsStdoutTTY()
}
