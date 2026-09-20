package feishu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
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

// FakeServer provides a mock Feishu Open Platform HTTP and WebSocket server for testing.
type FakeServer struct {
	server *httptest.Server

	mu                 sync.Mutex
	token              string
	tokenErrCode       int
	tokenErrMsg        string
	endpointErrCode    int
	endpointErrMsg     string
	replyErrStatusCode int
	replyErrBody       string
	replies            []RecordedReply
	messages           []RecordedMessage

	wsMu            sync.Mutex
	activeWS        *websocket.Conn
	wsConnectedCh   chan struct{}
	wsClosedCh      chan struct{}
	responseFrames  chan Frame
	recordedResp    []Frame
	pingNotifyCh    chan struct{}
	receivedPings   int
	seqIDGen        uint64
	clientConfig    ClientConfig
	autoPong        bool
	lastPushedFrame *Frame
}

// NewFakeServer launches a local HTTP and WebSocket test server mimicking Feishu Open Platform.
func NewFakeServer() *FakeServer {
	fs := &FakeServer{
		token:          "mock_tenant_access_token_default",
		wsConnectedCh:  make(chan struct{}, 10),
		wsClosedCh:     make(chan struct{}, 10),
		responseFrames: make(chan Frame, 100),
		autoPong:       true,
		clientConfig: ClientConfig{
			ReconnectCount:    -1,
			ReconnectInterval: 120,
			ReconnectNonce:    30,
			PingInterval:      120,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /open-apis/auth/v3/tenant_access_token/internal", fs.handleToken)
	mux.HandleFunc("POST /open-apis/im/v1/messages/{message_id}/reply", fs.handleReply)
	mux.HandleFunc("POST /open-apis/im/v1/messages", fs.handleSendMessage)
	mux.HandleFunc("POST /callback/ws/endpoint", fs.handleEndpoint)
	mux.HandleFunc("/callback/ws/connect", fs.handleWSConnect)

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
	s.wsMu.Lock()
	if s.activeWS != nil {
		_ = s.activeWS.Close(websocket.StatusNormalClosure, "server closing")
	}
	s.wsMu.Unlock()
}

// WaitForConnection blocks until a WebSocket client connects or timeout expires.
func (s *FakeServer) WaitForConnection(timeout time.Duration) error {
	s.wsMu.Lock()
	if s.activeWS != nil {
		s.wsMu.Unlock()
		return nil
	}
	s.wsMu.Unlock()

	select {
	case <-s.wsConnectedCh:
		return nil
	case <-time.After(timeout):
		return errors.New("timeout waiting for websocket connection")
	}
}

// SetClientConfig updates the ClientConfig returned by the mock server.
func (s *FakeServer) SetClientConfig(cfg ClientConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clientConfig = cfg
}

// DisconnectWS forcibly terminates the current active WebSocket connection.
func (s *FakeServer) DisconnectWS() {
	s.wsMu.Lock()
	if s.activeWS != nil {
		_ = s.activeWS.Close(websocket.StatusGoingAway, "server kicked")
		s.activeWS = nil
	}
	s.wsMu.Unlock()
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

// SimulateEndpointError causes the endpoint discovery to return a Feishu error code.
func (s *FakeServer) SimulateEndpointError(code int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endpointErrCode = code
	s.endpointErrMsg = msg
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
	s.endpointErrCode = 0
	s.endpointErrMsg = ""
	s.replyErrStatusCode = 0
	s.replyErrBody = ""
}

// WaitForWSConnection waits until a client connects to the WebSocket endpoint.
func (s *FakeServer) WaitForWSConnection(timeout time.Duration) bool {
	select {
	case <-s.wsConnectedCh:
		return true
	case <-time.After(timeout):
		return false
	}
}

// WaitForWSClose waits until the active WebSocket connection is closed.
func (s *FakeServer) WaitForWSClose(timeout time.Duration) bool {
	s.wsMu.Lock()
	if s.activeWS == nil {
		s.wsMu.Unlock()
		return true
	}
	s.wsMu.Unlock()

	select {
	case <-s.wsClosedCh:
		return true
	case <-time.After(timeout):
		return false
	}
}

// SetPingNotifyChannel registers a channel to receive notifications whenever a ping frame arrives.
func (s *FakeServer) SetPingNotifyChannel(ch chan struct{}) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	s.pingNotifyCh = ch
}

// SetAutoPong configures whether the server automatically replies with a Pong frame upon receiving a Ping.
func (s *FakeServer) SetAutoPong(enabled bool) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	s.autoPong = enabled
}

// LastPushedFrame returns a copy of the most recent frame pushed by PushMessageEvent.
func (s *FakeServer) LastPushedFrame() *Frame {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if s.lastPushedFrame == nil {
		return nil
	}
	cp := *s.lastPushedFrame
	return &cp
}

// RecordedResponseFrames returns a copy of all response/data frames received from the client.
func (s *FakeServer) RecordedResponseFrames() []Frame {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	out := make([]Frame, len(s.recordedResp))
	copy(out, s.recordedResp)
	return out
}

// SimulateAuthFailure configures the endpoint discovery API to return Feishu error 514 (auth failed).
func (s *FakeServer) SimulateAuthFailure() {
	s.SimulateEndpointError(514, "auth failed: invalid app_id or app_secret")
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

func (s *FakeServer) handleEndpoint(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.endpointErrCode != 0 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": s.endpointErrCode,
			"msg":  s.endpointErrMsg,
		})
		return
	}

	wsURL := strings.Replace(s.server.URL, "http://", "ws://", 1) + "/callback/ws/connect?service_id=12345"

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"msg":  "success",
		"data": map[string]any{
			"url":          wsURL,
			"ClientConfig": s.clientConfig,
		},
	})
}

func (s *FakeServer) handleWSConnect(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(10 * 1024 * 1024)

	s.wsMu.Lock()
	s.activeWS = conn
	s.wsMu.Unlock()

	select {
	case s.wsConnectedCh <- struct{}{}:
	default:
	}

	defer func() {
		s.wsMu.Lock()
		if s.activeWS == conn {
			s.activeWS = nil
		}
		s.wsMu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "closed")
		select {
		case s.wsClosedCh <- struct{}{}:
		default:
		}
	}()

	for {
		msgType, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if msgType != websocket.MessageBinary {
			continue
		}

		var f Frame
		if err := f.Unmarshal(data); err != nil {
			continue
		}

		if f.Method == 0 && f.HeaderValue("type") == "ping" {
			s.wsMu.Lock()
			s.receivedPings++
			autoPong := s.autoPong
			notifyCh := s.pingNotifyCh
			s.wsMu.Unlock()

			// Echo pong frame if autoPong is enabled
			if autoPong {
				pong := Frame{
					SeqID:   f.SeqID,
					LogID:   f.LogID,
					Service: f.Service,
					Method:  0,
					Headers: []Header{
						{Key: "type", Value: "pong"},
					},
				}
				s.wsMu.Lock()
				confBytes, _ := json.Marshal(s.clientConfig)
				s.wsMu.Unlock()
				pong.Payload = confBytes
				wire, err := pong.Marshal()
				if err == nil {
					_ = conn.Write(r.Context(), websocket.MessageBinary, wire)
				}
			}

			if notifyCh != nil {
				select {
				case notifyCh <- struct{}{}:
				default:
				}
			}
		} else if f.Method == 1 {
			// Response frame or data frame from client
			s.wsMu.Lock()
			s.recordedResp = append(s.recordedResp, f)
			s.wsMu.Unlock()

			select {
			case s.responseFrames <- f:
			default:
			}
		}
	}
}

// PushMessageEvent sends an im.message.receive_v1 event frame to the connected client
// and waits for the client's echoed response frame.
func (s *FakeServer) PushMessageEvent(ctx context.Context, eventID, messageID, chatID, openID, text string) (*Frame, error) {
	s.wsMu.Lock()
	conn := s.activeWS
	s.wsMu.Unlock()

	if conn == nil {
		return nil, errors.New("no active websocket client connected")
	}

	eventBytes, err := s.BuildMessageReceiveEvent(eventID, messageID, chatID, openID, text)
	if err != nil {
		return nil, err
	}

	seqID := atomic.AddUint64(&s.seqIDGen, 1)
	frame := Frame{
		SeqID:   seqID,
		LogID:   seqID + 1000,
		Service: 12345,
		Method:  1, // Data
		Headers: []Header{
			{Key: "type", Value: "event"},
			{Key: "message_id", Value: messageID},
			{Key: "sum", Value: "1"},
			{Key: "seq", Value: "0"},
			{Key: "trace_id", Value: "trace_" + eventID},
			{Key: "timestamp", Value: strconv.FormatInt(time.Now().UnixMilli(), 10)},
		},
		Payload: eventBytes,
	}

	s.wsMu.Lock()
	s.lastPushedFrame = &frame
	s.wsMu.Unlock()

	wire, err := frame.Marshal()
	if err != nil {
		return nil, err
	}

	if err := conn.Write(ctx, websocket.MessageBinary, wire); err != nil {
		return nil, fmt.Errorf("write frame failed: %w", err)
	}

	// Wait for response frame
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-s.responseFrames:
		return &resp, nil
	}
}

// PushFragmentedMessageEvent splits an im.message.receive_v1 event payload into numFragments frames,
// and sends them according to the given order (e.g. [0, 1, 2] or [2, 0, 1]).
// Returns the response frame received from the client upon completion of the reassembly.
func (s *FakeServer) PushFragmentedMessageEvent(
	ctx context.Context,
	eventID, messageID, chatID, openID, text string,
	numFragments int,
	order []int,
) (*Frame, error) {
	s.wsMu.Lock()
	conn := s.activeWS
	s.wsMu.Unlock()

	if conn == nil {
		return nil, errors.New("no active websocket client connected")
	}
	if numFragments <= 1 {
		return s.PushMessageEvent(ctx, eventID, messageID, chatID, openID, text)
	}

	eventBytes, err := s.BuildMessageReceiveEvent(eventID, messageID, chatID, openID, text)
	if err != nil {
		return nil, err
	}

	// Split eventBytes into numFragments chunks
	chunkSize := (len(eventBytes) + numFragments - 1) / numFragments
	chunks := make([][]byte, numFragments)
	for i := 0; i < numFragments; i++ {
		start := i * chunkSize
		if start > len(eventBytes) {
			start = len(eventBytes)
		}
		end := start + chunkSize
		if end > len(eventBytes) {
			end = len(eventBytes)
		}
		chunks[i] = eventBytes[start:end]
	}

	if len(order) == 0 {
		order = make([]int, numFragments)
		for i := 0; i < numFragments; i++ {
			order[i] = i
		}
	}

	// Prepare frames
	frames := make([]Frame, numFragments)
	for seq := 0; seq < numFragments; seq++ {
		seqID := atomic.AddUint64(&s.seqIDGen, 1)
		frames[seq] = Frame{
			SeqID:   seqID,
			LogID:   seqID + 1000,
			Service: 12345,
			Method:  1, // Data
			Headers: []Header{
				{Key: "type", Value: "event"},
				{Key: "message_id", Value: messageID},
				{Key: "sum", Value: strconv.Itoa(numFragments)},
				{Key: "seq", Value: strconv.Itoa(seq)},
				{Key: "trace_id", Value: "trace_" + eventID},
				{Key: "timestamp", Value: strconv.FormatInt(time.Now().UnixMilli(), 10)},
			},
			Payload: chunks[seq],
		}
	}

	// Send frames in the specified order
	for _, seq := range order {
		if seq < 0 || seq >= numFragments {
			return nil, fmt.Errorf("invalid sequence index in order: %d", seq)
		}
		wire, err := frames[seq].Marshal()
		if err != nil {
			return nil, err
		}
		if err := conn.Write(ctx, websocket.MessageBinary, wire); err != nil {
			return nil, fmt.Errorf("write fragment %d failed: %w", seq, err)
		}
	}

	// The client should send exactly 1 response frame once all fragments are received
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-s.responseFrames:
		return &resp, nil
	}
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
