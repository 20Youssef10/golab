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

	// colab hooks may park on a human. One worker drains a small bounded queue
	// so a flood of colab_request frames cannot fork unbounded goroutines.
	hookQueueSize = 2
)

// hookTask is one intercepted colab_request handed to the single hook worker.
type hookTask struct {
	hook func(Request) bool
	req  Request
	msg  *Message
}

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
	// frameObserver receives labels only (never payloads) for wire traces.
	frameObserver func(FrameEvent)

	channels map[string]chan *Message

	done      chan struct{}
	closeOnce sync.Once
	writeMu   sync.Mutex

	// stateMu guards the mutable per-generation fields: the active socket,
	// its loss signal, and the cancel func for its reader/pinger goroutines.
	stateMu   sync.Mutex
	lost      chan struct{}
	genCancel context.CancelFunc

	hookMu     sync.RWMutex
	colabHook  func(Request) bool
	hookCh     chan hookTask
	hookCancel context.CancelFunc

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

func firstObserver(observers []func(FrameEvent)) func(FrameEvent) {
	if len(observers) > 0 {
		return observers[0]
	}
	return nil
}

// The variadic observer keeps existing tests and embedders source-compatible.
func newConn(ws wsConn, logger *slog.Logger, observer ...func(FrameEvent)) *conn {
	c := &conn{
		logger:            logger,
		frameObserver:     firstObserver(observer),
		channels:          map[string]chan *Message{},
		done:              make(chan struct{}),
		hookCh:            make(chan hookTask, hookQueueSize),
		reconnectBudget:   30 * time.Second,
		reconnectInterval: 500 * time.Millisecond,
	}
	// A single worker handles every intercepted colab_request. It is tied to
	// the whole connection, not to a socket generation: a redial must not
	// abandon a request that is waiting on a human.
	hookCtx, hookCancel := context.WithCancel(context.Background())
	c.hookCancel = hookCancel
	go c.hookWorker(hookCtx)
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

// currentWS snapshots the active socket. Callers that write must hold writeMu
// across the snapshot and the write so a redial cannot retire the socket in
// between; startGen takes the same lock when installing a new generation.
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
	// Install the new socket under the write lock: a send that already holds
	// it finishes on the socket it snapshotted, and a send that is waiting for
	// it snapshots this new socket instead. No frame can land on the retired
	// socket during the redial window.
	c.writeMu.Lock()
	c.stateMu.Lock()
	c.ws = ws
	c.lost = lost
	c.genCancel = cancel
	c.stateMu.Unlock()
	c.writeMu.Unlock()
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
		if c.frameObserver != nil {
			c.frameObserver(FrameEvent{Channel: msg.Channel, Direction: "in", MsgType: msg.Header.MsgType})
		}
		if msg.Header.MsgType == "colab_request" {
			c.hookMu.RLock()
			hook := c.colabHook
			c.hookMu.RUnlock()
			if hook != nil {
				req := requestFromMessage(msg)
				// The hook may wait for a human: never run it on the reader
				// goroutine and never fork one goroutine per frame. Hand it to
				// a single bounded worker instead; beyond the queue depth the
				// request is dropped rather than growing without limit.
				select {
				case c.hookCh <- hookTask{hook: hook, req: req, msg: msg}:
				default:
					c.logger.Warn("colab_request hook queue full, dropping request",
						"msg_type", msg.Header.MsgType)
				}
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

// hookWorker drains colab_request hooks one at a time. The queue is bounded so
// a flood cannot spawn unbounded goroutines, and the worker exits when the
// connection closes so no hook outlives the socket. Cancellation is checked
// before each task so requests queued before close are abandoned.
func (c *conn) hookWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case task := <-c.hookCh:
			c.runColabHook(task.hook, task.req, task.msg)
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
	if c.frameObserver != nil {
		c.frameObserver(FrameEvent{Channel: msg.Channel, Direction: "out", MsgType: msg.Header.MsgType})
	}
	// Lock first, then snapshot the socket under the lock. Snapshotting before
	// the lock lets a concurrent redial swap c.ws so the frame is written to a
	// retired socket, silently dropping input_reply/interrupt/execute_request.
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.currentWS().Write(ctx, typ, data)
}

func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.hookCancel != nil {
			c.hookCancel()
		}
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
