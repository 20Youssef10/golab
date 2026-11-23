package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/20Youssef10/golab/internal/logx"
)

// bridgeProxy is the OpenSSH ProxyCommand stdio bridge. It is a byte pump: no
// framing, no PTY, no SSH handshake of our own. Stdout is the raw SSH byte
// stream; diagnostics go to stderr.
func bridgeProxy(ctx context.Context, nc net.Conn, cfg Config) int {
	var wg sync.WaitGroup
	var once sync.Once
	closeConn := func() {
		once.Do(func() { _ = nc.Close() })
	}
	report := func(dir string, err error) {
		if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
			return
		}
		fmt.Fprintf(cfg.Stderr, "[colab] bridge %s: %s\n", dir, logx.Redact(err.Error()))
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := io.Copy(cfg.Stdout, nc)
		report("read", err)
		closeConn()
	}()
	go func() {
		defer wg.Done()
		defer closeConn()
		_, err := io.Copy(nc, cfg.Stdin)
		report("write", err)
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		closeConn()
		<-done
	}
	return 0
}
