package sshx

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/version"
)

// Wire-contract constants (docs/02 §4). They are deliberately exported so the
// conformance tests can assert the exact bytes on the wire.
const (
	SSHPath           = "/colab/ssh"
	PubkeyHeader      = "X-Colab-Ssh-Pubkey"
	ClientAgentHeader = "X-Colab-Client-Agent"
)

// DefaultClientAgent is our honest identity (DEC-03 / R-03): we do not
// impersonate the upstream colab-cli agent.
func DefaultClientAgent() string { return "colab-go/" + version.Number }

// BuildWSURL builds the SSH upgrade URL. The token is revealed only here, at
// the point of use; it is never formatted into a log or error message.
func BuildWSURL(baseURL string, token logx.Secret, mode Mode, agent string) string {
	u, err := url.Parse(baseURL)
	scheme := "wss"
	host := baseURL
	if err == nil {
		if u.Host != "" {
			host = u.Host
		}
		if u.Scheme == "http" {
			scheme = "ws"
		}
	}
	if mode == "" {
		mode = ModeProxy
	}
	if agent == "" {
		agent = DefaultClientAgent()
	}
	return scheme + "://" + host + SSHPath +
		"?colab-runtime-proxy-token=" + token.Reveal() +
		"&colab-client-agent=" + agent +
		"&colab-client-agent=ssh-mode-" + string(mode)
}

// DefaultHTTPClient builds a client with explicit connect/TLS/header/idle
// timeouts (AGENTS.md #5). coder/websocket moves Client.Timeout onto the
// handshake context, so it does not bound the long-lived SSH connection.
func DefaultHTTPClient(handshakeTimeout time.Duration) *http.Client {
	if handshakeTimeout <= 0 {
		handshakeTimeout = 30 * time.Second
	}
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: handshakeTimeout,
	}
	return &http.Client{Transport: tr, Timeout: handshakeTimeout}
}

// DialWS opens the WebSocket with the contracted query and headers. On a
// failed upgrade it returns a *HandshakeError; on other network failures an
// ordinary wrapped error. No token value is ever included in either message.
func DialWS(ctx context.Context, u, pubkey, agent string, client *http.Client) (net.Conn, error) {
	if client == nil {
		client = DefaultHTTPClient(0)
	}
	header := http.Header{}
	header.Set(PubkeyHeader, pubkey)
	header.Set(ClientAgentHeader, agent)
	conn, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: header,
	})
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			var body []byte
			if resp.Body != nil {
				body, _ = io.ReadAll(io.LimitReader(resp.Body, 1024))
				_ = resp.Body.Close()
			}
			return nil, &HandshakeError{
				Status:  resp.StatusCode,
				Body:    body,
				Message: logx.Redact(ExplainHandshakeFailure(resp.StatusCode, body)),
			}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("WebSocket connection failed: %w", ctxErr)
		}
		// url.Error from net/http embeds the full request URL — including the
		// proxy token in the query. Redact before it can reach a log or a user.
		return nil, fmt.Errorf("WebSocket connection failed: %s. "+
			"Check your network and that the runtime is healthy (`colab status`).",
			logx.Redact(err.Error()))
	}
	// A NetConn-style adapter is required by x/crypto/ssh. Unlike
	// websocket.NetConn it also accepts text frames as bytes (docs/02 §4) and
	// maps a close frame to EOF, the two upstream frame behaviours we rely on.
	return newWSConn(ctx, conn), nil
}

// wsConn adapts a *websocket.Conn to net.Conn for x/crypto/ssh. It is the
// NetConn pattern from ADR-005 extended for text frames; every Write is a
// binary frame, every Read consumes complete messages of either data type.
type wsConn struct {
	ctx    context.Context
	c      *websocket.Conn
	reader io.Reader

	readMu  sync.Mutex
	writeMu sync.Mutex
	closed  atomic.Bool
	addr    wsAddr
}

func newWSConn(ctx context.Context, c *websocket.Conn) net.Conn {
	c.SetReadLimit(64 << 20)
	return &wsConn{ctx: ctx, c: c}
}

func (w *wsConn) Read(p []byte) (int, error) {
	w.readMu.Lock()
	defer w.readMu.Unlock()
	for {
		if w.reader == nil {
			typ, r, err := w.c.Reader(w.ctx)
			if err != nil {
				switch websocket.CloseStatus(err) {
				case websocket.StatusNormalClosure, websocket.StatusGoingAway:
					return 0, io.EOF
				}
				return 0, err
			}
			if typ != websocket.MessageBinary && typ != websocket.MessageText {
				_ = w.c.Close(websocket.StatusUnsupportedData, "unexpected frame type")
				return 0, fmt.Errorf("unexpected WebSocket frame type %v", typ)
			}
			w.reader = r
		}
		n, err := w.reader.Read(p)
		if err == io.EOF {
			w.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		if n > 0 || err != nil {
			return n, err
		}
	}
}

func (w *wsConn) Write(p []byte) (int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if w.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if err := w.c.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsConn) Close() error {
	if !w.closed.CompareAndSwap(false, true) {
		return nil
	}
	return w.c.Close(websocket.StatusNormalClosure, "")
}

// Deadlines are no-ops: coder/websocket's NetConn cancels the whole connection
// on a deadline, which would break an interactive session. Session bounds come
// from the context instead.
func (w *wsConn) SetDeadline(time.Time) error      { return nil }
func (w *wsConn) SetReadDeadline(time.Time) error  { return nil }
func (w *wsConn) SetWriteDeadline(time.Time) error { return nil }

func (w *wsConn) LocalAddr() net.Addr  { return w.addr }
func (w *wsConn) RemoteAddr() net.Addr { return w.addr }

type wsAddr struct{}

func (wsAddr) Network() string { return "websocket" }
func (wsAddr) String() string  { return "websocket/colab-runtime" }
