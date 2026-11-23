package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

const (
	maxAttempts       = 3
	defaultAgent      = "golab"
	defaultGraceful   = 2 * time.Second
	firstRetryBackoff = 2 * time.Second
)

type runtimeImpl struct{ cfg Config }

// New builds a Runtime. All dependencies have usable defaults; Config.HTTPClient
// lets tests point the client at a fake runtime proxy with its own CA.
func New(cfg Config) (Runtime, error) {
	if cfg.Logger == nil {
		cfg.Logger = logx.Logger()
	}
	if cfg.ClientAgent == "" {
		cfg.ClientAgent = defaultAgent
	}
	if cfg.RetryBackoff == nil {
		cfg.RetryBackoff = func(attempt int) time.Duration {
			return firstRetryBackoff * time.Duration(1<<attempt)
		}
	}
	if cfg.GracefulCancelTimeout <= 0 {
		cfg.GracefulCancelTimeout = defaultGraceful
	}
	return &runtimeImpl{cfg: cfg}, nil
}

// Attach resolves the effective kernel id (creating a replacement when a
// cached id is stale), dials the channels socket and returns a Kernel.
// OnKernelStarted always receives the effective id (ADR-009.4).
func (r *runtimeImpl) Attach(ctx context.Context, target Target, opts AttachOptions) (Kernel, error) {
	if target.URL == "" {
		return nil, errors.New("runtime target has no URL")
	}
	rest := newREST(target, r.cfg)
	kernelID, err := r.resolve(ctx, rest, target.KernelID)
	if err != nil {
		return nil, err
	}
	sessionID := newSessionID()
	var wsConn *conn
	if err := r.retry(ctx, func() error {
		ws, err := rest.dialWS(ctx, kernelID, sessionID)
		if err != nil {
			return err
		}
		wsConn = newConn(ws, r.cfg.Logger)
		return nil
	}); err != nil {
		return nil, err
	}

	if opts.OnKernelStarted != nil {
		opts.OnKernelStarted(kernelID)
	}
	if opts.OnSessionStarted != nil {
		opts.OnSessionStarted(sessionID)
	}

	k := &kernel{
		id: kernelID, sessionID: sessionID, rest: rest, conn: wsConn,
		logger: r.cfg.Logger, graceful: r.cfg.GracefulCancelTimeout,
	}
	go k.stdinLoop()
	return k, nil
}

// resolve applies the stale-id recovery of docs/02 §3.2. A 404 from GET is a
// normal "gone" answer: create a new kernel and always return its id.
func (r *runtimeImpl) resolve(ctx context.Context, rest *restClient, cachedID string) (string, error) {
	var id string
	err := r.retry(ctx, func() error {
		if cachedID == "" {
			newID, err := rest.createKernel(ctx)
			if err != nil {
				return err
			}
			id = newID
			return nil
		}
		exists, err := rest.inspectKernel(ctx, cachedID)
		if err != nil {
			return err
		}
		if !exists {
			newID, err := rest.createKernel(ctx)
			if err != nil {
				return err
			}
			id = newID
			return nil
		}
		id = cachedID
		return nil
	})
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("attach: no kernel id resolved")
	}
	return id, nil
}

// retry runs fn up to three times, sleeping only after timeouts. Context
// cancellation always stops the sequence.
func (r *runtimeImpl) retry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if !isTimeout(err) || attempt == maxAttempts-1 {
			return err
		}
		delay := r.cfg.RetryBackoff(attempt)
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}
