package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

const (
	maxAttempts       = 3
	defaultAgent      = "golab"
	defaultGraceful   = 2 * time.Second
	firstRetryBackoff = 2 * time.Second
	// createKernelName must match the name restClient.createKernel posts. A
	// reconciled kernel is only reusable when it is the same kind we asked for.
	createKernelName = "python3"
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
	// A saved session id is honored so a resumed execution reattaches to the
	// same Jupyter session; a fresh execution still mints a new one.
	sessionID := target.SessionID
	if sessionID == "" {
		sessionID = newSessionID()
	}
	var wsConn *conn
	if err := r.retry(ctx, func() error {
		ws, err := rest.dialWS(ctx, kernelID, sessionID)
		if err != nil {
			return err
		}
		wsConn = newConn(ws, r.cfg.Logger, r.cfg.FrameObserver)
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
		journals: map[string]*journal{},
	}
	go k.stdinLoop()
	return k, nil
}

// resolve applies the stale-id recovery of docs/02 §3.2. A 404 from GET is a
// normal "gone" answer: create a new kernel and always return its id.
//
// Creation is deliberately kept out of the generic retry: a create POST that
// times out client-side may already have landed server-side. Blindly replaying
// it leaves an orphan and allocates a second billable runtime (defect §3.17).
// The idempotent GET inspect still uses retry unchanged.
func (r *runtimeImpl) resolve(ctx context.Context, rest *restClient, cachedID string) (string, error) {
	if cachedID != "" {
		var id string
		err := r.retry(ctx, func() error {
			exists, err := rest.inspectKernel(ctx, cachedID)
			if err != nil {
				return err
			}
			if exists {
				id = cachedID
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if id != "" {
			return id, nil
		}
		// The cached id is gone (404); fall through and create a replacement.
	}

	id, err := r.createWithReconcile(ctx, rest)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("attach: no kernel id resolved")
	}
	return id, nil
}

// createWithReconcile creates a kernel, retrying timeouts only after checking
// the server for a kernel the timed-out POST may already have created. A
// matching kernel is reused so the retry budget never double-allocates.
func (r *runtimeImpl) createWithReconcile(ctx context.Context, rest *restClient) (string, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		id, err := rest.createKernel(ctx)
		if err == nil {
			return id, nil
		}
		lastErr = err
		if !isTimeout(err) {
			return "", err
		}

		// Reconcile even on the final timeout: a create that landed just after
		// the deadline would otherwise be orphaned with no owner.
		reused, rerr := r.reconcileKernel(ctx, rest)
		if rerr != nil {
			return "", rerr
		}
		if reused != "" {
			return reused, nil
		}
		if attempt == maxAttempts-1 {
			return "", err
		}

		delay := r.cfg.RetryBackoff(attempt)
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", lastErr
}

// reconcileKernel lists the kernels on the proxy and returns the id of a live
// kernel matching the one createKernel posts. GET is idempotent, so it keeps
// the same timeout-retry semantics as the other inspect path. An empty result
// means the timed-out create genuinely did not land and a retry is safe.
func (r *runtimeImpl) reconcileKernel(ctx context.Context, rest *restClient) (string, error) {
	var id string
	err := r.retry(ctx, func() error {
		data, status, err := rest.do(ctx, http.MethodGet, "/api/kernels", nil)
		if err != nil {
			return fmt.Errorf("reconcile kernels: %w", err)
		}
		switch status {
		case http.StatusOK:
		case http.StatusUnauthorized:
			return &SessionLostError{Op: "reconcile kernels", StatusCode: status}
		default:
			return &HTTPStatusError{Op: "reconcile kernels", Code: status}
		}
		var models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &models); err != nil {
			return fmt.Errorf("reconcile kernels: malformed response: %w", err)
		}
		for _, m := range models {
			if m.ID != "" && m.Name == createKernelName {
				id = m.ID
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return "", err
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
