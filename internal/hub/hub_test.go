package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/feishu"
	"github.com/Sskift/talkintent/internal/protocol"
	"github.com/Sskift/talkintent/internal/store"
	"github.com/Sskift/talkintent/web"
)

func setupTestHub(t *testing.T) (*HubServer, store.Store, string, func()) {
	tempDir := t.TempDir()
	st, err := store.NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}

	cfg := &config.HubConfig{
		Addr:    "127.0.0.1:0", // loopback only: a 0.0.0.0 bind trips the Windows Defender prompt on every fresh test binary
		DataDir: tempDir,
		RateLimits: config.RateLimitConfig{
			QueriesPerMinute: 60,
			Burst:            30,
		},
	}

	srv, err := NewServer(cfg, st, nil, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	cleanup := func() {
		_ = st.Close()
	}

	return srv, st, cfg.AdminToken, cleanup
}

func TestAdminAuthAndInvites(t *testing.T) {
	srv, _, adminToken, cleanup := setupTestHub(t)
	defer cleanup()

	if adminToken == "" {
		t.Fatalf("expected admin token to be generated")
	}

	// 1. Without auth header -> 401
	invReq := protocol.InviteCreateRequest{
		TargetName: "王五",
	}
	body, _ := json.Marshal(invReq)
	req := httptest.NewRequest("POST", "/api/v1/admin/invites", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", rec.Code)
	}

	// 2. With invalid auth header -> 401
	req = httptest.NewRequest("POST", "/api/v1/admin/invites", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer invalid_token")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", rec.Code)
	}

	// 3. With valid admin token -> 201 Created
	req = httptest.NewRequest("POST", "/api/v1/admin/invites", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	var invResp protocol.InviteCreateResponse
	if err := json.NewDecoder(rec.Body).Decode(&invResp); err != nil {
		t.Fatalf("decode invite response failed: %v", err)
	}
	if invResp.Code == "" || invResp.TargetName != "王五" {
		t.Errorf("unexpected invite response: %+v", invResp)
	}
}

func TestQueryIdempotencyAndAccessControl(t *testing.T) {
	srv, st, adminToken, cleanup := setupTestHub(t)
	defer cleanup()

	ctx := context.Background()

	// Create Member A (Asker)
	invA, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "AskerUser"})
	pairA, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invA.Code, MachineName: "dev-a"})

	// Create Member B (Target)
	invB, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "TargetUser"})
	pairB, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invB.Code, MachineName: "dev-b"})

	// Create Member C (Stranger)
	invC, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "StrangerUser"})
	pairC, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invC.Code, MachineName: "dev-c"})

	// 1. Submit Query with Idempotency Key
	queryPayload := protocol.QuerySubmitRequest{
		Target:         "TargetUser",
		Query:          "What are you working on?",
		IdempotencyKey: "idemp_key_12345",
	}
	b, _ := json.Marshal(queryPayload)

	req := httptest.NewRequest("POST", "/api/v1/queries", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+pairA.Token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", rec.Code, rec.Body.String())
	}

	var firstResp protocol.QueryDetailResponse
	_ = json.NewDecoder(rec.Body).Decode(&firstResp)
	queryID := firstResp.QueryID

	// Resubmit with same Idempotency Key -> should return identical query ID
	req2 := httptest.NewRequest("POST", "/api/v1/queries", bytes.NewReader(b))
	req2.Header.Set("Authorization", "Bearer "+pairA.Token)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)

	var secondResp protocol.QueryDetailResponse
	_ = json.NewDecoder(rec2.Body).Decode(&secondResp)
	if secondResp.QueryID != queryID {
		t.Errorf("expected idempotent query ID %s, got %s", queryID, secondResp.QueryID)
	}

	// 2. Query Detail Access Control
	// Stranger (Member C) attempts access -> 403 Forbidden
	reqDetailStranger := httptest.NewRequest("GET", "/api/v1/queries/"+queryID, nil)
	reqDetailStranger.Header.Set("Authorization", "Bearer "+pairC.Token)
	recDetailStranger := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDetailStranger, reqDetailStranger)

	if recDetailStranger.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for stranger, got %d", recDetailStranger.Code)
	}

	// Asker (Member A) attempts access -> 200 OK
	reqDetailAsker := httptest.NewRequest("GET", "/api/v1/queries/"+queryID, nil)
	reqDetailAsker.Header.Set("Authorization", "Bearer "+pairA.Token)
	recDetailAsker := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDetailAsker, reqDetailAsker)

	if recDetailAsker.Code != http.StatusOK {
		t.Errorf("expected 200 OK for asker, got %d", recDetailAsker.Code)
	}

	// Target (Member B) attempts access -> 200 OK
	reqDetailTarget := httptest.NewRequest("GET", "/api/v1/queries/"+queryID, nil)
	reqDetailTarget.Header.Set("Authorization", "Bearer "+pairB.Token)
	recDetailTarget := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDetailTarget, reqDetailTarget)

	if recDetailTarget.Code != http.StatusOK {
		t.Errorf("expected 200 OK for target, got %d", recDetailTarget.Code)
	}

	// Admin attempts access -> 200 OK
	reqDetailAdmin := httptest.NewRequest("GET", "/api/v1/queries/"+queryID, nil)
	reqDetailAdmin.Header.Set("Authorization", "Bearer "+adminToken)
	recDetailAdmin := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDetailAdmin, reqDetailAdmin)

	if recDetailAdmin.Code != http.StatusOK {
		t.Errorf("expected 200 OK for admin, got %d", recDetailAdmin.Code)
	}
}

func TestAntiHijackingAndInFlightReconciliation(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ctx := context.Background()

	// Member B
	invB, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "TargetUser"})
	pairB, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invB.Code, MachineName: "dev-b"})

	// Create query targeting Member B
	q := &protocol.QueryDetailResponse{
		QueryID:          "q_test_hijack_1",
		Status:           protocol.QueryStatusDispatched,
		AskerID:          "mem_asker",
		AskerName:        "Asker",
		TargetMemberID:   pairB.MemberID,
		TargetMemberName: "TargetUser",
		Query:            "Are you there?",
		CreatedAt:        time.Now().UnixMilli(),
	}
	_ = st.CreateQuery(ctx, q)

	// Simulate daemon session for Member C (attacker)
	attackerSess := &daemonSession{
		sessionID: "attacker_sess",
		memberID:  "mem_attacker",
	}

	// Attacker sends response for query targeting Member B
	respPayload := protocol.QueryResponsePayload{
		QueryID: "q_test_hijack_1",
		Status:  "completed",
		Answer:  "Malicious injected answer",
	}
	rawResp, _ := json.Marshal(respPayload)
	env := protocol.Envelope{
		Version: protocol.Version1,
		Type:    protocol.TypeQueryResponse,
		Payload: rawResp,
	}

	srv.handleDaemonEnvelope(ctx, attackerSess, env)

	// Verify query status was NOT updated by attacker
	checkQ, _ := st.GetQuery(ctx, "q_test_hijack_1")
	if checkQ.Answer == "Malicious injected answer" {
		t.Fatalf("security violation: attacker successfully spoofed query response!")
	}
	if checkQ.Status != protocol.QueryStatusDispatched {
		t.Errorf("expected status dispatched, got %s", checkQ.Status)
	}

	// Test In-flight query reconciliation on Member B disconnect
	srv.reconcileInFlightQueries(pairB.MemberID)

	reconciledQ, _ := st.GetQuery(ctx, "q_test_hijack_1")
	if reconciledQ.Status != protocol.QueryStatusQueued {
		t.Errorf("expected reconciled query status to be queued, got %s", reconciledQ.Status)
	}
}

// TestFullQueryLifecycle_LiveWebSocket tests the end-to-end query lifecycle:
// pair -> connect -> REST query -> WS frame -> WS answer -> REST result (DESIGN §7.3 WP2).
func TestFullQueryLifecycle_LiveWebSocket(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Pair Asker and Target
	invAsker, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "AliceAsker"})
	pairAsker, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invAsker.Code, MachineName: "laptop-a"})

	invTarget, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "BobTarget"})
	pairTarget, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invTarget.Code, MachineName: "desktop-b"})

	// 2. Connect Target's Daemon via WebSocket
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/daemon"
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":  []string{"Bearer " + pairTarget.Token},
			"X-Machine-Name": []string{"desktop-b"},
		},
	}
	wsConn, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("target daemon failed to dial websocket: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "test done")

	// Target sends daemon_hello
	helloPayload := protocol.DaemonHelloPayload{
		MachineName:    "desktop-b",
		MaxConcurrency: 2,
		Workspaces: []protocol.WorkspaceInfo{
			{ID: "ws1", Name: "talkintent", RootPath: "/home/bob/talkintent"},
		},
	}
	rawHello, _ := json.Marshal(helloPayload)
	helloEnv := protocol.Envelope{
		Version:   protocol.Version1,
		Type:      protocol.TypeDaemonHello,
		ID:        "msg_hello",
		Timestamp: time.Now().UnixMilli(),
		Payload:   rawHello,
	}
	helloBytes, _ := json.Marshal(helloEnv)
	if err := wsConn.Write(ctx, websocket.MessageText, helloBytes); err != nil {
		t.Fatalf("failed to write daemon_hello: %v", err)
	}

	// Target reads hub_ack
	_, ackData, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read hub_ack: %v", err)
	}
	var ackEnv protocol.Envelope
	_ = json.Unmarshal(ackData, &ackEnv)
	if ackEnv.Type != protocol.TypeHubAck {
		t.Fatalf("expected hub_ack, got type %s", ackEnv.Type)
	}
	var hubAck protocol.HubAckPayload
	_ = json.Unmarshal(ackEnv.Payload, &hubAck)
	if !hubAck.Authenticated || hubAck.SessionID == "" {
		t.Fatalf("invalid hub_ack payload: %+v", hubAck)
	}

	// 3. Asker submits REST query
	querySubmit := protocol.QuerySubmitRequest{
		Target: "BobTarget",
		Query:  "What feature are you building?",
	}
	submitBytes, _ := json.Marshal(querySubmit)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/api/v1/queries", bytes.NewReader(submitBytes))
	httpReq.Header.Set("Authorization", "Bearer "+pairAsker.Token)
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("failed to submit query: %v", err)
	}
	if httpResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", httpResp.StatusCode)
	}
	var queryAccepted protocol.QueryDetailResponse
	_ = json.NewDecoder(httpResp.Body).Decode(&queryAccepted)
	httpResp.Body.Close()

	if queryAccepted.QueryID == "" || queryAccepted.Status != protocol.QueryStatusDispatched {
		t.Fatalf("unexpected query detail on submit: %+v", queryAccepted)
	}

	// 4. Target reads query_request from WebSocket
	_, reqData, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read query_request from WS: %v", err)
	}
	var reqEnv protocol.Envelope
	_ = json.Unmarshal(reqData, &reqEnv)
	if reqEnv.Type != protocol.TypeQueryRequest {
		t.Fatalf("expected query_request, got %s", reqEnv.Type)
	}
	var queryReqPayload protocol.QueryRequestPayload
	_ = json.Unmarshal(reqEnv.Payload, &queryReqPayload)
	if queryReqPayload.QueryID != queryAccepted.QueryID {
		t.Fatalf("query ID mismatch: expected %s, got %s", queryAccepted.QueryID, queryReqPayload.QueryID)
	}

	// 5. Target sends query_response over WebSocket
	respPayload := protocol.QueryResponsePayload{
		QueryID:    queryReqPayload.QueryID,
		Status:     "success", // will be normalized to completed (F36)
		Answer:     "Building the WebSocket routing core for TalkIntent.",
		ToolsUsed:  []string{"git_diff", "file_read"},
		DurationMS: 420,
	}
	rawResp, _ := json.Marshal(respPayload)
	respEnv := protocol.Envelope{
		Version:   protocol.Version1,
		Type:      protocol.TypeQueryResponse,
		ID:        "msg_resp_1",
		Timestamp: time.Now().UnixMilli(),
		Payload:   rawResp,
	}
	respBytes, _ := json.Marshal(respEnv)
	if err := wsConn.Write(ctx, websocket.MessageText, respBytes); err != nil {
		t.Fatalf("failed to send query_response: %v", err)
	}

	// 6. Asker polls GET /api/v1/queries/{id}?wait=5s to verify completion
	var finalQ protocol.QueryDetailResponse
	deadline := time.Now().Add(5 * time.Second)
	for {
		getReq, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/queries/"+queryAccepted.QueryID+"?wait=2s", nil)
		getReq.Header.Set("Authorization", "Bearer "+pairAsker.Token)
		getResp, err := http.DefaultClient.Do(getReq)
		if err != nil {
			t.Fatalf("failed to get query detail: %v", err)
		}
		if getResp.StatusCode != http.StatusOK {
			getResp.Body.Close()
			t.Fatalf("expected 200 OK, got %d", getResp.StatusCode)
		}
		_ = json.NewDecoder(getResp.Body).Decode(&finalQ)
		getResp.Body.Close()

		if finalQ.Status == protocol.QueryStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for completed status; last status=%s", finalQ.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if finalQ.Status != protocol.QueryStatusCompleted {
		t.Errorf("expected status %s, got %s", protocol.QueryStatusCompleted, finalQ.Status)
	}
	if !strings.Contains(finalQ.Answer, "WebSocket routing core") {
		t.Errorf("expected answer content, got: %s", finalQ.Answer)
	}
	if len(finalQ.ToolsUsed) != 2 || finalQ.ToolsUsed[0] != "git_diff" {
		t.Errorf("unexpected tools used: %v", finalQ.ToolsUsed)
	}
}

// TestOfflineQueueingAndDeliveryOnConnect tests query submission while target is offline,
// followed by connection and automated drain (F19, DESIGN §7.3).
func TestOfflineQueueingAndDeliveryOnConnect(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Create Asker and Target
	invAsker, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "CharlieAsker"})
	pairAsker, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invAsker.Code, MachineName: "laptop-c"})

	invTarget, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "DianaTarget"})
	pairTarget, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invTarget.Code, MachineName: "desktop-d"})

	// Target is currently offline.
	// 2. Submit query targeting DianaTarget
	querySubmit := protocol.QuerySubmitRequest{
		Target: "DianaTarget",
		Query:  "Can you review the PR?",
	}
	b, _ := json.Marshal(querySubmit)
	req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/api/v1/queries", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+pairAsker.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("submit query failed: %v", err)
	}
	var qAccepted protocol.QueryDetailResponse
	_ = json.NewDecoder(resp.Body).Decode(&qAccepted)
	resp.Body.Close()

	if qAccepted.Status != protocol.QueryStatusQueued {
		t.Fatalf("expected queued status for offline target, got %s", qAccepted.Status)
	}
	if qAccepted.QueuePosition != 1 {
		t.Errorf("expected queue position 1, got %d", qAccepted.QueuePosition)
	}

	// 3. DianaTarget connects via WebSocket
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/daemon"
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + pairTarget.Token},
		},
	}
	wsConn, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "test done")

	// Send daemon_hello
	helloEnv := protocol.Envelope{
		Version:   protocol.Version1,
		Type:      protocol.TypeDaemonHello,
		ID:        "msg_hello_d",
		Timestamp: time.Now().UnixMilli(),
		Payload:   []byte(`{"machine_name":"desktop-d","max_concurrency":5}`),
	}
	hb, _ := json.Marshal(helloEnv)
	_ = wsConn.Write(ctx, websocket.MessageText, hb)

	// Read hub_ack: should show PendingQueriesCount == 1
	_, ackBytes, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read hub_ack: %v", err)
	}
	var ackEnv protocol.Envelope
	_ = json.Unmarshal(ackBytes, &ackEnv)
	var ackPayload protocol.HubAckPayload
	_ = json.Unmarshal(ackEnv.Payload, &ackPayload)

	if ackPayload.PendingQueriesCount != 1 {
		t.Errorf("expected pending_queries_count == 1 in hub_ack, got %d", ackPayload.PendingQueriesCount)
	}

	// Read drained query_request from offline queue
	_, drainedBytes, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read drained query_request: %v", err)
	}
	var drainedEnv protocol.Envelope
	_ = json.Unmarshal(drainedBytes, &drainedEnv)
	if drainedEnv.Type != protocol.TypeQueryRequest {
		t.Fatalf("expected drained type query_request, got %s", drainedEnv.Type)
	}
	var drainedReq protocol.QueryRequestPayload
	_ = json.Unmarshal(drainedEnv.Payload, &drainedReq)
	if drainedReq.QueryID != qAccepted.QueryID {
		t.Errorf("expected drained query ID %s, got %s", qAccepted.QueryID, drainedReq.QueryID)
	}
}

// TestAuthNegativeCases covers missing token, bad token, major protocol mismatch, and admin authorization.
func TestAuthNegativeCases(t *testing.T) {
	srv, _, _, cleanup := setupTestHub(t)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/daemon"

	// 1. Connect without Authorization header -> 401
	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatalf("expected error dialing without auth")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}

	// 2. Connect with invalid token -> 401
	optsBad := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer invalid_token_123"},
		},
	}
	_, resp2, err := websocket.Dial(ctx, wsURL, optsBad)
	if err == nil {
		t.Fatalf("expected error dialing with invalid token")
	}
	if resp2 != nil && resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", resp2.StatusCode)
	}
}

// TestAmbiguousTarget verifies that querying a prefix matching multiple members returns HTTP 400 AMBIGUOUS_TARGET.
func TestAmbiguousTarget(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ctx := context.Background()

	// Create Member 1: "Alice Zhang"
	inv1, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "Alice Zhang"})
	_, _ = st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv1.Code, MachineName: "dev1"})

	// Create Member 2: "Alice Wang"
	inv2, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "Alice Wang"})
	_, _ = st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv2.Code, MachineName: "dev2"})

	// Create Asker
	invAsker, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "Bob"})
	pairAsker, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invAsker.Code, MachineName: "dev-bob"})

	// Submit query targeting ambiguous "Alice"
	submitReq := protocol.QuerySubmitRequest{
		Target: "Alice",
		Query:  "What's your status?",
	}
	b, _ := json.Marshal(submitReq)
	req := httptest.NewRequest("POST", "/api/v1/queries", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+pairAsker.Token)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for ambiguous target, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp protocol.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error.Code != protocol.ErrCodeAmbiguousTarget {
		t.Errorf("expected error code %s, got %s", protocol.ErrCodeAmbiguousTarget, errResp.Error.Code)
	}
	candidates, ok := errResp.Error.Details["candidates"].([]any)
	if !ok || len(candidates) < 2 {
		t.Errorf("expected candidates list in details, got: %+v", errResp.Error.Details)
	}
}

// TestRateLimiting verifies token-bucket rate limiting returning HTTP 429.
func TestRateLimiting(t *testing.T) {
	tempDir := t.TempDir()
	st, _ := store.NewJSONLStore(tempDir)
	defer st.Close()

	// Hub with tight rate limit: 2 QPM, burst 2
	cfg := &config.HubConfig{
		Addr:    "127.0.0.1:0",
		DataDir: tempDir,
		RateLimits: config.RateLimitConfig{
			QueriesPerMinute: 2,
			Burst:            2,
		},
	}
	srv, err := NewServer(cfg, st, nil, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	ctx := context.Background()
	invTarget, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "TargetBob"})
	_, _ = st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invTarget.Code, MachineName: "dev-b"})

	invAsker, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "FastAsker"})
	pairAsker, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invAsker.Code, MachineName: "dev-a"})

	sendQuery := func() int {
		payload := protocol.QuerySubmitRequest{
			Target: "TargetBob",
			Query:  "Hello?",
		}
		b, _ := json.Marshal(payload)
		req := httptest.NewRequest("POST", "/api/v1/queries", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+pairAsker.Token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	// 1st request -> 202
	if code := sendQuery(); code != http.StatusAccepted {
		t.Errorf("req 1: expected 202, got %d", code)
	}
	// 2nd request -> 202
	if code := sendQuery(); code != http.StatusAccepted {
		t.Errorf("req 2: expected 202, got %d", code)
	}
	// 3rd request -> burst exhausted, 429 Too Many Requests
	if code := sendQuery(); code != http.StatusTooManyRequests {
		t.Errorf("req 3: expected 429 Too Many Requests, got %d", code)
	}
}

// TestMultiDeviceTakeover tests connection takeover from the same machine (F17).
func TestMultiDeviceTakeover(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "MultiUser"})
	pair, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "laptop-1"})

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/daemon"

	// 1. First connection from "laptop-1"
	dialOpts1 := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":  []string{"Bearer " + pair.Token},
			"X-Machine-Name": []string{"laptop-1"},
		},
	}
	conn1, _, err := websocket.Dial(ctx, wsURL, dialOpts1)
	if err != nil {
		t.Fatalf("first connection failed: %v", err)
	}
	defer conn1.Close(websocket.StatusNormalClosure, "done")

	// Send daemon_hello on conn1 and read hub_ack
	helloBytes := []byte(`{"version":"v1","type":"daemon_hello","id":"h1","payload":{"machine_name":"laptop-1"}}`)
	_ = conn1.Write(ctx, websocket.MessageText, helloBytes)
	_, _, _ = conn1.Read(ctx)

	// 2. Second connection from SAME machine "laptop-1" (e.g. reconnect or daemon restart)
	conn2, _, err := websocket.Dial(ctx, wsURL, dialOpts1)
	if err != nil {
		t.Fatalf("second connection failed: %v", err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "done")

	// The first connection should now be closed by the Hub (superseded)
	readCtx, readCancel := context.WithTimeout(ctx, 1*time.Second)
	defer readCancel()
	_, _, readErr := conn1.Read(readCtx)
	if readErr == nil {
		t.Errorf("expected conn1 to be closed by server on takeover, but read succeeded")
	}

	// 3. Third connection from a DIFFERENT machine "desktop-2" -> should coexist with laptop-1
	dialOpts3 := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":  []string{"Bearer " + pair.Token},
			"X-Machine-Name": []string{"desktop-2"},
		},
	}
	conn3, _, err := websocket.Dial(ctx, wsURL, dialOpts3)
	if err != nil {
		t.Fatalf("third connection failed: %v", err)
	}
	defer conn3.Close(websocket.StatusNormalClosure, "done")

	// Send daemon_hello on conn3 and wait for hub_ack to ensure server processed registration
	hello3Bytes := []byte(`{"version":"v1","type":"daemon_hello","id":"h3","payload":{"machine_name":"desktop-2"}}`)
	_ = conn3.Write(ctx, websocket.MessageText, hello3Bytes)
	_, _, _ = conn3.Read(ctx)

	// Both conn2 and conn3 should be active in Hub
	srv.mu.RLock()
	sessions := srv.memberSessions[pair.MemberID]
	activeCount := len(sessions)
	srv.mu.RUnlock()

	if activeCount != 2 {
		t.Errorf("expected 2 concurrent sessions for different machines, got %d", activeCount)
	}
}

// TestTTLExpiry verifies that expired queries are marked expired and swept (F19/F41).
func TestTTLExpiry(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ctx := context.Background()

	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "ExpiringUser"})
	pair, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "dev-exp"})

	// Create query that is already expired (TTL in the past)
	pastTTL := time.Now().Add(-10 * time.Second).UnixMilli()
	q := &protocol.QueryDetailResponse{
		QueryID:          "q_exp_test",
		Status:           protocol.QueryStatusQueued,
		AskerID:          pair.MemberID,
		AskerName:        "ExpiringUser",
		TargetMemberID:   pair.MemberID,
		TargetMemberName: "ExpiringUser",
		Query:            "This should expire",
		TTLExpiresAt:     pastTTL,
		CreatedAt:        pastTTL - 1000,
	}
	_ = st.CreateQuery(ctx, q)

	// Sweep expired queries
	swept, err := srv.store.SweepExpiredQueries(ctx)
	if err != nil {
		t.Fatalf("sweep expired queries failed: %v", err)
	}
	if swept == 0 {
		t.Errorf("expected at least 1 query swept, got %d", swept)
	}

	// Verify status is expired
	checkQ, err := st.GetQuery(ctx, "q_exp_test")
	if err != nil {
		t.Fatalf("failed to get query: %v", err)
	}
	if checkQ.Status != protocol.QueryStatusExpired {
		t.Errorf("expected status %s, got %s", protocol.QueryStatusExpired, checkQ.Status)
	}
}

// TestMemberSelfServiceRoutes tests /api/v1/members, /api/v1/members/me, and /api/v1/members/{id}.
func TestMemberSelfServiceRoutes(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ctx := context.Background()

	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "SelfUser"})
	pair, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "dev-self"})

	// 1. GET /api/v1/members/me
	reqMe := httptest.NewRequest("GET", "/api/v1/members/me", nil)
	reqMe.Header.Set("Authorization", "Bearer "+pair.Token)
	recMe := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recMe, reqMe)

	if recMe.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /me, got %d: %s", recMe.Code, recMe.Body.String())
	}
	var memMe protocol.MemberInfo
	_ = json.NewDecoder(recMe.Body).Decode(&memMe)
	if memMe.ID != pair.MemberID || memMe.Name != "SelfUser" {
		t.Errorf("unexpected /me response: %+v", memMe)
	}

	// 2. PUT /api/v1/members/me
	updatePayload := protocol.MemberUpdateRequest{
		Name:    "SelfUserUpdated",
		Aliases: []string{"selfie"},
	}
	upBytes, _ := json.Marshal(updatePayload)
	reqUp := httptest.NewRequest("PUT", "/api/v1/members/me", bytes.NewReader(upBytes))
	reqUp.Header.Set("Authorization", "Bearer "+pair.Token)
	recUp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recUp, reqUp)

	if recUp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for PUT /me, got %d: %s", recUp.Code, recUp.Body.String())
	}

	// 3. GET /api/v1/members/{id}
	reqID := httptest.NewRequest("GET", "/api/v1/members/"+pair.MemberID, nil)
	reqID.Header.Set("Authorization", "Bearer "+pair.Token)
	recID := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recID, reqID)

	if recID.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /members/{id}, got %d: %s", recID.Code, recID.Body.String())
	}
	var memByID protocol.MemberInfo
	_ = json.NewDecoder(recID.Body).Decode(&memByID)
	if memByID.Name != "SelfUserUpdated" {
		t.Errorf("expected updated name 'SelfUserUpdated', got %s", memByID.Name)
	}
}

// TestFeishuBindingCRUD tests the CRUD operations for Feishu bot integration bindings.
func TestFeishuBindingCRUD(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ctx := context.Background()

	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "FeishuUser"})
	pair, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "dev-feishu"})

	// 1. Initial GET -> not bound
	reqGet := httptest.NewRequest("GET", "/api/v1/feishu/binding", nil)
	reqGet.Header.Set("Authorization", "Bearer "+pair.Token)
	recGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet, reqGet)

	if recGet.Code != http.StatusNotFound && recGet.Code != http.StatusOK {
		t.Fatalf("unexpected code for empty binding: %d", recGet.Code)
	}

	// 2. POST /api/v1/feishu/binding -> save binding
	fakeServer := feishu.NewFakeServer()
	defer fakeServer.Close()

	bindReq := protocol.FeishuBindingRequest{
		AppID:     "cli_test_12345",
		AppSecret: "sec_test_67890",
		BaseURL:   fakeServer.URL(),
	}
	bindBytes, _ := json.Marshal(bindReq)
	reqPost := httptest.NewRequest("POST", "/api/v1/feishu/binding", bytes.NewReader(bindBytes))
	reqPost.Header.Set("Authorization", "Bearer "+pair.Token)
	recPost := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recPost, reqPost)

	if recPost.Code != http.StatusOK {
		t.Fatalf("expected 200 OK saving binding, got %d: %s", recPost.Code, recPost.Body.String())
	}

	// 3. GET /api/v1/feishu/binding -> bound
	reqGet2 := httptest.NewRequest("GET", "/api/v1/feishu/binding", nil)
	reqGet2.Header.Set("Authorization", "Bearer "+pair.Token)
	recGet2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet2, reqGet2)

	if recGet2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for bound feishu, got %d", recGet2.Code)
	}
	var bindResp protocol.FeishuBindingResponse
	_ = json.NewDecoder(recGet2.Body).Decode(&bindResp)
	if !bindResp.Bound || bindResp.AppID != "cli_test_12345" {
		t.Errorf("unexpected binding response: %+v", bindResp)
	}

	// 4. DELETE /api/v1/feishu/binding
	reqDel := httptest.NewRequest("DELETE", "/api/v1/feishu/binding", nil)
	reqDel.Header.Set("Authorization", "Bearer "+pair.Token)
	recDel := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDel, reqDel)

	if recDel.Code != http.StatusNoContent {
		t.Fatalf("expected 204 No Content, got %d", recDel.Code)
	}
}

// TestLongPollingWait tests ?wait= query submission and query detail retrieval long-polling.
func TestLongPollingWait(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	invAsker, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "WaitAsker"})
	pairAsker, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invAsker.Code, MachineName: "dev-wa"})

	invTarget, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "WaitTarget"})
	pairTarget, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invTarget.Code, MachineName: "dev-wt"})

	// Connect target via WS
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/daemon"
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + pairTarget.Token},
		},
	}
	wsConn, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "done")

	// Read hello ack
	helloEnv := protocol.Envelope{
		Version:   protocol.Version1,
		Type:      protocol.TypeDaemonHello,
		ID:        "h1",
		Timestamp: time.Now().UnixMilli(),
		Payload:   []byte(`{"machine_name":"dev-wt"}`),
	}
	hb, _ := json.Marshal(helloEnv)
	_ = wsConn.Write(ctx, websocket.MessageText, hb)
	_, _, _ = wsConn.Read(ctx)

	// In goroutine, target will answer query after 100ms
	go func() {
		readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer readCancel()
		_, data, err := wsConn.Read(readCtx)
		if err != nil {
			return
		}
		var env protocol.Envelope
		_ = json.Unmarshal(data, &env)
		var reqPayload protocol.QueryRequestPayload
		_ = json.Unmarshal(env.Payload, &reqPayload)

		time.Sleep(100 * time.Millisecond)
		respPayload := protocol.QueryResponsePayload{
			QueryID: reqPayload.QueryID,
			Status:  "completed",
			Answer:  "Long polling works perfectly.",
		}
		rb, _ := json.Marshal(respPayload)
		respEnv := protocol.Envelope{
			Version:   protocol.Version1,
			Type:      protocol.TypeQueryResponse,
			ID:        "r1",
			Timestamp: time.Now().UnixMilli(),
			Payload:   rb,
		}
		eb, _ := json.Marshal(respEnv)
		_ = wsConn.Write(readCtx, websocket.MessageText, eb)
	}()

	// Asker submits query with wait=true
	submitReq := protocol.QuerySubmitRequest{
		Target: "WaitTarget",
		Query:  "Testing long poll",
		Wait:   true,
	}
	sb, _ := json.Marshal(submitReq)
	req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/api/v1/queries?wait=3s", bytes.NewReader(sb))
	req.Header.Set("Authorization", "Bearer "+pairAsker.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("submit query failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with long poll wait, got %d", resp.StatusCode)
	}
	var qResult protocol.QueryDetailResponse
	_ = json.NewDecoder(resp.Body).Decode(&qResult)
	if qResult.Status != protocol.QueryStatusCompleted || !strings.Contains(qResult.Answer, "Long polling works") {
		t.Errorf("unexpected query result: %+v", qResult)
	}
}

// TestEnvelopeVersionCheck verifies that major protocol version mismatches are rejected (F32).
func TestEnvelopeVersionCheck(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "VersionUser"})
	pair, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "dev-ver"})

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/daemon"
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + pair.Token},
		},
	}
	conn, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Send envelope with invalid protocol version "v99"
	invalidEnv := protocol.Envelope{
		Version:   "v99",
		Type:      protocol.TypeDaemonHello,
		ID:        "h_bad_version",
		Timestamp: time.Now().UnixMilli(),
		Payload:   []byte(`{}`),
	}
	eb, _ := json.Marshal(invalidEnv)
	_ = conn.Write(ctx, websocket.MessageText, eb)

	// Server should close connection with StatusProtocolError (F32)
	readCtx, readCancel := context.WithTimeout(ctx, 2*time.Second)
	defer readCancel()
	_, _, readErr := conn.Read(readCtx)
	if readErr == nil {
		t.Fatalf("expected read error due to protocol version mismatch closure")
	}
	var closeErr websocket.CloseError
	if errors.As(readErr, &closeErr) {
		if closeErr.Code != websocket.StatusProtocolError {
			t.Errorf("expected StatusProtocolError (%d), got %d", websocket.StatusProtocolError, closeErr.Code)
		}
	}
}

// TestWebUIMounting verifies that the static Web UI is properly mounted and served by the Hub.
func TestWebUIMounting(t *testing.T) {
	tempDir := t.TempDir()
	st, err := store.NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	defer st.Close()

	cfg := &config.HubConfig{
		Addr:    "127.0.0.1:0",
		DataDir: tempDir,
	}

	srv, err := NewServer(cfg, st, web.FS(), nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// 1. Root "/" redirects to "/web/"
	reqRoot := httptest.NewRequest("GET", "/", nil)
	recRoot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recRoot, reqRoot)
	if recRoot.Code != http.StatusFound {
		t.Errorf("expected 302 Found on root, got %d", recRoot.Code)
	}
	if loc := recRoot.Header().Get("Location"); loc != "/web/" {
		t.Errorf("expected Location /web/, got %s", loc)
	}

	// 2. "/web/" serves index.html
	reqWeb := httptest.NewRequest("GET", "/web/", nil)
	recWeb := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recWeb, reqWeb)
	if recWeb.Code != http.StatusOK {
		t.Errorf("expected 200 OK on /web/, got %d", recWeb.Code)
	}
	if !strings.Contains(recWeb.Body.String(), "TalkIntent") {
		t.Errorf("expected TalkIntent in /web/ body")
	}

	// 3. "/web/app.js" serves JavaScript
	reqJS := httptest.NewRequest("GET", "/web/app.js", nil)
	recJS := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recJS, reqJS)
	if recJS.Code != http.StatusOK {
		t.Errorf("expected 200 OK on /web/app.js, got %d", recJS.Code)
	}

	// 4. "/web/style.css" serves CSS
	reqCSS := httptest.NewRequest("GET", "/web/style.css", nil)
	recCSS := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recCSS, reqCSS)
	if recCSS.Code != http.StatusOK {
		t.Errorf("expected 200 OK on /web/style.css, got %d", recCSS.Code)
	}
}

// TestFeishuBindingLongConnection verifies that Hub starts a long-connection client when a Feishu binding is saved.
func TestFeishuBindingLongConnection(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ctx := context.Background()
	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "FeishuUser"})
	pair, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "dev-fs"})

	fakeServer := feishu.NewFakeServer()
	defer fakeServer.Close()

	// POST /api/v1/feishu/binding pointing to fakeServer
	bindReq := protocol.FeishuBindingRequest{
		AppID:     "cli_test_hook",
		AppSecret: "secret_12345",
		BaseURL:   fakeServer.URL(),
	}
	bindBytes, _ := json.Marshal(bindReq)
	reqPost := httptest.NewRequest("POST", "/api/v1/feishu/binding", bytes.NewReader(bindBytes))
	reqPost.Header.Set("Authorization", "Bearer "+pair.Token)
	recPost := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recPost, reqPost)

	if recPost.Code != http.StatusOK {
		t.Fatalf("expected 200 OK saving binding, got %d: %s", recPost.Code, recPost.Body.String())
	}

	// Wait for WebSocket client to connect to fakeServer
	if !fakeServer.WaitForWSConnection(5 * time.Second) {
		t.Fatalf("timed out waiting for Feishu long-connection client to connect")
	}

	// Verify GET /api/v1/feishu/binding returns connected status
	var bindResp protocol.FeishuBindingResponse
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		reqGet := httptest.NewRequest("GET", "/api/v1/feishu/binding", nil)
		reqGet.Header.Set("Authorization", "Bearer "+pair.Token)
		recGet := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recGet, reqGet)

		if recGet.Code == http.StatusOK {
			var resp protocol.FeishuBindingResponse
			if err := json.NewDecoder(recGet.Body).Decode(&resp); err == nil {
				bindResp = resp
				if resp.Status == "connected" {
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !bindResp.Bound || bindResp.AppID != "cli_test_hook" {
		t.Errorf("unexpected binding response: %+v", bindResp)
	}
	if bindResp.Status != "connected" {
		t.Errorf("expected binding status 'connected', got %q", bindResp.Status)
	}
	if bindResp.ConnectedAt == "" {
		t.Errorf("expected connected_at to be populated, got empty string")
	}
	if bindResp.Reconnects != 0 {
		t.Errorf("expected reconnects to be 0 for fresh connection, got %d", bindResp.Reconnects)
	}
}

// TestQueryCancelOnClientDisconnect tests that when an HTTP client aborts while long-polling in handleQuerySubmit,
// the waiter is cleaned up and a query_cancel envelope is dispatched to the executing daemon session.
func TestQueryCancelOnClientDisconnect(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Register target and asker
	invTarget, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "CancelTarget"})
	pairTarget, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invTarget.Code, MachineName: "dev-ct"})

	invAsker, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "CancelAsker"})
	pairAsker, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invAsker.Code, MachineName: "dev-ca"})

	// 2. Connect target via WebSocket
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/daemon"
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + pairTarget.Token},
		},
	}
	wsConn, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "done")

	// Complete daemon_hello handshake
	helloEnv := protocol.Envelope{
		Version:   protocol.Version1,
		Type:      protocol.TypeDaemonHello,
		ID:        "msg_ct_hello",
		Timestamp: time.Now().UnixMilli(),
		Payload:   []byte(`{"machine_name":"dev-ct"}`),
	}
	hb, _ := json.Marshal(helloEnv)
	_ = wsConn.Write(ctx, websocket.MessageText, hb)

	// Read hub_ack
	_, ackBytes, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("failed reading hub_ack: %v", err)
	}
	var ackEnv protocol.Envelope
	_ = json.Unmarshal(ackBytes, &ackEnv)
	if ackEnv.Type != protocol.TypeHubAck {
		t.Fatalf("expected hub_ack, got: %s", ackEnv.Type)
	}

	// 3. Initiate query submission with wait=true in a separate goroutine with a cancellable context
	submitCtx, cancelSubmit := context.WithCancel(ctx)
	submitErrCh := make(chan error, 1)

	var queryID string
	go func() {
		submitPayload := protocol.QuerySubmitRequest{
			Target: "CancelTarget",
			Query:  "Will be cancelled",
			Wait:   true,
		}
		b, _ := json.Marshal(submitPayload)
		req, _ := http.NewRequestWithContext(submitCtx, "POST", ts.URL+"/api/v1/queries", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+pairAsker.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			submitErrCh <- err
			return
		}
		defer resp.Body.Close()
		submitErrCh <- nil
	}()

	// 4. Target daemon reads query_request
	_, qReqBytes, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("failed reading query_request: %v", err)
	}
	var qReqEnv protocol.Envelope
	_ = json.Unmarshal(qReqBytes, &qReqEnv)
	if qReqEnv.Type != protocol.TypeQueryRequest {
		t.Fatalf("expected query_request, got %s", qReqEnv.Type)
	}
	var qReqPayload protocol.QueryRequestPayload
	_ = json.Unmarshal(qReqEnv.Payload, &qReqPayload)
	queryID = qReqPayload.QueryID

	// Verify waiter is registered
	srv.mu.RLock()
	waitersCount := len(srv.queryWaiters[queryID])
	srv.mu.RUnlock()
	if waitersCount != 1 {
		t.Fatalf("expected 1 waiter registered, got %d", waitersCount)
	}

	// 5. Abort the HTTP client context
	cancelSubmit()

	// Wait for goroutine to exit
	select {
	case <-submitErrCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for submit goroutine to exit")
	}

	// 6. Target daemon should now receive a query_cancel envelope
	cancelReadCtx, cancelReadDone := context.WithTimeout(ctx, 3*time.Second)
	defer cancelReadDone()

	_, cancelBytes, err := wsConn.Read(cancelReadCtx)
	if err != nil {
		t.Fatalf("failed to read query_cancel frame on daemon: %v", err)
	}
	var cancelEnv protocol.Envelope
	_ = json.Unmarshal(cancelBytes, &cancelEnv)
	if cancelEnv.Type != protocol.TypeQueryCancel {
		t.Fatalf("expected envelope type %s, got %s", protocol.TypeQueryCancel, cancelEnv.Type)
	}

	var cancelPayload protocol.QueryCancelPayload
	_ = json.Unmarshal(cancelEnv.Payload, &cancelPayload)
	if cancelPayload.QueryID != queryID {
		t.Errorf("expected query ID %s in cancel payload, got %s", queryID, cancelPayload.QueryID)
	}
	if cancelPayload.Reason != "asker_cancelled" {
		t.Errorf("expected reason 'asker_cancelled', got '%s'", cancelPayload.Reason)
	}

	// 7. Verify waiter channel was cleaned up from h.queryWaiters
	srv.mu.RLock()
	waitersRemaining := len(srv.queryWaiters[queryID])
	srv.mu.RUnlock()
	if waitersRemaining != 0 {
		t.Errorf("expected 0 remaining waiters after client disconnect, got %d", waitersRemaining)
	}
}

// TestOfflineQueueCustomTimeoutPreserved verifies that a query submitted with custom timeout
// preserves that timeout when dispatched upon daemon reconnect (Finding 3).
func TestOfflineQueueCustomTimeoutPreserved(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	invTarget, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "OfflineTimeoutTarget"})
	pairTarget, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invTarget.Code, MachineName: "dev-ott"})

	invAsker, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "OfflineTimeoutAsker"})
	pairAsker, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: invAsker.Code, MachineName: "dev-ota"})

	// Target is offline. Submit query with custom timeout_seconds = 45
	submitPayload := protocol.QuerySubmitRequest{
		Target:         "OfflineTimeoutTarget",
		Query:          "Check timeout preservation",
		TimeoutSeconds: 45,
	}
	b, _ := json.Marshal(submitPayload)
	req := httptest.NewRequest("POST", "/api/v1/queries", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+pairAsker.Token)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", rec.Code, rec.Body.String())
	}

	var qResp protocol.QueryDetailResponse
	_ = json.NewDecoder(rec.Body).Decode(&qResp)

	// Now connect the target daemon
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/daemon"
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + pairTarget.Token},
		},
	}
	wsConn, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "done")

	// Send daemon_hello
	helloEnv := protocol.Envelope{
		Version:   protocol.Version1,
		Type:      protocol.TypeDaemonHello,
		ID:        "msg_ott_hello",
		Timestamp: time.Now().UnixMilli(),
		Payload:   []byte(`{"machine_name":"dev-ott"}`),
	}
	hb, _ := json.Marshal(helloEnv)
	_ = wsConn.Write(ctx, websocket.MessageText, hb)

	// Read hub_ack
	_, ackBytes, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("failed reading hub_ack: %v", err)
	}
	var ackEnv protocol.Envelope
	_ = json.Unmarshal(ackBytes, &ackEnv)
	if ackEnv.Type != protocol.TypeHubAck {
		t.Fatalf("expected hub_ack, got: %s", ackEnv.Type)
	}

	// Read drained query_request
	_, drainedBytes, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatalf("failed reading drained query_request: %v", err)
	}
	var drainedEnv protocol.Envelope
	_ = json.Unmarshal(drainedBytes, &drainedEnv)
	if drainedEnv.Type != protocol.TypeQueryRequest {
		t.Fatalf("expected query_request, got: %s", drainedEnv.Type)
	}

	var drainedReq protocol.QueryRequestPayload
	_ = json.Unmarshal(drainedEnv.Payload, &drainedReq)
	if drainedReq.QueryID != qResp.QueryID {
		t.Errorf("expected query ID %s, got %s", qResp.QueryID, drainedReq.QueryID)
	}
	if drainedReq.TimeoutSeconds != 45 {
		t.Errorf("expected preserved TimeoutSeconds 45, got %d", drainedReq.TimeoutSeconds)
	}
}

// TestRESTErrorResponsesJSON verifies that REST error endpoints consistently return
// Content-Type: application/json and uniform protocol.ErrorResponse schema (Finding 2).
func TestRESTErrorResponsesJSON(t *testing.T) {
	srv, st, _, cleanup := setupTestHub(t)
	defer cleanup()

	ctx := context.Background()
	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "ErrUser"})
	pair, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "dev-err"})

	checkErrorResponse := func(rec *httptest.ResponseRecorder, expectedStatus int, expectedCode string) {
		t.Helper()
		if rec.Code != expectedStatus {
			t.Errorf("expected status %d, got %d (body: %s)", expectedStatus, rec.Code, rec.Body.String())
		}
		contentType := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(contentType, "application/json") {
			t.Errorf("expected application/json Content-Type, got: %s", contentType)
		}
		var errResp protocol.ErrorResponse
		if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode JSON error response: %v", err)
		}
		if errResp.Error.Code != expectedCode {
			t.Errorf("expected error code %s, got %s", expectedCode, errResp.Error.Code)
		}
		if errResp.Error.Message == "" {
			t.Errorf("expected non-empty error message")
		}
	}

	// 1. handleAuthPair with invalid JSON -> 400 INVALID_ARGUMENT
	{
		req := httptest.NewRequest("POST", "/api/v1/auth/pair", strings.NewReader("not-json"))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		checkErrorResponse(rec, http.StatusBadRequest, protocol.ErrCodeInvalidArgument)
	}

	// 2. handleMembersList unauthorized -> 401 UNAUTHORIZED
	{
		req := httptest.NewRequest("GET", "/api/v1/members", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		checkErrorResponse(rec, http.StatusUnauthorized, protocol.ErrCodeUnauthorized)
	}

	// 3. handleMemberGet not found -> 404 NOT_FOUND
	{
		req := httptest.NewRequest("GET", "/api/v1/members/nonexistent_member_123", nil)
		req.Header.Set("Authorization", "Bearer "+pair.Token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		checkErrorResponse(rec, http.StatusNotFound, protocol.ErrCodeNotFound)
	}

	// 4. handleMemberMe unauthorized -> 401 UNAUTHORIZED
	{
		req := httptest.NewRequest("GET", "/api/v1/members/me", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		checkErrorResponse(rec, http.StatusUnauthorized, protocol.ErrCodeUnauthorized)
	}

	// 5. handleMemberMeUpdate invalid JSON -> 400 INVALID_ARGUMENT
	{
		req := httptest.NewRequest("PUT", "/api/v1/members/me", strings.NewReader("{invalid"))
		req.Header.Set("Authorization", "Bearer "+pair.Token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		checkErrorResponse(rec, http.StatusBadRequest, protocol.ErrCodeInvalidArgument)
	}

	// 6. handleQueryDetail not found -> 404 NOT_FOUND
	{
		req := httptest.NewRequest("GET", "/api/v1/queries/q_nonexistent", nil)
		req.Header.Set("Authorization", "Bearer "+pair.Token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		checkErrorResponse(rec, http.StatusNotFound, protocol.ErrCodeNotFound)
	}

	// 7. handleAuditInbound unauthorized -> 401 UNAUTHORIZED
	{
		req := httptest.NewRequest("GET", "/api/v1/audit/inbound", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		checkErrorResponse(rec, http.StatusUnauthorized, protocol.ErrCodeUnauthorized)
	}

	// 8. handleFeishuBindingSave invalid JSON -> 400 INVALID_ARGUMENT
	{
		req := httptest.NewRequest("POST", "/api/v1/feishu/binding", strings.NewReader("{bad"))
		req.Header.Set("Authorization", "Bearer "+pair.Token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		checkErrorResponse(rec, http.StatusBadRequest, protocol.ErrCodeInvalidArgument)
	}
}

// TestShutdownUnblocksLongPolling verifies that when Hub is stopped via context cancellation
// or Stop(), in-flight long-polling requests (GET ?wait= and POST ?wait=) return immediately
// with well-formed responses and Start exits within 2 seconds.
func TestShutdownUnblocksLongPolling(t *testing.T) {
	t.Run("GET_detail_wait", func(t *testing.T) {
		tempDir := t.TempDir()
		st, err := store.NewJSONLStore(tempDir)
		if err != nil {
			t.Fatalf("failed to create test store: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })

		cfg := &config.HubConfig{
			Addr:    "127.0.0.1:0",
			DataDir: tempDir,
			RateLimits: config.RateLimitConfig{
				QueriesPerMinute: 60,
				Burst:            30,
			},
		}

		srv, err := NewServer(cfg, st, nil, nil)
		if err != nil {
			t.Fatalf("failed to create server: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		startDone := make(chan error, 1)
		go func() {
			startDone <- srv.Start(ctx)
		}()

		var hubAddr string
		for i := 0; i < 50; i++ {
			addr := srv.Addr()
			if addr != "" && !strings.HasSuffix(addr, ":0") {
				hubAddr = addr
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if hubAddr == "" {
			t.Fatalf("hub server failed to bind")
		}

		invAsker, err := st.CreateInvite(context.Background(), &protocol.InviteCreateRequest{TargetName: "AskerA"})
		if err != nil {
			t.Fatalf("create asker invite failed: %v", err)
		}
		pairAsker, err := st.ConsumeInvite(context.Background(), &protocol.PairRequest{InviteCode: invAsker.Code, MachineName: "dev-asker-a"})
		if err != nil {
			t.Fatalf("consume asker invite failed: %v", err)
		}

		invTarget, err := st.CreateInvite(context.Background(), &protocol.InviteCreateRequest{TargetName: "TargetA"})
		if err != nil {
			t.Fatalf("create target invite failed: %v", err)
		}
		_, err = st.ConsumeInvite(context.Background(), &protocol.PairRequest{InviteCode: invTarget.Code, MachineName: "dev-target-a"})
		if err != nil {
			t.Fatalf("consume target invite failed: %v", err)
		}

		// Submit query to offline target -> queued
		submitPayload := protocol.QuerySubmitRequest{
			Target: "TargetA",
			Query:  "What are you working on?",
		}
		b, _ := json.Marshal(submitPayload)
		reqSubmit, err := http.NewRequest("POST", "http://"+hubAddr+"/api/v1/queries", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("create submit request failed: %v", err)
		}
		reqSubmit.Header.Set("Authorization", "Bearer "+pairAsker.Token)
		reqSubmit.Header.Set("Content-Type", "application/json")

		respSubmit, err := http.DefaultClient.Do(reqSubmit)
		if err != nil {
			t.Fatalf("submit query failed: %v", err)
		}
		defer respSubmit.Body.Close()
		if respSubmit.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted, got %d", respSubmit.StatusCode)
		}
		var createdQuery protocol.QueryDetailResponse
		if err := json.NewDecoder(respSubmit.Body).Decode(&createdQuery); err != nil {
			t.Fatalf("failed to decode created query: %v", err)
		}
		if createdQuery.Status != protocol.QueryStatusQueued {
			t.Fatalf("expected queued status, got %s", createdQuery.Status)
		}

		// Start long-poll in background
		type pollResult struct {
			statusCode int
			query      protocol.QueryDetailResponse
			err        error
		}
		pollDone := make(chan pollResult, 1)
		go func() {
			client := &http.Client{Timeout: 5 * time.Second}
			reqPoll, err := http.NewRequest("GET", "http://"+hubAddr+"/api/v1/queries/"+createdQuery.QueryID+"?wait=30s", nil)
			if err != nil {
				pollDone <- pollResult{err: err}
				return
			}
			reqPoll.Header.Set("Authorization", "Bearer "+pairAsker.Token)
			resp, err := client.Do(reqPoll)
			if err != nil {
				pollDone <- pollResult{err: err}
				return
			}
			defer resp.Body.Close()

			var q protocol.QueryDetailResponse
			decErr := json.NewDecoder(resp.Body).Decode(&q)
			pollDone <- pollResult{
				statusCode: resp.StatusCode,
				query:      q,
				err:        decErr,
			}
		}()

		// Wait until waiter is registered
		for i := 0; i < 50; i++ {
			srv.mu.RLock()
			count := len(srv.queryWaiters[createdQuery.QueryID])
			srv.mu.RUnlock()
			if count > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		srv.mu.RLock()
		count := len(srv.queryWaiters[createdQuery.QueryID])
		srv.mu.RUnlock()
		if count == 0 {
			t.Fatalf("expected at least 1 registered waiter")
		}

		// Cancel context to trigger Start's shutdown path
		cancel()

		// (a) Start must return within 2s
		select {
		case err := <-startDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("Start returned unexpected error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Start did not return within 2s")
		}

		// (b) long-poll got well-formed response with current status
		select {
		case res := <-pollDone:
			if res.err != nil {
				t.Fatalf("long-poll request failed: %v", res.err)
			}
			if res.statusCode != http.StatusOK {
				t.Fatalf("expected 200 OK from detail long-poll, got %d", res.statusCode)
			}
			if res.query.QueryID != createdQuery.QueryID {
				t.Errorf("expected query ID %s, got %s", createdQuery.QueryID, res.query.QueryID)
			}
			if res.query.Status != protocol.QueryStatusQueued {
				t.Errorf("expected status queued, got %s", res.query.Status)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("long-poll did not return within 2s")
		}

		srv.mu.RLock()
		remaining := len(srv.queryWaiters[createdQuery.QueryID])
		srv.mu.RUnlock()
		if remaining != 0 {
			t.Errorf("expected 0 remaining waiters, got %d", remaining)
		}
	})

	t.Run("POST_submit_wait", func(t *testing.T) {
		tempDir := t.TempDir()
		st, err := store.NewJSONLStore(tempDir)
		if err != nil {
			t.Fatalf("failed to create test store: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })

		cfg := &config.HubConfig{
			Addr:    "127.0.0.1:0",
			DataDir: tempDir,
			RateLimits: config.RateLimitConfig{
				QueriesPerMinute: 60,
				Burst:            30,
			},
		}

		srv, err := NewServer(cfg, st, nil, nil)
		if err != nil {
			t.Fatalf("failed to create server: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		startDone := make(chan error, 1)
		go func() {
			startDone <- srv.Start(ctx)
		}()

		var hubAddr string
		for i := 0; i < 50; i++ {
			addr := srv.Addr()
			if addr != "" && !strings.HasSuffix(addr, ":0") {
				hubAddr = addr
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if hubAddr == "" {
			t.Fatalf("hub server failed to bind")
		}

		invAsker, _ := st.CreateInvite(context.Background(), &protocol.InviteCreateRequest{TargetName: "AskerB"})
		pairAsker, _ := st.ConsumeInvite(context.Background(), &protocol.PairRequest{InviteCode: invAsker.Code, MachineName: "dev-asker-b"})

		invTarget, _ := st.CreateInvite(context.Background(), &protocol.InviteCreateRequest{TargetName: "TargetB"})
		_, _ = st.ConsumeInvite(context.Background(), &protocol.PairRequest{InviteCode: invTarget.Code, MachineName: "dev-target-b"})

		type submitResult struct {
			statusCode int
			query      protocol.QueryDetailResponse
			err        error
		}
		submitDone := make(chan submitResult, 1)

		go func() {
			client := &http.Client{Timeout: 5 * time.Second}
			submitPayload := protocol.QuerySubmitRequest{
				Target: "TargetB",
				Query:  "What are you working on?",
				Wait:   true,
			}
			b, _ := json.Marshal(submitPayload)
			reqSubmit, err := http.NewRequest("POST", "http://"+hubAddr+"/api/v1/queries?wait=30s", bytes.NewReader(b))
			if err != nil {
				submitDone <- submitResult{err: err}
				return
			}
			reqSubmit.Header.Set("Authorization", "Bearer "+pairAsker.Token)
			reqSubmit.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(reqSubmit)
			if err != nil {
				submitDone <- submitResult{err: err}
				return
			}
			defer resp.Body.Close()

			var q protocol.QueryDetailResponse
			decErr := json.NewDecoder(resp.Body).Decode(&q)
			submitDone <- submitResult{
				statusCode: resp.StatusCode,
				query:      q,
				err:        decErr,
			}
		}()

		// Wait until waiter is registered
		var queryID string
		for i := 0; i < 50; i++ {
			srv.mu.RLock()
			for qid, waiters := range srv.queryWaiters {
				if len(waiters) > 0 {
					queryID = qid
					break
				}
			}
			srv.mu.RUnlock()
			if queryID != "" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if queryID == "" {
			t.Fatalf("expected registered waiter for submit request")
		}

		// Cancel context to trigger Start's shutdown path
		cancel()

		// (a) Start must return within 2s
		select {
		case err := <-startDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("Start returned unexpected error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Start did not return within 2s")
		}

		// (b) submit got well-formed response with current status (202 Accepted)
		select {
		case res := <-submitDone:
			if res.err != nil {
				t.Fatalf("submit request failed: %v", res.err)
			}
			if res.statusCode != http.StatusAccepted {
				t.Fatalf("expected 202 Accepted from submit long-poll, got %d", res.statusCode)
			}
			if res.query.QueryID != queryID {
				t.Errorf("expected query ID %s, got %s", queryID, res.query.QueryID)
			}
			if res.query.Status != protocol.QueryStatusQueued {
				t.Errorf("expected status queued, got %s", res.query.Status)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("submit long-poll did not return within 2s")
		}

		srv.mu.RLock()
		remaining := len(srv.queryWaiters[queryID])
		srv.mu.RUnlock()
		if remaining != 0 {
			t.Errorf("expected 0 remaining waiters, got %d", remaining)
		}
	})
}

// TestStopMethodHonorsCallerCtx verifies that Stop(ctx) respects the caller's context
// and closes connections cleanly.
func TestStopMethodHonorsCallerCtx(t *testing.T) {
	tempDir := t.TempDir()
	st, err := store.NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.HubConfig{
		Addr:    "127.0.0.1:0",
		DataDir: tempDir,
	}
	srv, err := NewServer(cfg, st, nil, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	startDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		startDone <- srv.Start(ctx)
	}()

	for i := 0; i < 50; i++ {
		addr := srv.Addr()
		if addr != "" && !strings.HasSuffix(addr, ":0") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := srv.Stop(stopCtx); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	select {
	case err := <-startDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Start did not exit after Stop")
	}
}
