package runtime

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MessageHeader is the Jupyter message envelope header.
type MessageHeader struct {
	MsgID    string `json:"msg_id"`
	MsgType  string `json:"msg_type"`
	Username string `json:"username,omitempty"`
	Session  string `json:"session,omitempty"`
	Date     string `json:"date,omitempty"`
	Version  string `json:"version,omitempty"`
}

// Message is one default-protocol Jupyter message. Buffers are carried by
// binary frames only and are not part of the JSON envelope.
type Message struct {
	Header       MessageHeader   `json:"header"`
	ParentHeader MessageHeader   `json:"parent_header"`
	Metadata     json.RawMessage `json:"metadata"`
	Content      json.RawMessage `json:"content"`
	Channel      string          `json:"channel"`
	Buffers      [][]byte        `json:"-"`
}

func rawObject(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage(`{}`)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func newMessage(msgType, channel, sessionID string, parent MessageHeader, content any) *Message {
	return &Message{
		Header: MessageHeader{
			MsgID: uuid.NewString(), MsgType: msgType, Username: "colab-go",
			Session: sessionID, Date: time.Now().UTC().Format(time.RFC3339Nano),
			Version: "5.3",
		},
		ParentHeader: parent,
		Metadata:     rawObject(nil),
		Content:      rawObject(content),
		Channel:      channel,
	}
}

func newMsgID() string { return uuid.NewString() }
func newSessionID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func (m *Message) contentMap() map[string]any {
	var v map[string]any
	_ = json.Unmarshal(m.Content, &v)
	if v == nil {
		v = map[string]any{}
	}
	return v
}

func (m *Message) metadataMap() map[string]any {
	var v map[string]any
	_ = json.Unmarshal(m.Metadata, &v)
	if v == nil {
		v = map[string]any{}
	}
	return v
}

func requestFromMessage(m *Message) Request {
	r := Request{
		MsgID: m.Header.MsgID, MsgType: m.Header.MsgType, Header: m.Header,
		Content: m.Content, Metadata: m.Metadata,
	}
	meta := m.metadataMap()
	if v, ok := meta["colab_msg_id"].(string); ok {
		r.ColabMsgID = v
	}
	content := m.contentMap()
	if req, ok := content["request"].(map[string]any); ok {
		if v, ok := req["authType"].(string); ok {
			r.AuthType = v
		}
	}
	return r
}
