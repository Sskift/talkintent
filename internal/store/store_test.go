package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
		AppID:     "cli_test_app_id",
		AppSecret: "super_secret_feishu_key_12345",
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

// TestReplay1000EventsEquality verifies §7.3 requirement: 1000-event replay equality.
func TestReplay1000EventsEquality(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	st, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("NewJSONLStore failed: %v", err)
	}

	// Create 20 members via invite/pair
	var memberIDs []string
	var tokens []string
	for i := 0; i < 20; i++ {
		inv, err := st.CreateInvite(ctx, &protocol.InviteCreateRequest{
			TargetName: fmt.Sprintf("Member_%02d", i),
			Aliases:    []string{fmt.Sprintf("alias_%02d", i), fmt.Sprintf("short%d", i)},
		})
		if err != nil {
			t.Fatalf("CreateInvite %d failed: %v", i, err)
		}
		pairResp, err := st.ConsumeInvite(ctx, &protocol.PairRequest{
			InviteCode:    inv.Code,
			MachineName:   fmt.Sprintf("host-%02d", i),
			ClientVersion: "1.0.0",
		})
		if err != nil {
			t.Fatalf("ConsumeInvite %d failed: %v", i, err)
		}
		memberIDs = append(memberIDs, pairResp.MemberID)
		tokens = append(tokens, pairResp.Token)
	}

	// Update presence & bindings
	for i, mid := range memberIDs {
		_ = st.SetMemberOnline(ctx, mid, i%2 == 0, fmt.Sprintf("machine-%d", i), []string{fmt.Sprintf("ws-%d", i)})
		_ = st.SaveFeishuBinding(ctx, mid, &protocol.FeishuBindingRequest{
			AppID:     fmt.Sprintf("app_%d", i),
			AppSecret: fmt.Sprintf("secret_%d", i),
		})
	}

	// Create queries up to 1000 total events
	// Total events so far: 20 invites + 20 pairs (each pair = consumed + member_created + token_indexed = 3 events)
	// => 20 + 60 = 80 events.
	// Presence: 20 events.
	// Bindings: 20 events.
	// Total base events: 120.
	// We add 440 queries (create + update = 880 events) => 120 + 880 = 1000 events.
	for i := 0; i < 440; i++ {
		asker := memberIDs[i%len(memberIDs)]
		target := memberIDs[(i+1)%len(memberIDs)]
		qid := fmt.Sprintf("qry_test_%04d", i)
		q := &protocol.QueryDetailResponse{
			QueryID:          qid,
			Status:           protocol.QueryStatusQueued,
			AskerID:          asker,
			AskerName:        fmt.Sprintf("Member_%02d", i%len(memberIDs)),
			TargetMemberID:   target,
			TargetMemberName: fmt.Sprintf("Member_%02d", (i+1)%len(memberIDs)),
			Query:            fmt.Sprintf("How is feature %d progressing?", i),
			CreatedAt:        time.Now().UnixMilli() + int64(i),
		}
		if err := st.CreateQuery(ctx, q); err != nil {
			t.Fatalf("CreateQuery %d failed: %v", i, err)
		}

		status := protocol.QueryStatusCompleted
		if i%5 == 0 {
			status = protocol.QueryStatusError
		}
		_ = st.UpdateQueryStatus(ctx, qid, status, fmt.Sprintf("Feature %d done", i), []string{"git_status", "git_diff"}, int64(100+i), protocol.TokenUsage{TotalTokens: 50 + i}, "")
	}

	// Capture in-memory state before closing
	membersBefore, _ := st.ListMembers(ctx)
	q1Before, _ := st.GetQuery(ctx, "qry_test_0001")
	q100Before, _ := st.GetQuery(ctx, "qry_test_0100")
	auditInboundBefore, _ := st.GetInboundAudit(ctx, memberIDs[1], 100, 0)

	_ = st.Close()

	// Reopen store from disk
	reopened, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer reopened.Close()

	membersAfter, err := reopened.ListMembers(ctx)
	if err != nil {
		t.Fatalf("ListMembers after replay failed: %v", err)
	}
	if len(membersBefore) != len(membersAfter) {
		t.Fatalf("member count mismatch: before=%d, after=%d", len(membersBefore), len(membersAfter))
	}

	// Verify token lookup works on all replayed members
	for i, tok := range tokens {
		m, err := reopened.GetMemberByToken(ctx, tok)
		if err != nil || m.ID != memberIDs[i] {
			t.Fatalf("token lookup mismatch for member %s: err=%v", memberIDs[i], err)
		}
	}

	// Verify queries replayed accurately
	q1After, err := reopened.GetQuery(ctx, "qry_test_0001")
	if err != nil || q1After.Status != q1Before.Status || q1After.Answer != q1Before.Answer {
		t.Fatalf("query 1 replay mismatch: got %+v, want %+v", q1After, q1Before)
	}
	q100After, err := reopened.GetQuery(ctx, "qry_test_0100")
	if err != nil || q100After.Status != q100Before.Status || q100After.Answer != q100Before.Answer {
		t.Fatalf("query 100 replay mismatch: got %+v, want %+v", q100After, q100Before)
	}

	// Verify audit counts and items
	auditInboundAfter, err := reopened.GetInboundAudit(ctx, memberIDs[1], 100, 0)
	if err != nil || auditInboundAfter.Total != auditInboundBefore.Total {
		t.Fatalf("audit count mismatch: before=%d, after=%d", auditInboundBefore.Total, auditInboundAfter.Total)
	}
}

// TestTruncatedLineCrashRecovery verifies §7.3 requirement: crash recovery for a half-written trailing JSONL line.
func TestTruncatedLineCrashRecovery(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	st, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("NewJSONLStore failed: %v", err)
	}

	inv, err := st.CreateInvite(ctx, &protocol.InviteCreateRequest{
		TargetName: "Alice",
		Aliases:    []string{"alice_dev"},
	})
	if err != nil {
		t.Fatalf("CreateInvite failed: %v", err)
	}

	pairResp, err := st.ConsumeInvite(ctx, &protocol.PairRequest{
		InviteCode:    inv.Code,
		MachineName:   "host-alice",
		ClientVersion: "1.0.0",
	})
	if err != nil {
		t.Fatalf("ConsumeInvite failed: %v", err)
	}

	q := &protocol.QueryDetailResponse{
		QueryID:          "q_trunc_1",
		Status:           protocol.QueryStatusCompleted,
		AskerID:          "mem_other",
		AskerName:        "Bob",
		TargetMemberID:   pairResp.MemberID,
		TargetMemberName: "Alice",
		Query:            "How is the auth refactor?",
		Answer:           "All tests pass",
		CreatedAt:        time.Now().UnixMilli(),
	}
	if err := st.CreateQuery(ctx, q); err != nil {
		t.Fatalf("CreateQuery failed: %v", err)
	}

	// Close store gracefully
	if err := st.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Inject a half-written corrupted trailing JSON line (simulating sudden power loss or process kill)
	eventsPath := filepath.Join(tempDir, "events.jsonl")
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("OpenFile failed: %v", err)
	}
	corruptedHalfLine := []byte(`{"type":"query_updated","timestamp":1726828824500,"data":{"id":"q_trunc_1","stat`)
	if _, err := f.Write(corruptedHalfLine); err != nil {
		t.Fatalf("Write corrupted line failed: %v", err)
	}
	_ = f.Close()

	// Reopen store — recovery must truncate back to last valid boundary and not fail
	reopened, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("NewJSONLStore failed to recover from truncated line: %v", err)
	}
	defer reopened.Close()

	// Verify valid records survived
	mem, err := reopened.GetMember(ctx, pairResp.MemberID)
	if err != nil || mem.Name != "Alice" {
		t.Fatalf("failed to retrieve valid member Alice after recovery: %v", err)
	}

	qSurvived, err := reopened.GetQuery(ctx, "q_trunc_1")
	if err != nil || qSurvived.Answer != "All tests pass" {
		t.Fatalf("failed to retrieve valid query after recovery: %v", err)
	}

	// Append a new event to ensure the file pointer and truncation were handled cleanly
	qNew := &protocol.QueryDetailResponse{
		QueryID:          "q_trunc_2",
		Status:           protocol.QueryStatusQueued,
		AskerID:          "mem_other",
		AskerName:        "Bob",
		TargetMemberID:   pairResp.MemberID,
		TargetMemberName: "Alice",
		Query:            "New query after crash",
		CreatedAt:        time.Now().UnixMilli(),
	}
	if err := reopened.CreateQuery(ctx, qNew); err != nil {
		t.Fatalf("CreateQuery after recovery failed: %v", err)
	}

	// Close and reopen again to verify the new event is completely valid JSON and parses cleanly
	_ = reopened.Close()

	reopened2, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("second NewJSONLStore failed: %v", err)
	}
	defer reopened2.Close()

	qNewCheck, err := reopened2.GetQuery(ctx, "q_trunc_2")
	if err != nil || qNewCheck.Query != "New query after crash" {
		t.Fatalf("new query verification failed: %v", err)
	}
}

// TestNameAndAliasLookup verifies F35, exact match, case insensitivity, deduplication, and candidate ambiguity.
func TestNameAndAliasLookup(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	st, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("NewJSONLStore failed: %v", err)
	}
	defer st.Close()

	// 1. Create Zhang San with duplicate-like aliases
	inv1, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{
		TargetName: "张三",
		Aliases:    []string{"zhangsan", "三哥", "ZHANGSAN"}, // case variations in aliases
	})
	p1, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv1.Code, MachineName: "dev-zs"})

	// 2. Exact match case-insensitive
	res, candidates, err := st.ResolveTargetMember(ctx, "ZHANGSAN")
	if err != nil || len(candidates) != 0 || res.ID != p1.MemberID {
		t.Fatalf("expected exact case-insensitive match for ZHANGSAN: err=%v, res=%+v", err, res)
	}

	// 3. Candidate deduplication (F35): multiple matching aliases on the SAME member must NOT produce ErrAmbiguousMatch
	res2, candidates2, err := st.ResolveTargetMember(ctx, "zhangsan")
	if err != nil || len(candidates2) != 0 || res2.ID != p1.MemberID {
		t.Fatalf("candidate dedup failed (F35): err=%v, candidates=%+v", err, candidates2)
	}

	// 4. Natural language reverse substring matching
	nlRes, _, err := st.ResolveTargetMember(ctx, "麻烦帮我问下三哥现在网关改好没")
	if err != nil || nlRes.ID != p1.MemberID {
		t.Fatalf("NL reverse substring matching failed: err=%v, res=%+v", err, nlRes)
	}

	// 5. Create Zhang Wei with overlapping alias "小张"
	inv2, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{
		TargetName: "张伟",
		Aliases:    []string{"小张", "zw"},
	})
	p2, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv2.Code, MachineName: "dev-zw"})

	// Add "小张" to Zhang San as well
	_, _ = st.UpdateMember(ctx, p1.MemberID, &protocol.MemberUpdateRequest{
		Name:    "张三",
		Aliases: []string{"zhangsan", "三哥", "小张"},
	})

	// Resolving "小张" now matches two different members -> ErrAmbiguousMatch
	ambRes, ambCandidates, err := st.ResolveTargetMember(ctx, "小张")
	if err != ErrAmbiguousMatch {
		t.Fatalf("expected ErrAmbiguousMatch, got err=%v, res=%+v", err, ambRes)
	}
	if len(ambCandidates) != 2 {
		t.Fatalf("expected exactly 2 candidates, got %d: %+v", len(ambCandidates), ambCandidates)
	}

	// Both IDs must be represented
	foundP1, foundP2 := false, false
	for _, c := range ambCandidates {
		if c.ID == p1.MemberID {
			foundP1 = true
		}
		if c.ID == p2.MemberID {
			foundP2 = true
		}
	}
	if !foundP1 || !foundP2 {
		t.Fatalf("candidates do not contain both members: %+v", ambCandidates)
	}
}

// TestTokenHashVerification verifies tokens and invite codes are never stored in plaintext at rest.
func TestTokenHashVerification(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	st, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("NewJSONLStore failed: %v", err)
	}
	defer st.Close()

	// Verify permissions on master.key and salt (POSIX)
	if runtime.GOOS != "windows" {
		infoKey, err := os.Stat(filepath.Join(tempDir, "master.key"))
		if err != nil || infoKey.Mode().Perm() != 0600 {
			t.Errorf("expected master.key to have 0600 permissions, got %v", infoKey.Mode().Perm())
		}
		infoSalt, err := os.Stat(filepath.Join(tempDir, "salt"))
		if err != nil || infoSalt.Mode().Perm() != 0600 {
			t.Errorf("expected salt to have 0600 permissions, got %v", infoSalt.Mode().Perm())
		}
	}

	inv, err := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "Charlie"})
	if err != nil {
		t.Fatalf("CreateInvite failed: %v", err)
	}
	pairResp, err := st.ConsumeInvite(ctx, &protocol.PairRequest{
		InviteCode:  inv.Code,
		MachineName: "host-c",
	})
	if err != nil {
		t.Fatalf("ConsumeInvite failed: %v", err)
	}

	// Verify token hash function works deterministically
	expectedHash := HashToken(pairResp.Token)
	if expectedHash == "" || expectedHash == pairResp.Token {
		t.Fatalf("invalid token hash: %s", expectedHash)
	}

	// Verify events.jsonl has zero plaintext tokens or invite codes
	content, err := os.ReadFile(filepath.Join(tempDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile events.jsonl failed: %v", err)
	}
	raw := string(content)

	if strings.Contains(raw, inv.Code) {
		t.Errorf("plaintext invite code %s found in events.jsonl", inv.Code)
	}
	if strings.Contains(raw, pairResp.Token) {
		t.Errorf("plaintext member token %s found in events.jsonl", pairResp.Token)
	}

	// The hash MUST be present in events.jsonl
	if !strings.Contains(raw, expectedHash) {
		t.Errorf("token hash %s not found in events.jsonl", expectedHash)
	}
}

// TestEncryptionRoundTrip verifies AES-GCM credential encryption at rest (F3, F34).
func TestEncryptionRoundTrip(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	st, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("NewJSONLStore failed: %v", err)
	}
	defer st.Close()

	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "Dave"})
	pairResp, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "host-d"})

	req := &protocol.FeishuBindingRequest{
		AppID:     "cli_aa17a38637f8dbb7",
		AppSecret: "sec_TOP_SECRET_CREDENTIAL_999",
	}

	if err := st.SaveFeishuBinding(ctx, pairResp.MemberID, req); err != nil {
		t.Fatalf("SaveFeishuBinding failed: %v", err)
	}

	// Raw disk content must NOT contain secret or tokens
	data, _ := os.ReadFile(filepath.Join(tempDir, "events.jsonl"))
	raw := string(data)
	if strings.Contains(raw, "sec_TOP_SECRET_CREDENTIAL_999") {
		t.Fatal("plaintext AppSecret leaked into events.jsonl")
	}

	// Read decrypted
	binding, err := st.GetFeishuBinding(ctx, pairResp.MemberID)
	if err != nil {
		t.Fatalf("GetFeishuBinding failed: %v", err)
	}
	if binding.AppSecret != req.AppSecret {
		t.Errorf("decrypted credentials mismatch: %+v", binding)
	}

	// Delete binding
	if err := st.DeleteFeishuBinding(ctx, pairResp.MemberID); err != nil {
		t.Fatalf("DeleteFeishuBinding failed: %v", err)
	}
	_, err = st.GetFeishuBinding(ctx, pairResp.MemberID)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after deletion, got %v", err)
	}
}

// TestSnapshotReplayEquivalence verifies §7.3 requirement: snapshot + replay equivalence (F29).
func TestSnapshotReplayEquivalence(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	st, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("NewJSONLStore failed: %v", err)
	}

	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "Eve"})
	pairResp, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "host-e"})

	_ = st.SaveFeishuBinding(ctx, pairResp.MemberID, &protocol.FeishuBindingRequest{
		AppID:     "cli_eve",
		AppSecret: "eve_secret",
	})

	for i := 0; i < 50; i++ {
		qid := fmt.Sprintf("q_compact_%d", i)
		_ = st.CreateQuery(ctx, &protocol.QueryDetailResponse{
			QueryID:          qid,
			Status:           protocol.QueryStatusCompleted,
			AskerID:          "mem_other",
			AskerName:        "Other",
			TargetMemberID:   pairResp.MemberID,
			TargetMemberName: "Eve",
			Query:            fmt.Sprintf("Query %d", i),
			Answer:           fmt.Sprintf("Answer %d", i),
			CreatedAt:        time.Now().UnixMilli() + int64(i),
		})
	}

	preCompactMems, _ := st.ListMembers(ctx)
	preCompactBinding, _ := st.GetFeishuBinding(ctx, pairResp.MemberID)
	preCompactQ25, _ := st.GetQuery(ctx, "q_compact_25")

	// Trigger compaction
	if err := st.Compact(ctx); err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	_ = st.Close()

	// Reopen after compaction
	reopened, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("reopen after compact failed: %v", err)
	}
	defer reopened.Close()

	postCompactMems, err := reopened.ListMembers(ctx)
	if err != nil || len(preCompactMems) != len(postCompactMems) {
		t.Fatalf("member count mismatch after compact: before=%d, after=%d", len(preCompactMems), len(postCompactMems))
	}

	postCompactBinding, err := reopened.GetFeishuBinding(ctx, pairResp.MemberID)
	if err != nil || postCompactBinding.AppSecret != preCompactBinding.AppSecret {
		t.Fatalf("binding mismatch after compact: got %+v", postCompactBinding)
	}

	postCompactQ25, err := reopened.GetQuery(ctx, "q_compact_25")
	if err != nil || postCompactQ25.Answer != preCompactQ25.Answer {
		t.Fatalf("query mismatch after compact: got %+v", postCompactQ25)
	}
}

// TestQueueFIFOAndRequeueAndSweep verifies F19, F21, F41 (requeue on disconnect, FIFO order, TTL sweep).
func TestQueueFIFOAndRequeueAndSweep(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	st, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("NewJSONLStore failed: %v", err)
	}
	defer st.Close()

	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "Frank"})
	pFrank, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "host-f"})

	// Enqueue 3 queries
	for i := 1; i <= 3; i++ {
		q := &protocol.QueryDetailResponse{
			QueryID:          fmt.Sprintf("q_fifo_%d", i),
			Status:           protocol.QueryStatusQueued,
			AskerID:          "mem_other",
			AskerName:        "Asker",
			TargetMemberID:   pFrank.MemberID,
			TargetMemberName: "Frank",
			Query:            fmt.Sprintf("FIFO query %d", i),
			CreatedAt:        time.Now().UnixMilli() + int64(i),
		}
		if err := st.CreateQuery(ctx, q); err != nil {
			t.Fatalf("CreateQuery %d failed: %v", i, err)
		}
	}

	// Verify FIFO order and 1-based positions
	queued, err := st.GetQueuedQueriesForMember(ctx, pFrank.MemberID)
	if err != nil || len(queued) != 3 {
		t.Fatalf("expected 3 queued queries, got %d (err: %v)", len(queued), err)
	}
	for i, q := range queued {
		if q.QueuePosition != i+1 {
			t.Errorf("expected queue position %d, got %d", i+1, q.QueuePosition)
		}
	}

	// Mark q_fifo_1 as Dispatched (in flight)
	_ = st.UpdateQueryStatus(ctx, "q_fifo_1", protocol.QueryStatusDispatched, "", nil, 0, protocol.TokenUsage{}, "")

	// Simulate daemon disconnect -> Requeue in-flight queries (F21)
	requeued, err := st.RequeueInFlightQueries(ctx, pFrank.MemberID)
	if err != nil {
		t.Fatalf("RequeueInFlightQueries failed: %v", err)
	}
	if len(requeued) != 1 || requeued[0] != "q_fifo_1" {
		t.Fatalf("expected q_fifo_1 to be requeued, got %+v", requeued)
	}

	q1Check, _ := st.GetQuery(ctx, "q_fifo_1")
	if q1Check.Status != protocol.QueryStatusQueued {
		t.Errorf("expected q_fifo_1 status to be queued, got %s", q1Check.Status)
	}

	// Add an expired query
	qExpired := &protocol.QueryDetailResponse{
		QueryID:          "q_fifo_expired",
		Status:           protocol.QueryStatusQueued,
		AskerID:          "mem_other",
		AskerName:        "Asker",
		TargetMemberID:   pFrank.MemberID,
		TargetMemberName: "Frank",
		Query:            "I will expire",
		TTLExpiresAt:     time.Now().Add(-1 * time.Minute).UnixMilli(),
		CreatedAt:        time.Now().UnixMilli(),
	}
	_ = st.CreateQuery(ctx, qExpired)

	// Sweep expired queries (F19/F41)
	swept, err := st.SweepExpiredQueries(ctx)
	if err != nil {
		t.Fatalf("SweepExpiredQueries failed: %v", err)
	}
	if swept != 1 {
		t.Fatalf("expected 1 query swept, got %d", swept)
	}

	qExpiredCheck, _ := st.GetQuery(ctx, "q_fifo_expired")
	if qExpiredCheck.Status != protocol.QueryStatusExpired {
		t.Errorf("expected status expired, got %s", qExpiredCheck.Status)
	}
}

// TestIdempotencyKeys verifies F30 (query submission idempotency key lookup and persistence).
func TestIdempotencyKeys(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	st, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("NewJSONLStore failed: %v", err)
	}
	defer st.Close()

	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "Grace"})
	pGrace, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "host-g"})

	q := &protocol.QueryDetailResponse{
		QueryID:          "q_idemp_1",
		Status:           protocol.QueryStatusQueued,
		AskerID:          "mem_asker",
		AskerName:        "Asker",
		TargetMemberID:   pGrace.MemberID,
		TargetMemberName: "Grace",
		Query:            "Idempotent query",
		CreatedAt:        time.Now().UnixMilli(),
	}
	_ = st.CreateQuery(ctx, q)

	idempKey := "client_req_uuid_999"
	if err := st.SaveIdempotencyKey(ctx, idempKey, q.QueryID, time.Now().Add(1*time.Hour).UnixMilli()); err != nil {
		t.Fatalf("SaveIdempotencyKey failed: %v", err)
	}

	// Lookup by idempotency key
	found, err := st.GetQueryByIdempotencyKey(ctx, idempKey)
	if err != nil || found.QueryID != q.QueryID {
		t.Fatalf("GetQueryByIdempotencyKey failed: %v, found: %+v", err, found)
	}

	// Non-existent key
	_, err = st.GetQueryByIdempotencyKey(ctx, "non_existent_key")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for missing key, got %v", err)
	}
}

// TestAuditWithErrorMessage verifies F47 (audit logs surface error messages for failed queries).
func TestAuditWithErrorMessage(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	st, err := NewJSONLStore(tempDir)
	if err != nil {
		t.Fatalf("NewJSONLStore failed: %v", err)
	}
	defer st.Close()

	inv, _ := st.CreateInvite(ctx, &protocol.InviteCreateRequest{TargetName: "Hank"})
	pHank, _ := st.ConsumeInvite(ctx, &protocol.PairRequest{InviteCode: inv.Code, MachineName: "host-h"})

	// Create failed query with error message
	qFailed := &protocol.QueryDetailResponse{
		QueryID:          "q_err_1",
		Status:           protocol.QueryStatusError,
		AskerID:          "mem_asker",
		AskerName:        "Asker",
		TargetMemberID:   pHank.MemberID,
		TargetMemberName: "Hank",
		Query:            "Failing query",
		ErrorMessage:     "probe execution failed: LLM rate limited",
		CreatedAt:        time.Now().UnixMilli(),
	}
	_ = st.CreateQuery(ctx, qFailed)

	// Inbound audit check
	audit, err := st.GetInboundAudit(ctx, pHank.MemberID, 10, 0)
	if err != nil {
		t.Fatalf("GetInboundAudit failed: %v", err)
	}
	if len(audit.Entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(audit.Entries))
	}
	if !strings.Contains(audit.Entries[0].Answer, "LLM rate limited") {
		t.Errorf("expected ErrorMessage to be populated in Answer, got: %q", audit.Entries[0].Answer)
	}

	// Detailed audit check
	detailed, err := st.GetInboundAuditDetailed(ctx, pHank.MemberID, 10, 0)
	if err != nil {
		t.Fatalf("GetInboundAuditDetailed failed: %v", err)
	}
	if len(detailed.Entries) != 1 {
		t.Fatalf("expected 1 detailed audit entry, got %d", len(detailed.Entries))
	}
	if detailed.Entries[0].ErrorMessage != "probe execution failed: LLM rate limited" {
		t.Errorf("expected detailed ErrorMessage, got: %q", detailed.Entries[0].ErrorMessage)
	}
}
