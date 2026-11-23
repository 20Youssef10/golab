package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/20Youssef10/golab/internal/logx"
)

var (
	// ErrCorrupt means a history file exists but contains an invalid line. It
	// is never treated as an empty log (docs/03 ADR-004).
	ErrCorrupt = errors.New("corrupt history file")
	// ErrUnsupportedFormat is returned by Export for an unknown format.
	ErrUnsupportedFormat = errors.New("unsupported history export format")
)

// Event types used by the Go port. The first ten are the canonical list in
// docs/01 §5; the last two also appear in upstream logs.
const (
	EventSessionCreated    = "session_created"
	EventSessionTerminated = "session_terminated"
	EventExecution         = "execution"
	EventStdinRequest      = "stdin_request"
	EventInputReply        = "input_reply"
	EventFileOperation     = "file_operation"
	EventAutomation        = "automation"
	EventAutomationResult  = "automation_result"
	EventColabRequest      = "colab_request"
	EventDriveAuthNeeded   = "drive_auth_needed"
	EventDriveAuthSuccess  = "drive_auth_success"

	EventInputRequested = "input_requested"
	EventReplStarted    = "repl_started"
)

// Event is one JSONL record. Payload is a JSON object whose keys are emitted
// at the top level of the line, exactly as upstream's
// {"timestamp":…, "event_type":…, **data} does.
type Event struct {
	Timestamp time.Time
	EventType string
	Payload   json.RawMessage
}

const (
	pythonTimeLayout = "2006-01-02T15:04:05.999999Z07:00"
	pythonTimeOutput = "2006-01-02T15:04:05.000000+00:00"
	// displayTimeLayout mirrors converter.py's
	// timestamp.split(".")[0].replace("T", " ").
	displayTimeLayout = "2006-01-02 15:04:05"
)

func formatTimestamp(t time.Time) string { return t.UTC().Format(pythonTimeOutput) }

func parseTimestamp(s string) (time.Time, error) { return time.Parse(pythonTimeLayout, s) }

func displayTimestamp(e Event) string { return e.Timestamp.UTC().Format(displayTimeLayout) }

// MarshalJSON flattens Payload into the top-level object.
func (e Event) MarshalJSON() ([]byte, error) {
	m := make(map[string]json.RawMessage)
	ts, err := json.Marshal(formatTimestamp(e.Timestamp))
	if err != nil {
		return nil, err
	}
	m["timestamp"] = ts
	etype, err := json.Marshal(e.EventType)
	if err != nil {
		return nil, err
	}
	m["event_type"] = etype

	if len(e.Payload) > 0 {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return nil, fmt.Errorf("history: event payload must be a JSON object: %w", err)
		}
		for k, v := range payload {
			// The structural fields always win over payload keys of the same
			// name; otherwise a payload could forge a timestamp.
			if k == "timestamp" || k == "event_type" {
				continue
			}
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// UnmarshalJSON reads the flattened object back into Timestamp, EventType and
// Payload.
func (e *Event) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	tsRaw, ok := raw["timestamp"]
	if !ok {
		return errors.New("history: event missing timestamp")
	}
	var ts string
	if err := json.Unmarshal(tsRaw, &ts); err != nil {
		return err
	}
	t, err := parseTimestamp(ts)
	if err != nil {
		return fmt.Errorf("history: invalid timestamp %q: %w", ts, err)
	}
	etRaw, ok := raw["event_type"]
	if !ok {
		return errors.New("history: event missing event_type")
	}
	var etype string
	if err := json.Unmarshal(etRaw, &etype); err != nil {
		return err
	}
	delete(raw, "timestamp")
	delete(raw, "event_type")
	payload, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	*e = Event{Timestamp: t, EventType: etype, Payload: payload}
	return nil
}

// fields decodes Payload as a generic JSON object.
func (e Event) fields() map[string]any {
	m := map[string]any{}
	if len(e.Payload) > 0 {
		if err := json.Unmarshal(e.Payload, &m); err != nil || m == nil {
			return map[string]any{}
		}
	}
	return m
}

func (e Event) stringField(key string) string {
	s, _ := e.fields()[key].(string)
	return s
}

func (e Event) outputs() []map[string]any {
	raw, ok := e.fields()["outputs"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

var sensitiveKeys = map[string]bool{
	"token": true, "access_token": true, "refresh_token": true,
	"client_secret": true, "secret": true, "authorization": true,
	"cookie": true, "set-cookie": true, "verifier": true,
	"password": true, "x-colab-runtime-proxy-token": true,
	"x-goog-colab-token": true,
}

// redactedPayload marshals a free-form payload and runs logx.Redact over every
// string value (plus key-based redaction for credential fields). Applying the
// redactor to string values rather than to the whole JSON document keeps the
// result valid JSON. The caller's value is never mutated.
func redactedPayload(payload any) (json.RawMessage, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	if _, ok := v.(map[string]any); !ok {
		v = map[string]any{"value": v}
	}
	return json.Marshal(redactJSONValue(v))
}

func redactJSONValue(v any) any {
	switch x := v.(type) {
	case string:
		return logx.Redact(x)
	case []any:
		for i := range x {
			x[i] = redactJSONValue(x[i])
		}
		return x
	case map[string]any:
		for k, value := range x {
			if sensitiveKeys[strings.ToLower(k)] {
				x[k] = "REDACTED"
			} else {
				x[k] = redactJSONValue(value)
			}
		}
		return x
	default:
		return v
	}
}
