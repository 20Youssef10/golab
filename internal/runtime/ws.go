package runtime

import (
	"context"
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

// conn owns one WebSocket. The coder/websocket reader runs in exactly one
// goroutine, dispatching by channel to buffered queues; all writes go through
// a mutex. No package-level mutable state is used.
type conn struct {
	ws     *websocket.Conn
	logger *slog.Logger

	channels map[string]chan *Message

	done      chan struct{}
	lost      chan struct{}
	closeOnce sync.Once
	writeMu   sync.Mutex

	hookMu    sync.RWMutex
	colabHook func(Request) bool

	activeMu sync.RWMutex
	active   activeExec
}

type activeExec struct {
	msgID   string
	opts    ExecOptions
	session string
}

func newConn(ws *websocket.Conn, logger *slog.Logger) *conn {
	c := &conn{
		ws:       ws,
		logger:   logger,
		channels: map[string]chan *Message{},
		done:     make(chan struct{}),
		lost:     make(chan struct{}),
	}
	for _, name := range []string{"shell", "iopub", "stdin", "control", "hb"} {
		c.channels[name] = make(chan *Message, channelBuffer)
	}
	go c.readLoop()
	go c.pingLoop()
	return c
}

func (c *conn) ch(name string) chan *Message {
	if ch := c.channels[name]; ch != nil {
		return ch
	}
	// Unknown channels still need a sink so the reader never blocks.
	return c.channels["control"]
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

func (c *conn) readLoop() {
	defer close(c.lost)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		typ, data, err := c.ws.Read(ctx)
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
		case <-c.done:
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

func (c *conn) pingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				c.close()
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
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, typ, data)
}

func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
	})
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
