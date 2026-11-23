package sshx

import (
	"context"
	"os"
	"sync/atomic"
)

// Run performs the whole connection: discover the key, open the WebSocket,
// dispatch by mode, and run --rm teardown on every exit path (including a
// termination signal).
//
// Exit codes mirror upstream: 2 for a local key error, 1 for a connection or
// handshake error, the remote status for `-- CMD`, 130 when a piped command is
// interrupted, and 0 for a clean interactive/proxy bridge.
func (r *Runner) Run(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return r.interruptedExitCode(), nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	mode := NormalizeMode(r.cfg.Mode)
	if mode == ModeCmd && len(r.cfg.Command) == 0 {
		mode = ModeInteractive // bare `ssh --`
	}
	piped := !r.stdinTTY() || !r.stdoutTTY()

	teardown := NewTeardown(r.cfg.Teardown, r.cfg.Stderr)
	defer func() { _ = teardown.Run(context.Background()) }()

	var interrupted atomic.Bool
	// Always install handlers: a piped `ssh -- CMD` must turn SIGINT into 130,
	// --rm needs SIGHUP, and interactive mode must restore the console.
	stopSignals := InstallTerminationSignals(func(os.Signal) {
		interrupted.Store(true)
		_ = teardown.Run(context.Background())
		cancel()
	})
	defer stopSignals()

	key, err := ResolveKey(r.cfg.Identity)
	if err != nil {
		return 2, err
	}
	hc := r.cfg.HTTPClient
	if hc == nil {
		hc = DefaultHTTPClient(r.cfg.HandshakeTimeout)
	}
	conn, err := DialWS(runCtx,
		BuildWSURL(r.cfg.ProxyURL, r.cfg.Token, mode, r.cfg.ClientAgent),
		key.PublicLine, r.cfg.ClientAgent, hc)
	if err != nil {
		return 1, err
	}
	defer conn.Close()

	var rc int
	switch mode {
	case ModeProxy:
		rc = bridgeProxy(runCtx, conn, r.cfg)
	case ModeCmd:
		rc, err = runCommand(runCtx, conn, key, r.cfg, piped)
	case ModeInteractive:
		rc, err = runInteractive(runCtx, conn, key, r.cfg)
	}
	if interrupted.Load() {
		if mode == ModeCmd && piped {
			return 130, nil
		}
		return 0, nil
	}
	return rc, err
}

func (r *Runner) interruptedExitCode() int {
	mode := NormalizeMode(r.cfg.Mode)
	if mode == ModeCmd && (!r.stdinTTY() || !r.stdoutTTY()) {
		return 130
	}
	return 0
}
