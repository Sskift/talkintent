package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWSClientReconnectAndMessageDelivery(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	fake.SetClientConfig(ClientConfig{
		ReconnectCount:    -1,
		ReconnectInterval: 1,
		ReconnectNonce:    1,
		PingInterval:      10,
	})

	receivedMsgs := make(chan string, 10)
	cfg := WSClientConfig{
		MemberID:  "mem_test_reconnect",
		AppID:     "cli_reconnect_test",
		AppSecret: "sec_reconnect_test",
		BaseURL:   fake.URL(),
		EventHandler: func(ctx context.Context, payload []byte) error {
			receivedMsgs <- string(payload)
			return nil
		},
	}

	cli := NewWSClient(cfg)
	cli.Start()
	defer cli.Stop()

	// 1. Wait for initial connection
	if !fake.WaitForWSConnection(5 * time.Second) {
		t.Fatalf("timed out waiting for initial WebSocket connection")
	}

	// Verify connected state
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := cli.Status()
		if st == "connected" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	st, errStr := cli.Status()
	if st != "connected" {
		t.Fatalf("expected status 'connected', got %q (%s)", st, errStr)
	}

	// 2. Deliver an initial message event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	respFrame, err := fake.PushMessageEvent(ctx, "evt_1", "msg_1", "chat_1", "ou_1", "hello before disconnect")
	if err != nil {
		t.Fatalf("failed to push initial message event: %v", err)
	}
	if respFrame == nil {
		t.Fatalf("expected response frame from client")
	}

	select {
	case msg := <-receivedMsgs:
		if len(msg) == 0 {
			t.Fatalf("received empty message payload")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for message payload before disconnect")
	}

	// 3. Forcibly disconnect the active connection from server side
	fake.DisconnectWS()

	// Wait for client to observe disconnection
	disconnectDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(disconnectDeadline) {
		st, _ := cli.Status()
		if st != "connected" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 4. Verify client reconnects to fake server
	if !fake.WaitForWSConnection(5 * time.Second) {
		t.Fatalf("timed out waiting for client to re-establish WebSocket connection")
	}

	reconnected := false
	reconnectDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(reconnectDeadline) {
		st, _ := cli.Status()
		if st == "connected" {
			reconnected = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !reconnected {
		st, errStr := cli.Status()
		t.Fatalf("client failed to report connected status after reconnect, current status: %q (%s)", st, errStr)
	}

	// 5. Deliver another message after reconnect to assert normal operation
	respFrame2, err := fake.PushMessageEvent(ctx, "evt_2", "msg_2", "chat_1", "ou_1", "hello after reconnect")
	if err != nil {
		t.Fatalf("failed to push message event after reconnect: %v", err)
	}
	if respFrame2 == nil {
		t.Fatalf("expected response frame after reconnect")
	}

	select {
	case msg := <-receivedMsgs:
		if len(msg) == 0 {
			t.Fatalf("received empty message payload after reconnect")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for message payload after reconnect")
	}
}

func TestWSClientSingleEventResponse(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	handlerCalled := make(chan []byte, 10)
	cfg := WSClientConfig{
		MemberID:  "mem_single",
		AppID:     "cli_single",
		AppSecret: "sec_single",
		BaseURL:   fake.URL(),
		EventHandler: func(ctx context.Context, payload []byte) error {
			handlerCalled <- payload
			return nil
		},
	}

	cli := NewWSClient(cfg)
	cli.Start()
	defer cli.Stop()

	if !fake.WaitForWSConnection(5 * time.Second) {
		t.Fatalf("timed out waiting for WebSocket connection")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	respFrame, err := fake.PushMessageEvent(ctx, "evt_single_1", "msg_single_1", "chat_s1", "ou_s1", "ping single")
	if err != nil {
		t.Fatalf("failed to push single message event: %v", err)
	}
	if respFrame == nil {
		t.Fatalf("expected non-nil response frame")
	}

	// (a) exactly 1 handler invocation with full, validated payload
	select {
	case p := <-handlerCalled:
		var env EventEnvelope
		if err := json.Unmarshal(p, &env); err != nil {
			t.Fatalf("failed to unmarshal handler payload: %v", err)
		}
		if env.Header == nil || env.Header.EventID != "evt_single_1" {
			t.Errorf("expected EventID 'evt_single_1', got %v", env.Header)
		}
		if env.Event == nil || env.Event.Message == nil || env.Event.Message.MessageID != "msg_single_1" {
			t.Errorf("expected MessageID 'msg_single_1', got %v", env.Event)
		}
		var msgContent map[string]string
		if err := json.Unmarshal([]byte(env.Event.Message.Content), &msgContent); err != nil {
			t.Fatalf("failed to parse message content: %v", err)
		}
		if msgContent["text"] != "ping single" {
			t.Errorf("expected text 'ping single', got %q", msgContent["text"])
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for handler invocation")
	}

	// Ensure no duplicate or extraneous handler invocations occurred
	select {
	case extra := <-handlerCalled:
		t.Fatalf("unexpected extra handler invocation: %s", string(extra))
	default:
	}

	// Response frame assertions: SeqID/LogID/Service/Method echo request, has biz_rt, payload code 200
	reqFrame := fake.LastPushedFrame()
	if reqFrame == nil {
		t.Fatalf("expected recorded pushed request frame")
	}
	if respFrame.SeqID == 0 || respFrame.SeqID != reqFrame.SeqID {
		t.Errorf("expected echoed SeqID %d, got %d", reqFrame.SeqID, respFrame.SeqID)
	}
	if respFrame.LogID == 0 || respFrame.LogID != reqFrame.LogID {
		t.Errorf("expected echoed LogID %d, got %d", reqFrame.LogID, respFrame.LogID)
	}
	if respFrame.Method != 1 {
		t.Errorf("expected response Method 1 (Data), got %d", respFrame.Method)
	}
	if respFrame.Service != 12345 {
		t.Errorf("expected response Service 12345, got %d", respFrame.Service)
	}
	if respFrame.HeaderValue("type") != "event" {
		t.Errorf("expected echoed type 'event', got %q", respFrame.HeaderValue("type"))
	}
	if respFrame.HeaderValue("message_id") != "msg_single_1" {
		t.Errorf("expected echoed message_id 'msg_single_1', got %q", respFrame.HeaderValue("message_id"))
	}
	if respFrame.HeaderValue("biz_rt") == "" {
		t.Errorf("expected response frame to contain biz_rt header")
	}

	var payloadMap map[string]any
	if err := json.Unmarshal(respFrame.Payload, &payloadMap); err != nil {
		t.Fatalf("failed to unmarshal response frame payload: %v", err)
	}
	if code, ok := payloadMap["code"].(float64); !ok || int(code) != 200 {
		t.Errorf("expected response payload code 200, got %v", payloadMap["code"])
	}

	recorded := fake.RecordedResponseFrames()
	if len(recorded) != 1 {
		t.Errorf("expected exactly 1 recorded response frame, got %d", len(recorded))
	}
}

func TestWSClientFragmentedShuffledEvent(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	handlerCalled := make(chan []byte, 2)
	cfg := WSClientConfig{
		MemberID:  "mem_frag",
		AppID:     "cli_frag",
		AppSecret: "sec_frag",
		BaseURL:   fake.URL(),
		EventHandler: func(ctx context.Context, payload []byte) error {
			handlerCalled <- payload
			return nil
		},
	}

	cli := NewWSClient(cfg)
	cli.Start()
	defer cli.Stop()

	if !fake.WaitForWSConnection(5 * time.Second) {
		t.Fatalf("timed out waiting for WebSocket connection")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Split into 3 fragments delivered in shuffled order: seq 2, then seq 0, then seq 1
	respFrame, err := fake.PushFragmentedMessageEvent(
		ctx,
		"evt_frag_1",
		"msg_frag_1",
		"chat_f1",
		"ou_f1",
		"hello from shuffled fragments!",
		3,
		[]int{2, 0, 1},
	)
	if err != nil {
		t.Fatalf("failed to push fragmented message: %v", err)
	}
	if respFrame == nil {
		t.Fatalf("expected response frame from final fragment")
	}

	// (b) exactly 1 handler invocation with reassembled payload
	select {
	case p := <-handlerCalled:
		var env EventEnvelope
		if err := json.Unmarshal(p, &env); err != nil {
			t.Fatalf("failed to parse reassembled event: %v", err)
		}
		if env.Event == nil || env.Event.Message == nil {
			t.Fatalf("incomplete event body in reassembled payload: %s", string(p))
		}
		text, err := ParseTextContent(env.Event.Message.Content)
		if err != nil {
			t.Fatalf("failed to parse text content: %v", err)
		}
		if text != "hello from shuffled fragments!" {
			t.Errorf("expected 'hello from shuffled fragments!', got %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for fragmented handler invocation")
	}

	// Verify no second handler call was triggered
	select {
	case extra := <-handlerCalled:
		t.Fatalf("unexpected extra handler invocation: %s", string(extra))
	default:
	}

	// Check response echoed final fragment headers + biz_rt and code 200
	if respFrame.HeaderValue("biz_rt") == "" {
		t.Errorf("expected biz_rt in response frame")
	}
	var payloadMap map[string]any
	if err := json.Unmarshal(respFrame.Payload, &payloadMap); err != nil {
		t.Fatalf("failed to parse response payload: %v", err)
	}
	if code, ok := payloadMap["code"].(float64); !ok || int(code) != 200 {
		t.Errorf("expected code 200, got %v", payloadMap["code"])
	}

	// Exactly 1 response frame recorded by fake server
	recorded := fake.RecordedResponseFrames()
	if len(recorded) != 1 {
		t.Errorf("expected exactly 1 recorded response frame across 3 fragments, got %d", len(recorded))
	}
}

func TestWSClientHandlerErrorResponseCode500(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	cfg := WSClientConfig{
		MemberID:  "mem_err",
		AppID:     "cli_err",
		AppSecret: "sec_err",
		BaseURL:   fake.URL(),
		EventHandler: func(ctx context.Context, payload []byte) error {
			return errors.New("simulated handler error")
		},
	}

	cli := NewWSClient(cfg)
	cli.Start()
	defer cli.Stop()

	if !fake.WaitForWSConnection(5 * time.Second) {
		t.Fatalf("timed out waiting for WebSocket connection")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	respFrame, err := fake.PushMessageEvent(ctx, "evt_err_1", "msg_err_1", "chat_e1", "ou_e1", "will error")
	if err != nil {
		t.Fatalf("failed to push message event: %v", err)
	}
	if respFrame == nil {
		t.Fatalf("expected response frame")
	}

	var payloadMap map[string]any
	if err := json.Unmarshal(respFrame.Payload, &payloadMap); err != nil {
		t.Fatalf("failed to parse response payload: %v", err)
	}
	// (c) handler returning error -> response payload code 500
	if code, ok := payloadMap["code"].(float64); !ok || int(code) != 500 {
		t.Errorf("expected response code 500 on handler error, got %v", payloadMap["code"])
	}
}

func TestWSClientNonEventDataFrameIgnored(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	handlerCalled := make(chan []byte, 10)
	cfg := WSClientConfig{
		MemberID:  "mem_nonevent",
		AppID:     "cli_nonevent",
		AppSecret: "sec_nonevent",
		BaseURL:   fake.URL(),
		EventHandler: func(ctx context.Context, payload []byte) error {
			handlerCalled <- payload
			return nil
		},
	}

	cli := NewWSClient(cfg)
	cli.Start()
	defer cli.Stop()

	if !fake.WaitForWSConnection(5 * time.Second) {
		t.Fatalf("timed out waiting for WebSocket connection")
	}

	// Construct a data frame with type != "event" (e.g. type: "card")
	nonEventFrame := Frame{
		SeqID:   100,
		LogID:   101,
		Service: 12345,
		Method:  1, // Data
		Headers: []Header{
			{Key: "type", Value: "card"},
			{Key: "message_id", Value: "card_msg_1"},
		},
		Payload: []byte(`{"action":"card_button_click"}`),
	}
	wire, err := nonEventFrame.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal non-event frame: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fake.wsMu.Lock()
	conn := fake.activeWS
	fake.wsMu.Unlock()
	if conn == nil {
		t.Fatalf("expected active WS connection")
	}
	if err := conn.Write(ctx, 2 /* Binary */, wire); err != nil {
		t.Fatalf("failed to write non-event frame: %v", err)
	}

	// (d) Follow with a valid event frame as a stream barrier to prove the client read loop
	// has already processed past the preceding non-event frame.
	barrierResp, err := fake.PushMessageEvent(ctx, "evt_barrier", "msg_barrier", "chat_b", "ou_b", "barrier")
	if err != nil {
		t.Fatalf("failed to push barrier message event: %v", err)
	}
	if barrierResp == nil {
		t.Fatalf("expected non-nil barrier response frame")
	}

	select {
	case p := <-handlerCalled:
		var env EventEnvelope
		if err := json.Unmarshal(p, &env); err != nil {
			t.Fatalf("failed to unmarshal handler payload: %v", err)
		}
		if env.Header == nil || env.Header.EventID != "evt_barrier" {
			t.Fatalf("expected handler invoked only for barrier event 'evt_barrier', got: %v", env.Header)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for barrier event handler invocation")
	}

	// Verify no duplicate or extra handler invocations occurred (such as from the non-event frame)
	select {
	case extra := <-handlerCalled:
		t.Fatalf("expected handler NOT to be called for type != 'event', got payload: %s", string(extra))
	default:
	}
}

func TestWSClientPongDynamicPingInterval(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	// Initial server config has 120s ping interval.
	// Disable auto-pong so server does not write an asynchronous 120s pong frame
	// racing with the test's artificial 1s dynamic pong frame.
	fake.SetAutoPong(false)
	fake.SetClientConfig(ClientConfig{
		ReconnectCount:    -1,
		ReconnectInterval: 120,
		ReconnectNonce:    30,
		PingInterval:      120,
	})

	pingCh := make(chan struct{}, 10)
	fake.SetPingNotifyChannel(pingCh)

	cfg := WSClientConfig{
		MemberID:  "mem_pong_cadence",
		AppID:     "cli_pong_cadence",
		AppSecret: "sec_pong_cadence",
		BaseURL:   fake.URL(),
	}

	cli := NewWSClient(cfg)
	cli.Start()
	defer cli.Stop()

	if !fake.WaitForWSConnection(5 * time.Second) {
		t.Fatalf("timed out waiting for WebSocket connection")
	}

	// Wait for initial startup ping
	select {
	case <-pingCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for initial ping")
	}

	// Update server config to 1s PingInterval. Next pong will deliver this ClientConfig.
	fake.SetClientConfig(ClientConfig{
		ReconnectCount:    -1,
		ReconnectInterval: 120,
		ReconnectNonce:    30,
		PingInterval:      1,
	})

	// Send an artificial pong frame to the client carrying the new config
	fake.wsMu.Lock()
	conn := fake.activeWS
	fake.wsMu.Unlock()
	if conn == nil {
		t.Fatalf("expected active WS connection")
	}

	pongFrame := Frame{
		SeqID:   999,
		LogID:   999,
		Service: 12345,
		Method:  0, // Control
		Headers: []Header{
			{Key: "type", Value: "pong"},
		},
	}
	newConfBytes, _ := json.Marshal(ClientConfig{
		ReconnectCount:    -1,
		ReconnectInterval: 120,
		ReconnectNonce:    30,
		PingInterval:      1, // 1 second
	})
	pongFrame.Payload = newConfBytes
	wire, _ := pongFrame.Marshal()

	writeCtx, writeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer writeCancel()
	if err := conn.Write(writeCtx, 2 /* Binary */, wire); err != nil {
		t.Fatalf("failed to write pong frame: %v", err)
	}

	// (e) pong carrying new ClientConfig -> ping interval changes and next ping arrives within ~2s
	select {
	case <-pingCh:
		// Received ping at the new 1s cadence!
	case <-time.After(2500 * time.Millisecond):
		t.Fatalf("timed out waiting for ping at new dynamic interval (< 3s)")
	}
}

func TestWSClientAuthFailureFatalStop(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	appSecret := "super_secret_token_value_9999"
	// (f) Simulate auth failure code 514 containing raw app_secret to verify redaction
	fake.SimulateEndpointError(514, "auth failed: invalid app_id or app_secret: "+appSecret)

	cfg := WSClientConfig{
		MemberID:  "mem_auth_fail",
		AppID:     "cli_wrong_app",
		AppSecret: appSecret,
		BaseURL:   fake.URL(),
	}

	cli := NewWSClient(cfg)
	cli.Start()

	// Wait for client to discover auth failure and enter state "error"
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := cli.Status()
		if st == "error" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	st, lastErr := cli.Status()
	if st != "error" {
		t.Fatalf("expected state 'error', got %q (%s)", st, lastErr)
	}
	if !strings.Contains(lastErr, "514") {
		t.Errorf("expected last_error to mention code 514, got %q", lastErr)
	}
	if strings.Contains(lastErr, appSecret) {
		t.Errorf("last_error must NEVER leak raw app_secret (contains secret of length %d)", len(appSecret))
	}
	if !strings.Contains(lastErr, "[REDACTED]") {
		t.Errorf("expected last_error to contain [REDACTED], got %q", lastErr)
	}

	// Stop() must return promptly without hanging
	stopDone := make(chan struct{})
	go func() {
		cli.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
		// Stop completed promptly
	case <-time.After(2 * time.Second):
		t.Fatalf("WSClient.Stop() hung or did not return promptly")
	}

	// Assert status remains "error"
	finalState, _ := cli.Status()
	if finalState != "error" {
		t.Errorf("expected state to remain 'error' after Stop(), got %q", finalState)
	}
}

func TestWSClientStopDuringConnecting(t *testing.T) {
	// A server that delays endpoint discovery so the client is mid-fetch when Stop() is called.
	endpointBlocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-endpointBlocked
		http.Error(w, "server closing", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	defer close(endpointBlocked)

	cfg := WSClientConfig{
		MemberID:  "mem_stop_connecting",
		AppID:     "cli_stop_connecting",
		AppSecret: "sec_stop_connecting",
		BaseURL:   server.URL,
	}

	cli := NewWSClient(cfg)
	cli.Start()

	// Wait for client to enter "connecting" state
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := cli.Status()
		if st == "connecting" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Invoke Stop() while endpoint discovery is in flight
	cli.Stop()

	st, lastErr := cli.Status()
	if st != "stopped" {
		t.Fatalf("expected state 'stopped' after Stop(), got %q (lastErr: %s)", st, lastErr)
	}
}

func TestWSClientEventHandlerContextCanceledOnStop(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	handlerStarted := make(chan struct{})
	handlerCtxDone := make(chan struct{})

	cfg := WSClientConfig{
		MemberID:  "mem_handler_cancel",
		AppID:     "cli_handler_cancel",
		AppSecret: "sec_handler_cancel",
		BaseURL:   fake.URL(),
		EventHandler: func(ctx context.Context, payload []byte) error {
			close(handlerStarted)
			<-ctx.Done()
			close(handlerCtxDone)
			return ctx.Err()
		},
	}

	cli := NewWSClient(cfg)
	cli.Start()

	if !fake.WaitForWSConnection(5 * time.Second) {
		t.Fatalf("timed out waiting for WebSocket connection")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Push message event in background because PushMessageEvent waits for response frame
	go func() {
		_, _ = fake.PushMessageEvent(ctx, "evt_cancel_1", "msg_cancel_1", "chat_c1", "ou_c1", "ping cancel")
	}()

	// Wait for handler to start
	select {
	case <-handlerStarted:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for handler to start")
	}

	// Stop client; this must cancel the handler's context
	stopDone := make(chan struct{})
	go func() {
		cli.Stop()
		close(stopDone)
	}()

	select {
	case <-handlerCtxDone:
		// Success: handler context was cancelled by Stop()!
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for handler context cancellation on Stop()")
	}

	select {
	case <-stopDone:
		// Stop completed cleanly!
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for Stop() to return")
	}
}
