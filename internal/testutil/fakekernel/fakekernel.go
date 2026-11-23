package fakekernel

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Step is one scripted kernel message. A nil or empty Channel means "iopub";
// Parent "" or "exec" binds the message to the current execute_request;
// Parent "none" emits an empty parent_header (used for dead/restarting status).
// Setting Buffers forces a binary frame; otherwise a text frame is emitted.
type Step struct {
	Delay    time.Duration
	Channel  string
	MsgType  string
	Content  any
	Parent   string
	Metadata map[string]any
	Header   map[string]any
	Buffers  [][]byte
}

// RequestLog records one inbound HTTP request (including the WS handshake).
type RequestLog struct {
	Method  string
	Path    string
	Query   url.Values
	Headers http.Header
	Body    []byte
}

// InputRecord records an input_reply received from the client on stdin.
type InputRecord struct {
	Value        json.RawMessage
	ParentHeader map[string]any
	MsgType      string
}

// ExecuteRecord records an execute_request received from the client.
type ExecuteRecord struct {
	Code       string
	AllowStdin bool
	MsgID      string
	SessionID  string
}

// Server is a fake runtime proxy. Call New, then use URL/Client/Token to build
// a runtime.Target. Register scripts with Script before calling Execute.
type Server struct {
	*httptest.Server

	mu           sync.Mutex
	requests     []RequestLog
	scripts      [][]Step
	executions   []ExecuteRecord
	inputs       []InputRecord
	kernelID     string
	alive        bool
	unauthorized bool
	createFail   int // number of 500s to return from POST /api/kernels
	timeoutFirst int // number of initial REST requests that hang until ctx done

	msgCounter atomic.Uint64
}

// New starts a TLS fake kernel with one live kernel and a fixed proxy token.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		kernelID: "kernel-fake-1",
		alive:    true,
	}
	s.Server = httptest.NewTLSServer(s.handler())
	t.Cleanup(s.Server.Close)
	return s
}

// Client returns an http.Client that trusts the fake server's TLS certificate.
// It is also suitable for coder/websocket.DialOptions.HTTPClient.
func (s *Server) Client() *http.Client { return s.Server.Client() }

// Token returns the fixed proxy token the fake accepts.
func (s *Server) Token() string { return "fake-proxy-token-9f8e7d6c" }

// KernelID returns the id of the kernel created at startup.
func (s *Server) KernelID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kernelID
}

// URL returns the base URL of the fake runtime proxy.
func (s *Server) URL() string { return s.Server.URL }

// Script queues a message script for the next execute_request. With no queued
// script the kernel replies with a single idle status and an ok execute_reply.
func (s *Server) Script(steps ...Step) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts = append(s.scripts, steps)
}

// SetAlive toggles whether GET /api/kernels/{id} reports the kernel as live.
func (s *Server) SetAlive(alive bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alive = alive
}

// SetUnauthorized makes REST calls return HTTP 401, which the runtime must
// surface as ErrSessionLost (never by matching status text).
func (s *Server) SetUnauthorized(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unauthorized = v
}

// FailCreates makes the next n POST /api/kernels calls return HTTP 500.
func (s *Server) FailCreates(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createFail = n
}

// HangFirstREST makes the next n REST calls wait until the request context is
// cancelled, simulating a connect/read timeout.
func (s *Server) HangFirstREST(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeoutFirst = n
}

// Requests returns a snapshot of all inbound HTTP requests.
func (s *Server) Requests() []RequestLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RequestLog(nil), s.requests...)
}

// Executions returns a snapshot of all execute_requests received.
func (s *Server) Executions() []ExecuteRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ExecuteRecord(nil), s.executions...)
}

// Inputs returns a snapshot of all input_reply messages received.
func (s *Server) Inputs() []InputRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]InputRecord(nil), s.inputs...)
}

func (s *Server) record(r *http.Request, body []byte) {
	var b []byte
	if body != nil {
		b = append([]byte(nil), body...)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, RequestLog{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(),
		Headers: r.Header.Clone(), Body: b,
	})
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/kernels", s.handleCreate)
	mux.HandleFunc("/api/kernels/", s.handleKernel)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		s.mu.Lock()
		unauthorized := s.unauthorized
		s.mu.Unlock()
		if unauthorized {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Colab-Client-Agent") == "" ||
			r.Header.Get("User-Agent") != "Jupyter Kernel Client" ||
			r.Header.Get("Accept") != "application/json" {
			http.Error(w, "missing kernel REST headers", http.StatusBadRequest)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) maybeHang(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	n := s.timeoutFirst
	if n > 0 {
		s.timeoutFirst = n - 1
	}
	s.mu.Unlock()
	if n == 0 {
		return false
	}
	<-r.Context().Done()
	return true
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if s.maybeHang(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	if s.createFail > 0 {
		s.createFail--
		s.mu.Unlock()
		http.Error(w, "cannot create kernel", http.StatusInternalServerError)
		return
	}
	s.kernelID = fmt.Sprintf("kernel-fake-%d", s.msgCounter.Add(1)+1)
	id := s.kernelID
	s.alive = true
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": "python3"})
}

func (s *Server) handleKernel(w http.ResponseWriter, r *http.Request) {
	if s.maybeHang(w, r) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/kernels/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	s.mu.Lock()
	effective := s.kernelID
	s.mu.Unlock()
	if id != effective {
		http.NotFound(w, r)
		return
	}
	if len(parts) >= 2 && parts[len(parts)-1] == "channels" {
		s.handleWS(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && len(parts) == 1:
		s.mu.Lock()
		alive := s.alive
		s.mu.Unlock()
		if !alive {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": "python3", "execution_state": "idle"})
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "restart":
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "interrupt":
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	case r.Method == http.MethodDelete && len(parts) == 1:
		writeJSON(w, http.StatusOK, map[string]any{})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("token") == "" || r.Header.Get("X-Colab-Runtime-Proxy-Token") == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(64 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		msg := map[string]any{}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		channel, _ := msg["channel"].(string)
		hdr, _ := msg["header"].(map[string]any)
		msgType, _ := hdr["msg_type"].(string)
		parent, _ := msg["parent_header"].(map[string]any)
		reqID, _ := hdr["msg_id"].(string)
		sessionID, _ := hdr["session"].(string)
		switch {
		case channel == "shell" && msgType == "execute_request":
			content, _ := msg["content"].(map[string]any)
			code, _ := content["code"].(string)
			allow, _ := content["allow_stdin"].(bool)
			s.mu.Lock()
			s.executions = append(s.executions, ExecuteRecord{
				Code: code, AllowStdin: allow, MsgID: reqID, SessionID: sessionID,
			})
			var steps []Step
			if len(s.scripts) > 0 {
				steps = s.scripts[0]
				s.scripts = s.scripts[1:]
			}
			s.mu.Unlock()
			go s.runScript(conn, reqID, sessionID, steps)
		case channel == "stdin" && msgType == "input_reply":
			content, _ := msg["content"].(map[string]any)
			value, _ := json.Marshal(content["value"])
			s.mu.Lock()
			s.inputs = append(s.inputs, InputRecord{
				Value: value, ParentHeader: parent, MsgType: msgType,
			})
			s.mu.Unlock()
		}
	}
}

func (s *Server) runScript(conn *websocket.Conn, execID, sessionID string, steps []Step) {
	if len(steps) == 0 {
		steps = []Step{
			{MsgType: "status", Content: map[string]any{"execution_state": "idle"}},
			{Channel: "shell", MsgType: "execute_reply", Content: map[string]any{"status": "ok"}},
		}
	}
	var last time.Duration
	var writeMu sync.Mutex
	for _, step := range steps {
		if d := step.Delay - last; d > 0 {
			time.Sleep(d)
		}
		last = step.Delay
		typ, data, err := encodeStep(step, execID, sessionID, s.msgCounter.Add(1))
		if err != nil {
			continue
		}
		writeMu.Lock()
		_ = conn.Write(context.Background(), typ, data)
		writeMu.Unlock()
	}
}

func encodeStep(step Step, execID, sessionID string, seq uint64) (websocket.MessageType, []byte, error) {
	channel := step.Channel
	if channel == "" {
		channel = "iopub"
	}
	header := map[string]any{
		"msg_id": fmt.Sprintf("msg-fake-%d", seq), "msg_type": step.MsgType,
		"username": "kernel", "session": sessionID,
		"date": time.Now().UTC().Format(time.RFC3339Nano), "version": "5.3",
	}
	for k, v := range step.Header {
		header[k] = v
	}
	parent := map[string]any{}
	switch step.Parent {
	case "", "exec":
		parent["msg_id"] = execID
	case "none":
	default:
		parent["msg_id"] = step.Parent
	}
	metadata := step.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	content := step.Content
	if content == nil {
		content = map[string]any{}
	}
	j, err := json.Marshal(map[string]any{
		"channel": channel, "header": header, "parent_header": parent,
		"metadata": metadata, "content": content,
	})
	if err != nil {
		return 0, nil, err
	}
	if len(step.Buffers) == 0 {
		return websocket.MessageText, j, nil
	}
	nbufs := len(step.Buffers) + 1
	offsets := make([]uint32, nbufs)
	off := uint32(4 * (nbufs + 1))
	offsets[0] = off
	off += uint32(len(j))
	parts := make([][]byte, 0, nbufs)
	parts = append(parts, j)
	for i, b := range step.Buffers {
		if i+1 < nbufs {
			offsets[i+1] = off
		}
		off += uint32(len(b))
		parts = append(parts, b)
	}
	out := make([]byte, 0, int(off))
	var b4 [4]byte
	binary.BigEndian.PutUint32(b4[:], uint32(nbufs))
	out = append(out, b4[:]...)
	for _, o := range offsets {
		binary.BigEndian.PutUint32(b4[:], o)
		out = append(out, b4[:]...)
	}
	for _, p := range parts {
		out = append(out, p...)
	}
	return websocket.MessageBinary, out, nil
}

// --- Step constructors -------------------------------------------------------

// Stream is a stdout/stderr stream message.
func Stream(name, text string) Step {
	return Step{MsgType: "stream", Content: map[string]any{"name": name, "text": text}}
}

// Status is an execution_state status message. Parent "none" simulates a
// status announced to all clients without a parent.
func Status(state string) Step {
	return Step{MsgType: "status", Content: map[string]any{"execution_state": state}}
}

// Reply is an execute_reply on the shell channel.
func Reply(status string, extra map[string]any) Step {
	content := map[string]any{"status": status}
	for k, v := range extra {
		content[k] = v
	}
	return Step{Channel: "shell", MsgType: "execute_reply", Content: content}
}

// ExecuteResult is an execute_result message.
func ExecuteResult(data map[string]any, count int) Step {
	return Step{MsgType: "execute_result", Content: map[string]any{
		"data": data, "metadata": map[string]any{}, "execution_count": count,
	}}
}

// DisplayData is a display_data message with an optional transient display id.
func DisplayData(data, metadata map[string]any, displayID string) Step {
	if data == nil {
		data = map[string]any{}
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	content := map[string]any{"data": data, "metadata": metadata}
	if displayID != "" {
		content["transient"] = map[string]any{"display_id": displayID}
	}
	return Step{MsgType: "display_data", Content: content}
}

// UpdateDisplayData replaces a previously displayed output in place.
func UpdateDisplayData(data, metadata map[string]any, displayID string) Step {
	if data == nil {
		data = map[string]any{}
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	return Step{MsgType: "update_display_data", Content: map[string]any{
		"data": data, "metadata": metadata,
		"transient": map[string]any{"display_id": displayID},
	}}
}

// ClearOutput clears all accumulated outputs.
func ClearOutput() Step { return Step{MsgType: "clear_output", Content: map[string]any{"wait": false}} }

// Error is an error iopub message.
func Error(ename, evalue string, traceback []string) Step {
	return Step{MsgType: "error", Content: map[string]any{
		"ename": ename, "evalue": evalue, "traceback": traceback,
	}}
}

// InputRequest is an input_request on the stdin channel.
func InputRequest(prompt string) Step {
	return Step{Channel: "stdin", MsgType: "input_request", Content: map[string]any{
		"prompt": prompt, "password": false,
	}}
}

// ColabRequest is a colab_request on iopub carrying a Drive-style authType.
func ColabRequest(authType, colabMsgID string) Step {
	return Step{MsgType: "colab_request", Metadata: map[string]any{"colab_msg_id": colabMsgID},
		Content: map[string]any{"request": map[string]any{"authType": authType}}}
}

// BigStream streams n bytes of text (for read-limit / 10 MB tests).
func BigStream(n int) Step {
	return Stream("stdout", strings.Repeat("x", n))
}
