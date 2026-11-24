package runtime

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	channelBuffer = 256
	pingInterval  = 60 * time.Second
	pingTimeout   = 10 * time.Second
)

// wsConn is the subset of *websocket.Conn that conn uses. It is an interface so
// tests can drive the reader with a fake connection (transient-drop regression)
// without a live runtime proxy. *websocket.Conn satisfies it directly.
type wsConn interface {
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
	Write(ctx context.Context, typ websocket.MessageType, data []byte) error
	Ping(ctx context.Context) error
	Close(code websocket.StatusCode, reason string) error
	SetReadLimit(limit int64)
}

// conn owns one WebSocket generation at a time. The coder/websocket reader
// runs in exactly one goroutine, dispatching by channel to buffered queues; all
// writes go through a mutex. The buffered channels outlive any single socket so
// a bounded reconnect can reinstall a socket without losing queued messages.
// No package-level mutable state is used.
type conn struct {
	ws     wsConn
	logger *slog.Logger

	channels map[string]chan *Message

	done      chan struct{}
	closeOnce sync.Once
	writeMu   sync.Mutex

	// stateMu guards the mutable per-generation fields: the active socket,
	// its loss signal, and the cancel func for its reader/pinger goroutines.
	stateMu   sync.Mutex
	lost      chan struct{}
	genCancel context.CancelFunc

	hookMu    sync.RWMutex
	colabHook func(Request) bool

	activeMu sync.RWMutex
	active   activeExec

	// dialWS reopens the channels socket. When nil, reconnect falls back to
	// restClient.dialWS with the kernel/session id supplied by the kernel.
	// Tests inject a fake dialer here.
	dialWS func(ctx context.Context, kernelID, sessionID string) (wsConn, error)

	// Bounds for a reconnect after a transient reader failure. Tests shorten
	// these so the permanent-failure case stays fast.
	reconnectBudget   time.Duration
	reconnectInterval time.Duration
}

type activeExec struct {
	msgID   string
	opts    ExecOptions
	session string
}

func newConn(ws wsConn, logger *slog.Logger) *conn {
	c := &conn{
		logger:            logger,
		channels:          map[string]chan *Message{},
		done:              make(chan struct{}),
		reconnectBudget:   30 * time.Second,
		reconnectInterval: 500 * time.Millisecond,
	}
	for _, name := range []string{"shell", "iopub", "stdin", "control", "hb"} {
		c.channels[name] = make(chan *Message, channelBuffer)
	}
	c.startGen(ws)
	return c
}

func (c *conn) ch(name string) chan *Message {
	if ch := c.channels[name]; ch != nil {
		return ch
	}
	// Unknown channels still need a sink so the reader never blocks.
	return c.channels["control"]
}

// lostChan reports the current generation's loss signal. Execute re-reads it
// each loop iteration so a reconnect's replacement channel is observed.
func (c *conn) lostChan() <-chan struct{} {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.lost
}

// currentWS snapshots the active socket. A redial may swap it concurrently; a
// write against the previous socket simply fails as a transient error.
func (c *conn) currentWS() wsConn {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.ws
}

// startGen installs ws as the active generation and starts its reader/pinger.
// Each generation owns a fresh lost channel and a cancel func so a reconnect
// can retire the old goroutines without closing the connection as a whole.
func (c *conn) startGen(ws wsConn) {
	genCtx, cancel := context.WithCancel(context.Background())
	lost := make(chan struct{})
	c.stateMu.Lock()
	c.ws = ws
	c.lost = lost
	c.genCancel = cancel
	c.stateMu.Unlock()
	// Retire this generation when the whole connection is closed. The watcher
	// exits on cancel too, so reconnect does not leak it.
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-genCtx.Done():
		}
	}()
	go c.readLoop(genCtx, ws, lost)
	go c.pingLoop(genCtx, ws, cancel)
}

func (c *conn) setColabHook(f func(Request) bool) {
	c.hookMu.Lock()
	c.colabHook = f
	c.hookMu.Unlock()
}

func (c *conn) setActive(a activeExec) {
	c.activeMu.Lock()
	c.active = a
	c.activeMu.Unlock()
}

func (c *conn) clearActive() {
	c.activeMu.Lock()
	c.active = activeExec{}
	c.activeMu.Unlock()
}

func (c *conn) readLoop(ctx context.Context, ws wsConn, lost chan struct{}) {
	defer close(lost)
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		msg, err := DecodeFrame(data, typ == websocket.MessageBinary)
		if err != nil {
			c.logger.Debug("dropping malformed kernel frame", "error", err)
			continue
		}
		if msg.Header.MsgType == "colab_request" {
			c.hookMu.RLock()
			hook := c.colabHook
			c.hookMu.RUnlock()
			if hook != nil {
				req := requestFromMessage(msg)
				// The hook may wait for a human: never run it on the reader
				// goroutine. Intercept the message before normal queueing.
				go c.runColabHook(hook, req, msg)
				continue
			}
		}
		select {
		case c.ch(msg.Channel) <- msg:
		case <-ctx.Done():
			return
		}
	}
}

func (c *conn) runColabHook(hook func(Request) bool, req Request, msg *Message) {
	handled := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				c.logger.Warn("colab_request hook panicked")
			}
		}()
		handled = hook(req)
	}()
	if handled {
		return
	}
	// Unhandled authTypes fall through to the normal path.
	select {
	case c.ch("iopub") <- msg:
	case <-c.done:
	}
}

func (c *conn) pingLoop(ctx context.Context, ws wsConn, cancel context.CancelFunc) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pctx, pcancel := context.WithTimeout(context.Background(), pingTimeout)
			err := ws.Ping(pctx)
			pcancel()
			if err != nil {
				// Retire only this generation. The reader then closes its
				// lost channel, which lets Execute retry the socket in place.
				cancel()
				return
			}
		}
	}
}

func (c *conn) send(ctx context.Context, msg *Message) error {
	typ, data, err := EncodeFrame(msg)
	if err != nil {
		return err
	}
	ws := c.currentWS()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return ws.Write(ctx, typ, data)
}

func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.stateMu.Lock()
		cancel, ws := c.genCancel, c.ws
		c.stateMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if ws != nil {
			_ = ws.Close(websocket.StatusNormalClosure, "")
		}
	})
}

// reconnect redials the channels socket with the same kernel/session id until
// it succeeds or the bounded budget is exhausted. A SessionLostError (401/404)
// is permanent and is returned immediately so callers can prune the session;
// every other dial error is retried at a bounded interval. Context cancellation
// always stops the loop. On exhaustion a typed *ConnectionLostError is returned.
func (c *conn) reconnect(ctx context.Context, rest *restClient, kernelID, sessionID string) error {
	// A connection explicitly closed by the caller must never be redialed.
	select {
	case <-c.done:
		return &ConnectionLostError{Attempts: 0, Cause: errors.New("connection closed")}
	default:
	}

	start := time.Now()
	deadline := start.Add(c.reconnectBudget)
	attempts := 0
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempts++
		ws, err := c.redial(ctx, rest, kernelID, sessionID)
		if err == nil {
			c.stateMu.Lock()
			oldCancel := c.genCancel
			c.stateMu.Unlock()
			if oldCancel != nil {
				oldCancel()
			}
			c.startGen(ws)
			return nil
		}
		lastErr = err
		// A lost session cannot be recovered by redialing; surface it at once.
		if errors.Is(err, ErrSessionLost) {
			return err
		}
		if !time.Now().Before(deadline) {
			break
		}
		delay := c.reconnectInterval
		if delay <= 0 {
			delay = time.Millisecond // never busy-spin, even if misconfigured
		}
		if remain := time.Until(deadline); remain < delay {
			delay = remain
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-c.done:
			timer.Stop()
			return &ConnectionLostError{Attempts: attempts, Elapsed: time.Since(start), Cause: lastErr}
		case <-timer.C:
		}
	}
	return &ConnectionLostError{
		Attempts: attempts, Elapsed: time.Since(start), Cause: lastErr,
	}
}

func (c *conn) redial(ctx context.Context, rest *restClient, kernelID, sessionID string) (wsConn, error) {
	if c.dialWS != nil {
		return c.dialWS(ctx, kernelID, sessionID)
	}
	return rest.dialWS(ctx, kernelID, sessionID)
}

func (c *restClient) dialWS(ctx context.Context, kernelID, sessionID string) (*websocket.Conn, error) {
	u, err := url.Parse(c.baseURL + "/api/kernels/" + url.PathEscape(kernelID) + "/channels")
	if err != nil {
		return nil, safeError(err, c.token)
	}
	q := u.Query()
	q.Set("session_id", sessionID)
	q.Set("token", c.token)
	u.RawQuery = q.Encode()

	ws, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient:   c.client,
		HTTPHeader:   c.headers(),
		Subprotocols: nil, // default protocol: no subprotocol
	})
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound) {
			return nil, &SessionLostError{Op: "open channels", StatusCode: resp.StatusCode}
		}
		return nil, safeError(err, c.token)
	}
	ws.SetReadLimit(maxFrameSize)
	return ws, nil
}
