package feishu

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var userMentionRegex = regexp.MustCompile(`@_user_\d+`)

// EventEnvelope is the top-level Feishu event payload.
type EventEnvelope struct {
	Schema    string       `json:"schema,omitempty"`
	Header    *EventHeader `json:"header,omitempty"`
	Event     *EventBody   `json:"event,omitempty"`
	Challenge string       `json:"challenge,omitempty"`
	Token     string       `json:"token,omitempty"`
	Type      string       `json:"type,omitempty"`
	Encrypt   string       `json:"encrypt,omitempty"`

	// v1 schema fallbacks
	UUID string `json:"uuid,omitempty"`
	TS   string `json:"ts,omitempty"`
}

// EventHeader contains metadata for schema 2.0 events.
type EventHeader struct {
	EventID    string `json:"event_id"`
	EventType  string `json:"event_type"`
	CreateTime string `json:"create_time"`
	Token      string `json:"token"`
	AppID      string `json:"app_id"`
	TenantKey  string `json:"tenant_key"`
}

// EventBody contains the event-specific payload.
type EventBody struct {
	Sender  *EventSender  `json:"sender,omitempty"`
	Message *EventMessage `json:"message,omitempty"`
}

// EventSender identifies the initiator of the event.
type EventSender struct {
	SenderID   *SenderID `json:"sender_id,omitempty"`
	SenderType string    `json:"sender_type,omitempty"` // "user", "app"
	TenantKey  string    `json:"tenant_key,omitempty"`
}

// SenderID contains various Feishu user identifiers.
type SenderID struct {
	OpenID  string `json:"open_id,omitempty"`
	UserID  string `json:"user_id,omitempty"`
	UnionID string `json:"union_id,omitempty"`
}

// EventMessage contains the message payload for im.message.receive_v1.
type EventMessage struct {
	MessageID   string    `json:"message_id"`
	RootID      string    `json:"root_id,omitempty"`
	ParentID    string    `json:"parent_id,omitempty"`
	CreateTime  string    `json:"create_time,omitempty"`
	ChatID      string    `json:"chat_id"`
	ChatType    string    `json:"chat_type"`    // "p2p", "group"
	MessageType string    `json:"message_type"` // "text", ...
	Content     string    `json:"content"`      // JSON string: {"text": "..."}
	Mentions    []Mention `json:"mentions,omitempty"`
}

// Mention describes a user or bot mention in a message.
type Mention struct {
	Key       string    `json:"key"` // e.g. "@_user_1"
	ID        *SenderID `json:"id,omitempty"`
	Name      string    `json:"name"`
	TenantKey string    `json:"tenant_key,omitempty"`
}

// ParseTextContent parses a Feishu text message content JSON (e.g. {"text": "hello"}).
// If the content is already plain text (non-JSON), it returns the content as is.
func ParseTextContent(contentJSON string) (string, error) {
	trimmed := strings.TrimSpace(contentJSON)
	if !strings.HasPrefix(trimmed, "{") {
		return trimmed, nil
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return "", fmt.Errorf("failed to parse message content json: %w", err)
	}
	return parsed.Text, nil
}

// StripMentions parses the message content JSON, removes @mention placeholders and bot names,
// and returns the clean user query text.
func StripMentions(contentJSON string, mentions []Mention) (string, error) {
	text, err := ParseTextContent(contentJSON)
	if err != nil {
		return "", err
	}

	// 1. Strip explicit mention keys (e.g. @_user_1) and bot names (e.g. @BotName)
	for _, m := range mentions {
		if m.Key != "" {
			text = strings.ReplaceAll(text, m.Key, "")
		}
		if m.Name != "" {
			text = strings.ReplaceAll(text, "@"+m.Name, "")
			text = strings.ReplaceAll(text, m.Name, "")
		}
	}

	// 2. Strip any remaining Feishu mention keys like @_user_2
	text = userMentionRegex.ReplaceAllString(text, "")

	// 3. Normalize spaces
	clean := strings.TrimSpace(text)
	return clean, nil
}
