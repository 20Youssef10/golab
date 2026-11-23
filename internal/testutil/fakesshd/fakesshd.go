package fakesshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// Failure describes a failed WebSocket upgrade. Body is written verbatim with
// a Content-Length so clients that read error bodies see it.
type Failure struct {
	Status int
	Body   string
}

// UpstreamFailure returns the documented upgrade failure for status, including
// the upstream JSON bodies (docs/02 §4, docs/05 ssh row).
func UpstreamFailure(status int) Failure {
	switch status {
	case 400:
		return Failure{400, "unsupported key type"}
	case 401:
		return Failure{401, `{"error":"invalid runtime proxy token"}`}
	case 403:
		return Failure{403, "forbidden"}
	case 404:
		return Failure{404, "endpoint not found: SSH not enabled on this runtime"}
	case 429:
		return Failure{429, `{"error":"already-active-session","limit":2}`}
	case 502:
		return Failure{502, "sshd unreachable"}
	default:
		return Failure{status, "WebSocket upgrade rejected"}
	}
}

// Options configures a Server. All fields are optional.
type Options struct {
	// Failure makes the HTTP upgrade fail with this status and body.
	Failure *Failure
	// ExitStatus is returned for exec/shell (default 0). A negative value is
	// treated as 0 when ExitSignal is empty.
	ExitStatus int
	// ExitSignal, when non-empty, sends exit-signal instead of exit-status.
	ExitSignal string
	// Output is written to the channel's stdout on exec; default "OK\n".
	Output []byte
	// StderrOutput is written to the channel's stderr on exec.
	StderrOutput []byte
	// Banner is written to the channel on shell (interactive tests).
	Banner []byte
	// ShellHold bounds how long a shell channel stays open (default 50 ms).
	ShellHold time.Duration
	// Echo bridges raw frames back instead of running the SSH server.
	Echo bool
	// InjectText frames are sent to the client as text frames after upgrade.
	InjectText [][]byte
	// OnEvent, if set, is called for every recorded event.
	OnEvent func(Event)
}

// Event is one recorded SSH protocol event.
type Event struct {
	Type          string // "channel", "auth", "pty-req", "window-change", "env", "exec", "shell"
	Term          string
	Width, Height int
	Name, Value   string
	Command       string
	Key           string
	At            time.Time
}

// CapturedRequest is the raw HTTP upgrade request the client sent.
type CapturedRequest struct {
	Method string
	Target string // path + query, exactly as received
	Path   string
	Query  string
	Proto  string
	Header http.Header
}

// Server is a loopback fake of the Colab SSH WebSocket endpoint.
type Server struct {
	opts Options

	ln   net.Listener
	http *http.Server

	mu       sync.Mutex
	requests []CapturedRequest
	events   []Event
	closed   bool

	hostKey ssh.Signer
}

// New builds a Server with an ephemeral host key.
func New(opts Options) (*Server, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	return &Server{opts: opts, hostKey: signer}, nil
}

// Start binds a loopback listener and begins serving.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	s.http = &http.Server{
		Handler:           http.HandlerFunc(s.handle),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = s.http.Serve(ln) }()
	return nil
}

// URL returns the base URL to pass as Config.ProxyURL (http:// → ws://).
func (s *Server) URL() string {
	if s.ln == nil {
		return ""
	}
	return "http://" + s.ln.Addr().String()
}

// Close stops the server. It is safe to call more than once.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.http.Shutdown(ctx)
	return s.ln.Close()
}

// Requests returns a snapshot of all captured upgrade requests.
func (s *Server) Requests() []CapturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CapturedRequest(nil), s.requests...)
}

// LastRequest returns the most recently captured request, or nil.
func (s *Server) LastRequest() *CapturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return nil
	}
	r := s.requests[len(s.requests)-1]
	return &r
}

// Events returns a snapshot of all recorded SSH events.
func (s *Server) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func (s *Server) record(ev Event) {
	ev.At = time.Now()
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	if s.opts.OnEvent != nil {
		s.opts.OnEvent(ev)
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	req := CapturedRequest{
		Method: r.Method,
		Target: r.URL.RequestURI(),
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Proto:  r.Proto,
		Header: r.Header.Clone(),
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	if s.opts.Failure != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", fmt.Sprint(len(s.opts.Failure.Body)))
		w.WriteHeader(s.opts.Failure.Status)
		_, _ = w.Write([]byte(s.opts.Failure.Body))
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	if s.opts.Echo {
		s.echo(c)
		return
	}
	s.runSSH(c)
}

// readFrames consumes complete messages with a single Reader. coder/websocket
// allows only one concurrent reader, and CloseRead would swallow the frames
// before we see them.
func readFrames(ctx context.Context, c *websocket.Conn, handle func(websocket.MessageType, []byte) error) error {
	for {
		typ, r, err := c.Reader(ctx)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		if err := handle(typ, data); err != nil {
			return err
		}
	}
}

func (s *Server) echo(c *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer c.Close(websocket.StatusNormalClosure, "")
	for _, frame := range s.opts.InjectText {
		if err := c.Write(ctx, websocket.MessageText, frame); err != nil {
			return
		}
	}
	_ = readFrames(ctx, c, func(typ websocket.MessageType, data []byte) error {
		return c.Write(ctx, typ, data)
	})
}

func (s *Server) runSSH(c *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer c.Close(websocket.StatusNormalClosure, "")

	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	s.bridge(ctx, c, clientSide)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.record(Event{Type: "auth", Key: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))})
			return nil, nil
		},
	}
	cfg.AddHostKey(s.hostKey)
	conn, chans, reqs, err := ssh.NewServerConn(serverSide, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, reqCh, err := newCh.Accept()
		if err != nil {
			continue
		}
		s.record(Event{Type: "channel"})
		go s.handleSession(ch, reqCh)
	}
}

// bridge pumps WebSocket frames to the SSH net.Conn and back. Text frames are
// treated as bytes, matching the wire contract.
func (s *Server) bridge(ctx context.Context, c *websocket.Conn, conn net.Conn) {
	var writeMu sync.Mutex
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = conn.Close()
			_ = c.Close(websocket.StatusNormalClosure, "")
		})
	}
	go func() {
		defer closeBoth()
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				writeMu.Lock()
				werr := c.Write(ctx, websocket.MessageBinary, buf[:n])
				writeMu.Unlock()
				if werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer closeBoth()
		_ = readFrames(ctx, c, func(typ websocket.MessageType, data []byte) error {
			if typ != websocket.MessageBinary && typ != websocket.MessageText {
				return nil
			}
			_, err := conn.Write(data)
			return err
		})
	}()
}

func (s *Server) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term       string
				Columns    uint32
				Rows       uint32
				PixelWidth uint32
				PixHeight  uint32
				Modes      string
			}
			_ = ssh.Unmarshal(req.Payload, &p)
			s.record(Event{Type: "pty-req", Term: p.Term, Width: int(p.Columns), Height: int(p.Rows)})
			req.Reply(true, nil)
		case "window-change":
			var p struct {
				Columns    uint32
				Rows       uint32
				PixelWidth uint32
				PixHeight  uint32
			}
			_ = ssh.Unmarshal(req.Payload, &p)
			s.record(Event{Type: "window-change", Width: int(p.Columns), Height: int(p.Rows)})
			req.Reply(true, nil)
		case "env":
			var p struct{ Name, Value string }
			_ = ssh.Unmarshal(req.Payload, &p)
			s.record(Event{Type: "env", Name: p.Name, Value: p.Value})
			req.Reply(true, nil)
		case "shell":
			s.record(Event{Type: "shell"})
			req.Reply(true, nil)
			s.channelOutput(ch, s.opts.Banner)
			s.waitForClient(ch)
			s.replyExitStatus(ch)
			return
		case "exec":
			var p struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &p)
			s.record(Event{Type: "exec", Command: p.Command})
			req.Reply(true, nil)
			// An interactive shell is an exec request carrying the login-shell
			// command (OpenSSH -t semantics). Record it as "shell" too so
			// terminal tests can assert the intended request.
			if strings.Contains(p.Command, "exec ${SHELL:-/bin/bash} -l") {
				s.record(Event{Type: "shell", Command: p.Command})
				s.channelOutput(ch, s.opts.Banner)
				s.waitForClient(ch)
				s.replyExitStatus(ch)
				return
			}
			out := s.opts.Output
			if out == nil {
				out = []byte("OK\n")
			}
			s.channelOutput(ch, out)
			if len(s.opts.StderrOutput) > 0 {
				_, _ = ch.Stderr().Write(s.opts.StderrOutput)
			}
			s.replyExitStatus(ch)
			return
		default:
			req.Reply(false, nil)
		}
	}
}

func (s *Server) channelOutput(ch ssh.Channel, data []byte) {
	if len(data) > 0 {
		_, _ = ch.Write(data)
	}
}

func (s *Server) waitForClient(ch ssh.Channel) {
	hold := s.opts.ShellHold
	if hold <= 0 {
		hold = 50 * time.Millisecond
	}
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := ch.Read(buf); err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(hold):
	}
}

func (s *Server) replyExitStatus(ch ssh.Channel) {
	if s.opts.ExitSignal != "" {
		payload := ssh.Marshal(struct {
			Signal string
			Core   bool
			Error  string
			Lang   string
		}{s.opts.ExitSignal, false, "", ""})
		_, _ = ch.SendRequest("exit-signal", false, payload)
		return
	}
	status := s.opts.ExitStatus
	if status < 0 {
		status = 0
	}
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(status))
	_, _ = ch.SendRequest("exit-status", false, payload)
}
