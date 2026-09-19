package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sskift/talkintent/internal/protocol"
)

func TestJSONLStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	st, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	// 1. Create invite
	invReq := &protocol.InviteCreateRequest{
		TargetName:     "张三",
		Aliases:        []string{"zhangsan", "三哥"},
		ExpiresInHours: 24,
	}
	inv, err := st.CreateInvite(ctx, invReq)
	if err != nil {
		t.Fatalf("CreateInvite failed: %v", err)
	}
	if inv.Code == "" {
		t.Fatalf("empty invite code returned")
	}

	// Verify events.jsonl does not contain the plaintext invite code
	eventsContent, err := os.ReadFile(filepath.Join(tempDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl failed: %v", err)
	}
	if strings.Contains(string(eventsContent), inv.Code) {
		t.Fatalf("plaintext invite code %q found in events.jsonl", inv.Code)
	}

	// 2. Consume invite (Pair)
	pairReq := &protocol.PairRequest{
		InviteCode:    inv.Code,
		MachineName:   "zhangsan-laptop",
		ClientVersion: "1.0.0",
	}
	pairResp, err := st.ConsumeInvite(ctx, pairReq)
	if err != nil {
		t.Fatalf("ConsumeInvite failed: %v", err)
	}
	if pairResp.MemberID == "" || pairResp.Token == "" {
		t.Fatalf("invalid pair response: %+v", pairResp)
	}

	// 3. Resolve by token
	memByToken, err := st.GetMemberByToken(ctx, pairResp.Token)
	if err != nil {
		t.Fatalf("GetMemberByToken failed: %v", err)
	}
	if memByToken.Name != "张三" {
		t.Errorf("expected member name 张三, got %s", memByToken.Name)
	}

	// 4. Resolve by alias
	resolved, candidates, err := st.ResolveTargetMember(ctx, "三哥")
	if err != nil {
		t.Fatalf("ResolveTargetMember by alias failed: %v", err)
	}
	if len(candidates) > 0 {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
	if resolved.ID != pairResp.MemberID {
		t.Errorf("expected resolved ID %s, got %s", pairResp.MemberID, resolved.ID)
	}

	// 5. Resolve by natural language sentence (reverse substring)
	nlResolved, _, err := st.ResolveTargetMember(ctx, "问一下张三现在的登录模块改完了吗")
	if err != nil {
		t.Fatalf("ResolveTargetMember by NL failed: %v", err)
	}
	if nlResolved.ID != pairResp.MemberID {
		t.Errorf("expected resolved ID %s, got %s", pairResp.MemberID, nlResolved.ID)
	}

	// 6. Test Feishu binding encryption
	feishuReq := &protocol.FeishuBindingRequest{
		AppID:      "cli_test_app_id",
		AppSecret:  "super_secret_feishu_key_12345",
		EncryptKey: "feishu_encrypt_key_xyz",
	}
	if err := st.SaveFeishuBinding(ctx, pairResp.MemberID, feishuReq); err != nil {
		t.Fatalf("SaveFeishuBinding failed: %v", err)
	}

	eventsAfterBinding, _ := os.ReadFile(filepath.Join(tempDir, "events.jsonl"))
	if strings.Contains(string(eventsAfterBinding), "super_secret_feishu_key_12345") {
		t.Fatalf("plaintext AppSecret leaked into events.jsonl")
	}

	// Verify decrypted binding
	binding, err := st.GetFeishuBinding(ctx, pairResp.MemberID)
	if err != nil {
		t.Fatalf("GetFeishuBinding failed: %v", err)
	}
	if binding.AppSecret != "super_secret_feishu_key_12345" {
		t.Errorf("decrypted AppSecret mismatch: got %s", binding.AppSecret)
	}

	// 7. Test Offline Queue TTL expiration
	staleQuery := &protocol.QueryDetailResponse{
		QueryID:        "q_expired_1",
		Status:         protocol.QueryStatusQueued,
		AskerID:        "mem_asker",
		AskerName:      "李四",
		TargetMemberID: pairResp.MemberID,
		Query:          "stale query",
		TTLExpiresAt:   time.Now().Add(-10 * time.Minute).UnixMilli(),
		CreatedAt:      time.Now().Add(-20 * time.Minute).UnixMilli(),
	}
	if err := st.CreateQuery(ctx, staleQuery); err != nil {
		t.Fatalf("CreateQuery failed: %v", err)
	}

	freshQuery := &protocol.QueryDetailResponse{
		QueryID:        "q_fresh_1",
		Status:         protocol.QueryStatusQueued,
		AskerID:        "mem_asker",
		AskerName:      "李四",
		TargetMemberID: pairResp.MemberID,
		Query:          "fresh query",
		TTLExpiresAt:   time.Now().Add(10 * time.Minute).UnixMilli(),
		CreatedAt:      time.Now().UnixMilli(),
	}
	if err := st.CreateQuery(ctx, freshQuery); err != nil {
		t.Fatalf("CreateQuery failed: %v", err)
	}

	queued, err := st.GetQueuedQueriesForMember(ctx, pairResp.MemberID)
	if err != nil {
		t.Fatalf("GetQueuedQueriesForMember failed: %v", err)
	}
	if len(queued) != 1 || queued[0].QueryID != "q_fresh_1" {
		t.Fatalf("expected only fresh query, got %d queries", len(queued))
	}

	// Check status of expired query
	expiredQ, err := st.GetQuery(ctx, "q_expired_1")
	if err != nil {
		t.Fatalf("GetQuery failed: %v", err)
	}
	if expiredQ.Status != protocol.QueryStatusExpired {
		t.Errorf("expected status expired, got %s", expiredQ.Status)
	}

	// 8. Test Compact
	if err := st.Compact(ctx); err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	// 9. Close and reopen to verify in-memory index replay after compact
	st.Close()

	reopened, err := NewJSONLStore(filepath.Clean(tempDir))
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer reopened.Close()

	memReplayed, err := reopened.GetMember(ctx, pairResp.MemberID)
	if err != nil {
		t.Fatalf("GetMember after replay failed: %v", err)
	}
	if memReplayed.Name != "张三" {
		t.Errorf("expected replayed name 张三, got %s", memReplayed.Name)
	}

	bindingReplayed, err := reopened.GetFeishuBinding(ctx, pairResp.MemberID)
	if err != nil {
		t.Fatalf("GetFeishuBinding after replay failed: %v", err)
	}
	if bindingReplayed.AppSecret != "super_secret_feishu_key_12345" {
		t.Errorf("expected decrypted secret after replay, got %s", bindingReplayed.AppSecret)
	}
}
