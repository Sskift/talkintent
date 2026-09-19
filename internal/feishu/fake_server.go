package feishu

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RecordedReply captures an outgoing IM reply sent to the fake Feishu server.
type RecordedReply struct {
	MessageID  string    `json:"message_id"`
	MsgType    string    `json:"msg_type"`
	RawContent string    `json:"raw_content"`
	Text       string    `json:"text"`
	AuthHeader string    `json:"auth_header"`
	ReceivedAt time.Time `json:"received_at"`
}

// RecordedMessage captures an outgoing IM message sent to the fake Feishu server.
type RecordedMessage struct {
	ReceiveIDType string    `json:"receive_id_type"`
	ReceiveID     string    `json:"receive_id"`
	MsgType       string    `json:"msg_type"`
	RawContent    string    `json:"raw_content"`
	Text          string    `json:"text"`
	AuthHeader    string    `json:"auth_header"`
	ReceivedAt    time.Time `json:"received_at"`
}

// FakeServer provides a mock Feishu Open Platform HTTP server for testing.
// It serves tenant_access_token requests, records replies/messages, and can sign/encrypt events.
type FakeServer struct {
	server *httptest.Server

	mu                 sync.Mutex
	token              string
	tokenErrCode       int
	tokenErrMsg        string
	replyErrStatusCode int
	replyErrBody       string
	replies            []RecordedReply
	messages           []RecordedMessage
}

// NewFakeServer launches a local HTTP test server mimicking Feishu Open Platform.
func NewFakeServer() *FakeServer {
	fs := &FakeServer{
		token: "mock_tenant_access_token_default",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /open-apis/auth/v3/tenant_access_token/internal", fs.handleToken)
	mux.HandleFunc("POST /open-apis/im/v1/messages/{message_id}/reply", fs.handleReply)
	mux.HandleFunc("POST /open-apis/im/v1/messages", fs.handleSendMessage)

	fs.server = httptest.NewServer(mux)
	return fs
}

// URL returns the base URL of the fake server (e.g. http://127.0.0.1:xxxxx).
func (s *FakeServer) URL() string {
	return s.server.URL
}

// Close terminates the mock server.
func (s *FakeServer) Close() {
	s.server.Close()
}

// SetToken configures the tenant_access_token returned by the mock server.
func (s *FakeServer) SetToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
}

// SimulateTokenError causes the token endpoint to return a Feishu error code.
func (s *FakeServer) SimulateTokenError(code int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenErrCode = code
	s.tokenErrMsg = msg
}

// SimulateReplyError causes the reply endpoint to return an HTTP status code and error body.
func (s *FakeServer) SimulateReplyError(statusCode int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replyErrStatusCode = statusCode
	s.replyErrBody = body
}

// Reset clears recorded replies, messages, and simulated errors.
func (s *FakeServer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = nil
	s.messages = nil
	s.tokenErrCode = 0
	s.tokenErrMsg = ""
	s.replyErrStatusCode = 0
	s.replyErrBody = ""
}

// Replies returns a copy of all recorded replies received by the fake server.
func (s *FakeServer) Replies() []RecordedReply {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RecordedReply, len(s.replies))
	copy(out, s.replies)
	return out
}

// LastReply returns the most recent reply received, or nil if none.
func (s *FakeServer) LastReply() *RecordedReply {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.replies) == 0 {
		return nil
	}
	cp := s.replies[len(s.replies)-1]
	return &cp
}

// Messages returns a copy of all recorded messages sent to the fake server.
func (s *FakeServer) Messages() []RecordedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RecordedMessage, len(s.messages))
	copy(out, s.messages)
	return out
}

// LastMessage returns the most recent message sent, or nil if none.
func (s *FakeServer) LastMessage() *RecordedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return nil
	}
	cp := s.messages[len(s.messages)-1]
	return &cp
}

func (s *FakeServer) handleToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tokenErrCode != 0 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": s.tokenErrCode,
			"msg":  s.tokenErrMsg,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":                0,
		"msg":                 "ok",
		"tenant_access_token": s.token,
		"expire":              7200,
	})
}

func (s *FakeServer) handleReply(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.replyErrStatusCode != 0 {
		w.WriteHeader(s.replyErrStatusCode)
		_, _ = w.Write([]byte(s.replyErrBody))
		return
	}

	path := r.URL.Path
	// Extract message_id from /open-apis/im/v1/messages/{message_id}/reply
	parts := strings.Split(strings.Trim(path, "/"), "/")
	messageID := ""
	if len(parts) >= 6 && parts[5] == "reply" {
		messageID = parts[4]
	}

	bodyBytes, _ := io.ReadAll(r.Body)

	var payload struct {
		Content string `json:"content"`
		MsgType string `json:"msg_type"`
	}
	_ = json.Unmarshal(bodyBytes, &payload)

	text, _ := ParseTextContent(payload.Content)

	s.replies = append(s.replies, RecordedReply{
		MessageID:  messageID,
		MsgType:    payload.MsgType,
		RawContent: payload.Content,
		Text:       text,
		AuthHeader: r.Header.Get("Authorization"),
		ReceivedAt: time.Now(),
	})

	randomHex := make([]byte, 8)
	_, _ = rand.Read(randomHex)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"msg":  "success",
		"data": map[string]string{
			"message_id": "om_reply_" + hex.EncodeToString(randomHex),
		},
	})
}

func (s *FakeServer) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bodyBytes, _ := io.ReadAll(r.Body)

	var payload struct {
		ReceiveID string `json:"receive_id"`
		Content   string `json:"content"`
		MsgType   string `json:"msg_type"`
	}
	_ = json.Unmarshal(bodyBytes, &payload)

	text, _ := ParseTextContent(payload.Content)

	s.messages = append(s.messages, RecordedMessage{
		ReceiveIDType: r.URL.Query().Get("receive_id_type"),
		ReceiveID:     payload.ReceiveID,
		MsgType:       payload.MsgType,
		RawContent:    payload.Content,
		Text:          text,
		AuthHeader:    r.Header.Get("Authorization"),
		ReceivedAt:    time.Now(),
	})

	randomHex := make([]byte, 8)
	_, _ = rand.Read(randomHex)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"msg":  "success",
		"data": map[string]string{
			"message_id": "om_msg_" + hex.EncodeToString(randomHex),
		},
	})
}

// SignEvent computes the Feishu signature for an event payload.
func (s *FakeServer) SignEvent(timestamp, nonce, encryptKey string, body []byte) string {
	return CalculateSignature(timestamp, nonce, encryptKey, body)
}

// BuildChallengeEvent creates a JSON byte slice for a plain URL verification challenge.
func (s *FakeServer) BuildChallengeEvent(challenge, token string) ([]byte, error) {
	payload := map[string]string{
		"challenge": challenge,
		"token":     token,
		"type":      "url_verification",
	}
	return json.Marshal(payload)
}

// BuildEncryptedChallengeEvent creates an encrypted URL verification challenge payload: {"encrypt": "..."}.
func (s *FakeServer) BuildEncryptedChallengeEvent(challenge, token, encryptKey string) ([]byte, error) {
	plain, err := s.BuildChallengeEvent(challenge, token)
	if err != nil {
		return nil, err
	}
	enc, err := EncryptPayload(plain, encryptKey)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"encrypt": enc})
}

// BuildMessageReceiveEvent constructs a Feishu schema 2.0 im.message.receive_v1 event JSON payload.
func (s *FakeServer) BuildMessageReceiveEvent(eventID, messageID, chatID, openID, text string) ([]byte, error) {
	contentObj := map[string]string{"text": text}
	contentBytes, _ := json.Marshal(contentObj)

	event := EventEnvelope{
		Schema: "2.0",
		Header: &EventHeader{
			EventID:    eventID,
			EventType:  "im.message.receive_v1",
			CreateTime: strconv.FormatInt(time.Now().UnixMilli(), 10),
			Token:      "ver_mock_token",
		},
		Event: &EventBody{
			Sender: &EventSender{
				SenderID: &SenderID{
					OpenID: openID,
				},
				SenderType: "user",
			},
			Message: &EventMessage{
				MessageID:   messageID,
				ChatID:      chatID,
				ChatType:    "p2p",
				MessageType: "text",
				Content:     string(contentBytes),
			},
		},
	}
	return json.Marshal(event)
}

// BuildEncryptedMessageReceiveEvent constructs an encrypted im.message.receive_v1 payload.
func (s *FakeServer) BuildEncryptedMessageReceiveEvent(eventID, messageID, chatID, openID, text, encryptKey string) ([]byte, error) {
	plain, err := s.BuildMessageReceiveEvent(eventID, messageID, chatID, openID, text)
	if err != nil {
		return nil, err
	}
	enc, err := EncryptPayload(plain, encryptKey)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"encrypt": enc})
}

// BuildSignedRequest constructs an *http.Request with valid X-Lark-* signature headers.
func (s *FakeServer) BuildSignedRequest(targetURL, encryptKey string, body []byte) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "mock_nonce_12345"
	req.Header.Set("X-Lark-Request-Timestamp", ts)
	req.Header.Set("X-Lark-Request-Nonce", nonce)

	if encryptKey != "" {
		sig := CalculateSignature(ts, nonce, encryptKey, body)
		req.Header.Set("X-Lark-Signature", sig)
	}
	return req, nil
}

// BuildEncryptedSignedRequest encrypts plainJSON and builds an *http.Request with signature headers.
func (s *FakeServer) BuildEncryptedSignedRequest(targetURL, encryptKey string, plainJSON []byte) (*http.Request, error) {
	enc, err := EncryptPayload(plainJSON, encryptKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt payload failed: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"encrypt": enc})
	return s.BuildSignedRequest(targetURL, encryptKey, body)
}
