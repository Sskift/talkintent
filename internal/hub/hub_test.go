package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/protocol"
	"github.com/Sskift/talkintent/internal/store"
)

func setupTestHub(t *testing.T) (*HubServer, store.Store, string, func()) {
	tempDir := t.TempDir()
	st, err := store.NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}

	cfg := &config.HubConfig{
		Addr:    ":0",
		DataDir: tempDir,
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
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", rec.Code)
	}

	// 2. With invalid auth header -> 401
	req = httptest.NewRequest("POST", "/api/v1/admin/invites", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer invalid_token")
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", rec.Code)
	}

	// 3. With valid admin token -> 201 Created
	req = httptest.NewRequest("POST", "/api/v1/admin/invites", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

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
	srv.httpServer.Handler.ServeHTTP(rec, req)

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
	srv.httpServer.Handler.ServeHTTP(rec2, req2)

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
	srv.httpServer.Handler.ServeHTTP(recDetailStranger, reqDetailStranger)

	if recDetailStranger.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for stranger, got %d", recDetailStranger.Code)
	}

	// Asker (Member A) attempts access -> 200 OK
	reqDetailAsker := httptest.NewRequest("GET", "/api/v1/queries/"+queryID, nil)
	reqDetailAsker.Header.Set("Authorization", "Bearer "+pairA.Token)
	recDetailAsker := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(recDetailAsker, reqDetailAsker)

	if recDetailAsker.Code != http.StatusOK {
		t.Errorf("expected 200 OK for asker, got %d", recDetailAsker.Code)
	}

	// Target (Member B) attempts access -> 200 OK
	reqDetailTarget := httptest.NewRequest("GET", "/api/v1/queries/"+queryID, nil)
	reqDetailTarget.Header.Set("Authorization", "Bearer "+pairB.Token)
	recDetailTarget := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(recDetailTarget, reqDetailTarget)

	if recDetailTarget.Code != http.StatusOK {
		t.Errorf("expected 200 OK for target, got %d", recDetailTarget.Code)
	}

	// Admin attempts access -> 200 OK
	reqDetailAdmin := httptest.NewRequest("GET", "/api/v1/queries/"+queryID, nil)
	reqDetailAdmin.Header.Set("Authorization", "Bearer "+adminToken)
	recDetailAdmin := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(recDetailAdmin, reqDetailAdmin)

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
