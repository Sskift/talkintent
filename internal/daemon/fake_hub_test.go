package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Sskift/talkintent/internal/protocol"
)

// FakeHub represents a mock central Hub server for testing WebSocket client daemon behaviors.
type FakeHub struct {
	t            *testing.T
	server       *httptest.Server
	mu           sync.Mutex
	writeMu      sync.Mutex
	activeConn   *websocket.Conn
	headers      http.Header
	helloPayload *protocol.DaemonHelloPayload
	received     chan protocol.Envelope
	autoAck      bool
	autoPong     bool
	ackInterval  int
	sessionID    string
}

// NewFakeHub starts an httptest HTTP and WebSocket server.
func NewFakeHub(t *testing.T) *FakeHub {
	hub := &FakeHub{
		t:           t,
		received:    make(chan protocol.Envelope, 64),
		autoAck:     true,
		autoPong:    true,
		ackInterval: 20,
		sessionID:   "sess_fake_001",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", hub.handleWebSocket)
	hub.server = httptest.NewServer(mux)

	return hub
}

// URL returns the HTTP URL of the fake hub.
func (h *FakeHub) URL() string {
	return h.server.URL
}

// Close shuts down the test server and closes active connections.
func (h *FakeHub) Close() {
	h.mu.Lock()
	if h.activeConn != nil {
		_ = h.activeConn.Close(websocket.StatusNormalClosure, "server shutting down")
	}
	h.mu.Unlock()
	h.server.Close()
}

// Headers returns the HTTP headers sent during the last WebSocket handshake.
func (h *FakeHub) Headers() http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	copied := make(http.Header)
	for k, v := range h.headers {
		copied[k] = v
	}
	return copied
}

// SetAutoAck configures whether daemon_hello receives an automatic hub_ack.
func (h *FakeHub) SetAutoAck(auto bool, intervalSec int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.autoAck = auto
	h.ackInterval = intervalSec
}

// SetAutoPong configures whether heartbeat_ping receives an automatic heartbeat_pong.
func (h *FakeHub) SetAutoPong(auto bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.autoPong = auto
}

// CloseActiveConn drops the current WebSocket connection with status code and reason.
func (h *FakeHub) CloseActiveConn(code websocket.StatusCode, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.activeConn != nil {
		_ = h.activeConn.Close(code, reason)
		h.activeConn = nil
	}
}

// SendQuery transmits a query_request frame to the connected daemon.
func (h *FakeHub) SendQuery(req protocol.QueryRequestPayload) error {
	return h.SendEnvelope(protocol.TypeQueryRequest, req)
}

// SendCancel transmits a query_cancel frame to the connected daemon.
func (h *FakeHub) SendCancel(queryID, reason string) error {
	cancel := protocol.QueryCancelPayload{
		QueryID: queryID,
		Reason:  reason,
	}
	return h.SendEnvelope(protocol.TypeQueryCancel, cancel)
}

// SendEnvelope writes an arbitrary envelope to the connected daemon.
func (h *FakeHub) SendEnvelope(msgType string, payload any) error {
	h.mu.Lock()
	conn := h.activeConn
	h.mu.Unlock()

	if conn == nil {
		return errors.New("no active daemon connection")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := protocol.Envelope{
		Version:   protocol.Version1,
		Type:      msgType,
		ID:        fmt.Sprintf("hub_msg_%d", time.Now().UnixNano()),
		Timestamp: time.Now().UnixMilli(),
		Payload:   raw,
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}

	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	return conn.Write(context.Background(), websocket.MessageText, b)
}

// SendRawEnvelope writes a raw envelope struct directly to the connected daemon.
func (h *FakeHub) SendRawEnvelope(env protocol.Envelope) error {
	h.mu.Lock()
	conn := h.activeConn
	h.mu.Unlock()

	if conn == nil {
		return errors.New("no active daemon connection")
	}

	b, err := json.Marshal(env)
	if err != nil {
		return err
	}

	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	return conn.Write(context.Background(), websocket.MessageText, b)
}

// WaitForHello blocks until a daemon_hello envelope is received or timeout.
func (h *FakeHub) WaitForHello(timeout time.Duration) (*protocol.DaemonHelloPayload, error) {
	deadline := time.After(timeout)
	for {
		select {
		case env := <-h.received:
			if env.Type == protocol.TypeDaemonHello {
				var hello protocol.DaemonHelloPayload
				if err := json.Unmarshal(env.Payload, &hello); err != nil {
					return nil, err
				}
				return &hello, nil
			}
		case <-deadline:
			return nil, errors.New("timeout waiting for daemon_hello")
		}
	}
}

// WaitForPing blocks until a heartbeat_ping envelope is received or timeout.
func (h *FakeHub) WaitForPing(timeout time.Duration) (*protocol.HeartbeatPingPayload, error) {
	deadline := time.After(timeout)
	for {
		select {
		case env := <-h.received:
			if env.Type == protocol.TypeHeartbeatPing {
				var ping protocol.HeartbeatPingPayload
				if err := json.Unmarshal(env.Payload, &ping); err != nil {
					return nil, err
				}
				return &ping, nil
			}
		case <-deadline:
			return nil, errors.New("timeout waiting for heartbeat_ping")
		}
	}
}

// WaitForResponse blocks until a query_response envelope is received or timeout.
func (h *FakeHub) WaitForResponse(timeout time.Duration) (*protocol.QueryResponsePayload, error) {
	deadline := time.After(timeout)
	for {
		select {
		case env := <-h.received:
			if env.Type == protocol.TypeQueryResponse {
				var resp protocol.QueryResponsePayload
				if err := json.Unmarshal(env.Payload, &resp); err != nil {
					return nil, err
				}
				return &resp, nil
			}
		case <-deadline:
			return nil, errors.New("timeout waiting for query_response")
		}
	}
}

func (h *FakeHub) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.headers = r.Header.Clone()
	h.mu.Unlock()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	conn.SetReadLimit(2 * 1024 * 1024)

	h.mu.Lock()
	h.activeConn = conn
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		if h.activeConn == conn {
			h.activeConn = nil
		}
		h.mu.Unlock()
	}()

	ctx := r.Context()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}

		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}

		h.received <- env

		h.mu.Lock()
		autoAck := h.autoAck
		ackInterval := h.ackInterval
		sessionID := h.sessionID
		autoPong := h.autoPong
		h.mu.Unlock()

		switch env.Type {
		case protocol.TypeDaemonHello:
			var hello protocol.DaemonHelloPayload
			_ = json.Unmarshal(env.Payload, &hello)
			h.mu.Lock()
			h.helloPayload = &hello
			h.mu.Unlock()

			if autoAck {
				ack := protocol.HubAckPayload{
					Authenticated:        true,
					SessionID:            sessionID,
					HeartbeatIntervalSec: ackInterval,
					PendingQueriesCount:  0,
				}
				_ = h.SendEnvelope(protocol.TypeHubAck, ack)
			}

		case protocol.TypeHeartbeatPing:
			if autoPong {
				pong := protocol.HeartbeatPongPayload{
					ServerTime: time.Now().UnixMilli(),
				}
				_ = h.SendEnvelope(protocol.TypeHeartbeatPong, pong)
			}
		}
	}
}
