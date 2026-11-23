package runtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

// Target identifies a runtime proxy and a (possibly cached) kernel id. Token
// is the data-plane runtime proxy token, never a control-plane bearer token.
type Target struct {
	URL      string
	Token    logx.Secret
	Endpoint string
	KernelID string
}

// Config configures a Runtime. Zero values are usable; HTTPClient is primarily
// for tests that need to trust a fake server's TLS certificate.
type Config struct {
	Logger      *slog.Logger
	HTTPClient  *http.Client
	ClientAgent string

	// RequestTimeout bounds each REST call (default 30s).
	RequestTimeout time.Duration
	// RetryBackoff returns the delay before retry attempt i (0,1). The default
	// is 2s, 4s. Only timeouts are retried, at most three attempts in total.
	RetryBackoff func(attempt int) time.Duration
	// GracefulCancelTimeout bounds the wait for idle after a cancelled execute
	// has sent a REST interrupt (default 2s).
	GracefulCancelTimeout time.Duration
}

// AttachOptions carries callbacks for the ids chosen during Attach. The
// orchestrator must persist the kernel id reported by OnKernelStarted: it is
// always the *effective* id, including when a stale cached id was replaced.
type AttachOptions struct {
	OnKernelStarted  func(id string)
	OnSessionStarted func(id string)
}

// ExecOptions controls one Execute call.
type ExecOptions struct {
	// IdleTimeout is the longest silence from this execution before failing.
	// Zero (or negative) means unlimited.
	IdleTimeout time.Duration
	// AllowStdin lets the kernel request input via StdinHook.
	AllowStdin bool
	// OnOutput is called once for each newly appended output.
	OnOutput func(Output)
	// StdinHook answers an input_request prompt.
	StdinHook func(prompt string) (string, error)
	// ColabRequestHook handles a colab_request. It is invoked off the WebSocket
	// reader goroutine and must return true to mark the request intercepted.
	// To answer it, call Kernel.SendColabReply from the hook.
	ColabRequestHook func(Request) bool
}

// Result is the outcome of one execution.
type Result struct {
	Outputs        []Output
	Status         string
	ExecutionCount *int
}

// Transient is the display-id carried by display_data/update_display_data.
type Transient struct {
	DisplayID string `json:"display_id,omitempty"`
}

// Output is one notebook output. Text is a string (a Jupyter list-of-strings
// stream payload is joined on unmarshal). Data and Metadata preserve the MIME
// bundle verbatim. Buffers attached to a binary frame are deliberately ignored
// by the accumulator, matching docs/02 §3.5.
type Output struct {
	OutputType     string         `json:"output_type"`
	Name           string         `json:"name,omitempty"`
	Text           string         `json:"text,omitempty"`
	Data           map[string]any `json:"data,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	ExecutionCount *int           `json:"execution_count,omitempty"`
	EName          string         `json:"ename,omitempty"`
	EValue         string         `json:"evalue,omitempty"`
	Traceback      []string       `json:"traceback,omitempty"`
	Transient      *Transient     `json:"transient,omitempty"`
}

// UnmarshalJSON accepts both string and list-of-strings stream payloads.
func (o *Output) UnmarshalJSON(b []byte) error {
	var w struct {
		OutputType     string          `json:"output_type"`
		Name           string          `json:"name"`
		Text           json.RawMessage `json:"text"`
		Data           map[string]any  `json:"data"`
		Metadata       map[string]any  `json:"metadata"`
		ExecutionCount *int            `json:"execution_count"`
		EName          string          `json:"ename"`
		EValue         string          `json:"evalue"`
		Traceback      []string        `json:"traceback"`
		Transient      *Transient      `json:"transient"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*o = Output{
		OutputType: w.OutputType, Name: w.Name, Data: w.Data, Metadata: w.Metadata,
		ExecutionCount: w.ExecutionCount, EName: w.EName, EValue: w.EValue,
		Traceback: w.Traceback, Transient: w.Transient,
	}
	if len(w.Text) == 0 || string(w.Text) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(w.Text, &s) == nil {
		o.Text = s
		return nil
	}
	var parts []string
	if json.Unmarshal(w.Text, &parts) == nil {
		o.Text = strings.Join(parts, "")
		return nil
	}
	o.Text = string(w.Text)
	return nil
}

// Request is a parsed colab_request. Header is the request header and must be
// used as the parent_header of the colab_reply sent on stdin.
type Request struct {
	MsgID      string
	MsgType    string
	Header     MessageHeader
	Content    json.RawMessage
	Metadata   json.RawMessage
	AuthType   string
	ColabMsgID string
}

// DefaultReplyBody is the standard colab_reply payload for this request.
func (r Request) DefaultReplyBody() map[string]any {
	return map[string]any{"type": "colab_reply", "colab_msg_id": r.ColabMsgID}
}

// Runtime attaches kernels on a runtime proxy.
type Runtime interface {
	Attach(ctx context.Context, target Target, opts AttachOptions) (Kernel, error)
}

// Kernel is one attached Jupyter kernel. Close only closes the connection;
// Shutdown also deletes the kernel on the server.
type Kernel interface {
	ID() string
	SessionID() string
	Execute(ctx context.Context, code string, opts ExecOptions) (*Result, error)
	Restart(ctx context.Context) error
	Interrupt(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Close() error
	// SendColabReply sends an input_reply on stdin wrapping a colab_reply,
	// with parent_header set to the intercepted request's header. A nil body
	// uses Request.DefaultReplyBody.
	SendColabReply(ctx context.Context, req Request, body any) error
}
