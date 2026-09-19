package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sskift/talkintent/internal/protocol"
)

func TestHandlerPlainChallenge(t *testing.T) {
	bindingStore := map[string]*protocol.FeishuBindingRequest{
		"mem_zhangsan": {
			AppID:             "cli_test_zhangsan",
			AppSecret:         "sec_test_secret",
			VerificationToken: "ver_test_tok",
			EncryptKey:        "test_enc_key_12345",
		},
	}

	handler := NewHandler(HandlerConfig{
		BindingLookup: func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
			b, ok := bindingStore[memberID]
			if !ok {
				return nil, errors.New("not found")
			}
			return b, nil
		},
	})

	challengeBody := map[string]string{
		"challenge": "challenge_token_plain_999",
		"token":     "ver_test_tok",
		"type":      "url_verification",
	}
	bodyBytes, _ := json.Marshal(challengeBody)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_zhangsan", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if resp["challenge"] != "challenge_token_plain_999" {
		t.Fatalf("expected challenge challenge_token_plain_999, got %s", resp["challenge"])
	}
}

func TestHandlerEncryptedChallenge(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	encryptKey := "enc_key_for_challenge_test!"
	bindingStore := map[string]*protocol.FeishuBindingRequest{
		"mem_zhangsan": {
			AppID:             "cli_test_zhangsan",
			AppSecret:         "sec_test_secret",
			VerificationToken: "ver_test_tok",
			EncryptKey:        encryptKey,
		},
	}

	handler := NewHandler(HandlerConfig{
		BaseURL: fake.URL(),
		BindingLookup: func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
			b, ok := bindingStore[memberID]
			if !ok {
				return nil, errors.New("not found")
			}
			return b, nil
		},
	})

	plainChallenge, err := fake.BuildChallengeEvent("challenge_enc_888", "ver_test_tok")
	if err != nil {
		t.Fatalf("BuildChallengeEvent failed: %v", err)
	}

	req, err := fake.BuildEncryptedSignedRequest("/api/v1/feishu/webhook/mem_zhangsan", encryptKey, plainChallenge)
	if err != nil {
		t.Fatalf("BuildEncryptedSignedRequest failed: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode challenge response failed: %v", err)
	}
	if resp["challenge"] != "challenge_enc_888" {
		t.Fatalf("expected plain challenge challenge_enc_888, got %s", resp["challenge"])
	}
}

func TestHandlerSignatureAndTimestampVerification(t *testing.T) {
	encryptKey := "key_for_signature_verification!"
	bindingStore := map[string]*protocol.FeishuBindingRequest{
		"mem_zhangsan": {
			AppID:      "cli_test",
			AppSecret:  "sec_test",
			EncryptKey: encryptKey,
		},
	}

	handler := NewHandler(HandlerConfig{
		BindingLookup: func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
			return bindingStore[memberID], nil
		},
		MaxSkewSeconds: 300,
	})

	body := []byte(`{"type":"url_verification","challenge":"ch123"}`)

	// 1. Valid Signature
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "test_nonce"
	sig := CalculateSignature(ts, nonce, encryptKey, body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_zhangsan", bytes.NewReader(body))
	req.Header.Set("X-Lark-Request-Timestamp", ts)
	req.Header.Set("X-Lark-Request-Nonce", nonce)
	req.Header.Set("X-Lark-Signature", sig)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid signature should pass with 200, got %d", rec.Code)
	}

	// 2. Tampered signature
	req = httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_zhangsan", bytes.NewReader(body))
	req.Header.Set("X-Lark-Request-Timestamp", ts)
	req.Header.Set("X-Lark-Request-Nonce", nonce)
	req.Header.Set("X-Lark-Signature", "tampered_signature_hex_000000000000000000000000000000000000000000000")

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered signature should return 401, got %d", rec.Code)
	}

	// 3. Expired timestamp (> 300s)
	expiredTS := strconv.FormatInt(time.Now().Unix()-350, 10)
	expiredSig := CalculateSignature(expiredTS, nonce, encryptKey, body)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_zhangsan", bytes.NewReader(body))
	req.Header.Set("X-Lark-Request-Timestamp", expiredTS)
	req.Header.Set("X-Lark-Request-Nonce", nonce)
	req.Header.Set("X-Lark-Signature", expiredSig)

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired timestamp should return 401, got %d", rec.Code)
	}
}

func TestHandlerMessageEventDispatchAndAck(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	encryptKey := "enc_key_for_message_dispatch!"
	bindingStore := map[string]*protocol.FeishuBindingRequest{
		"mem_zhangsan": {
			AppID:      "cli_zhangsan_bot",
			AppSecret:  "sec_zhangsan_bot",
			EncryptKey: encryptKey,
		},
	}

	var dispatchedTarget string
	var dispatchedAsker AskerInfo
	var dispatchedQuery string
	var dispatchedFeishuCtx protocol.FeishuContext
	dispatchCalled := make(chan struct{}, 1)

	handler := NewHandler(HandlerConfig{
		BaseURL: fake.URL(),
		BindingLookup: func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
			return bindingStore[memberID], nil
		},
		Dispatch: func(ctx context.Context, targetMemberID string, asker AskerInfo, queryText string, feishuCtx protocol.FeishuContext) (*DispatchResult, error) {
			dispatchedTarget = targetMemberID
			dispatchedAsker = asker
			dispatchedQuery = queryText
			dispatchedFeishuCtx = feishuCtx
			dispatchCalled <- struct{}{}
			return &DispatchResult{
				QueryID: "qry_01J8DISPATCH",
				Status:  protocol.QueryStatusDispatched,
			}, nil
		},
	})

	// Construct message event with @mention
	rawText := "@_user_1 查一下登录重构进展"
	eventJSON, err := fake.BuildMessageReceiveEvent("evt_001", "om_msg_001", "oc_chat_001", "ou_asker_001", rawText)
	if err != nil {
		t.Fatalf("BuildMessageReceiveEvent failed: %v", err)
	}

	req, err := fake.BuildEncryptedSignedRequest("/api/v1/feishu/webhook/mem_zhangsan", encryptKey, eventJSON)
	if err != nil {
		t.Fatalf("BuildEncryptedSignedRequest failed: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Feishu webhook must return 200 OK immediately
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "{}" {
		t.Fatalf("expected empty json ack {}, got %s", rec.Body.String())
	}

	// Verify asynchronous dispatch was invoked
	select {
	case <-dispatchCalled:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for dispatch callback to be called")
	}

	if dispatchedTarget != "mem_zhangsan" {
		t.Errorf("dispatched target: got %s, want mem_zhangsan", dispatchedTarget)
	}
	if dispatchedAsker.OpenID != "ou_asker_001" || dispatchedAsker.ChatID != "oc_chat_001" {
		t.Errorf("unexpected asker: %+v", dispatchedAsker)
	}
	if dispatchedQuery != "查一下登录重构进展" {
		t.Errorf("mention was not stripped: got %q, want %q", dispatchedQuery, "查一下登录重构进展")
	}
	if dispatchedFeishuCtx.MessageID != "om_msg_001" || dispatchedFeishuCtx.ChatID != "oc_chat_001" {
		t.Errorf("unexpected feishu context: %+v", dispatchedFeishuCtx)
	}
}

func TestHandlerDeduplication(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	bindingStore := map[string]*protocol.FeishuBindingRequest{
		"mem_zhangsan": {
			AppID:     "cli_zhangsan",
			AppSecret: "sec_zhangsan",
		},
	}

	var mu sync.Mutex
	dispatchCount := 0

	handler := NewHandler(HandlerConfig{
		BaseURL: fake.URL(),
		BindingLookup: func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
			return bindingStore[memberID], nil
		},
		Dispatch: func(ctx context.Context, targetMemberID string, asker AskerInfo, queryText string, feishuCtx protocol.FeishuContext) (*DispatchResult, error) {
			mu.Lock()
			dispatchCount++
			mu.Unlock()
			return &DispatchResult{QueryID: "qry_dedupe", Status: protocol.QueryStatusDispatched}, nil
		},
	})

	eventJSON, _ := fake.BuildMessageReceiveEvent("evt_dup_001", "om_dup_001", "oc_chat", "ou_user", "你好")

	// First delivery
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_zhangsan", bytes.NewReader(eventJSON))
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first delivery failed: %d", rec1.Code)
	}

	// Immediate re-delivery of identical event
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_zhangsan", bytes.NewReader(eventJSON))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second delivery failed: %d", rec2.Code)
	}

	// Give async dispatch goroutine time to run
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := dispatchCount
	mu.Unlock()

	if count != 1 {
		t.Fatalf("expected dispatch to be called exactly once due to deduplication, got %d", count)
	}
}

func TestHandlerOfflineQueuedAckF40(t *testing.T) {
	// F40 & WP5: When target member is offline and query is queued, send offline acknowledgment
	fake := NewFakeServer()
	defer fake.Close()

	bindingStore := map[string]*protocol.FeishuBindingRequest{
		"mem_lisi": {
			AppID:     "cli_lisi_bot",
			AppSecret: "sec_lisi_bot",
		},
	}

	handler := NewHandler(HandlerConfig{
		BaseURL: fake.URL(),
		BindingLookup: func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
			return bindingStore[memberID], nil
		},
		Dispatch: func(ctx context.Context, targetMemberID string, asker AskerInfo, queryText string, feishuCtx protocol.FeishuContext) (*DispatchResult, error) {
			// Target is offline!
			return &DispatchResult{
				QueryID:          "qry_queued_001",
				Status:           protocol.QueryStatusQueued,
				TargetMemberName: "李四",
				QueuePosition:    1,
			}, nil
		},
	})

	eventJSON, _ := fake.BuildMessageReceiveEvent("evt_offline_001", "om_offline_msg_001", "oc_chat_lisi", "ou_asker", "李四你在干啥")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_lisi", bytes.NewReader(eventJSON))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	// Wait for background offline ack to be sent
	var reply *RecordedReply
	for i := 0; i < 20; i++ {
		reply = fake.LastReply()
		if reply != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if reply == nil {
		t.Fatalf("expected offline ack reply to be sent to fake Feishu server")
	}
	if reply.MessageID != "om_offline_msg_001" {
		t.Errorf("reply message id got %s, want om_offline_msg_001", reply.MessageID)
	}
	if !strings.Contains(reply.Text, "李四") || !strings.Contains(reply.Text, "离线") {
		t.Errorf("unexpected offline notice text: %s", reply.Text)
	}
}

func TestHandlerOnQueryCompleteF40(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	bindingStore := map[string]*protocol.FeishuBindingRequest{
		"mem_zhangsan": {
			AppID:     "cli_zhangsan_bot",
			AppSecret: "sec_zhangsan_bot",
		},
	}

	handler := NewHandler(HandlerConfig{
		BaseURL: fake.URL(),
		BindingLookup: func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
			return bindingStore[memberID], nil
		},
	})

	ctx := context.Background()

	// 1. Completed query
	qCompleted := &protocol.QueryDetailResponse{
		QueryID:          "qry_comp_001",
		Status:           protocol.QueryStatusCompleted,
		TargetMemberID:   "mem_zhangsan",
		TargetMemberName: "张三",
		Answer:           "张三目前正在 feature/auth-v2 分支重构 JWT 校验器",
		FeishuContext: &protocol.FeishuContext{
			MessageID: "om_comp_msg_001",
			ChatID:    "oc_comp_chat_001",
			AppID:     "cli_zhangsan_bot",
		},
	}
	err := handler.OnQueryComplete(ctx, qCompleted)
	if err != nil {
		t.Fatalf("OnQueryComplete for completed query failed: %v", err)
	}
	lastReply := fake.LastReply()
	if lastReply == nil {
		t.Fatalf("expected reply recorded on fake server")
	}
	if lastReply.MessageID != "om_comp_msg_001" {
		t.Errorf("reply messageID mismatch: %s", lastReply.MessageID)
	}
	if !strings.Contains(lastReply.Text, "[TalkIntent 自动回答]") || !strings.Contains(lastReply.Text, "feature/auth-v2") {
		t.Errorf("unexpected reply body: %s", lastReply.Text)
	}

	// 2. Expired query notice
	fake.Reset()
	qExpired := &protocol.QueryDetailResponse{
		QueryID:          "qry_exp_001",
		Status:           protocol.QueryStatusExpired,
		TargetMemberID:   "mem_zhangsan",
		TargetMemberName: "张三",
		FeishuContext: &protocol.FeishuContext{
			MessageID: "om_exp_msg_001",
			ChatID:    "oc_comp_chat_001",
			AppID:     "cli_zhangsan_bot",
		},
	}
	err = handler.OnQueryComplete(ctx, qExpired)
	if err != nil {
		t.Fatalf("OnQueryComplete for expired query failed: %v", err)
	}
	lastReply = fake.LastReply()
	if lastReply == nil || !strings.Contains(lastReply.Text, "过期") {
		t.Errorf("unexpected expired reply: %+v", lastReply)
	}

	// 3. Refused query notice
	fake.Reset()
	qRefused := &protocol.QueryDetailResponse{
		QueryID:          "qry_ref_001",
		Status:           protocol.QueryStatusRefused,
		TargetMemberID:   "mem_zhangsan",
		TargetMemberName: "张三",
		FeishuContext: &protocol.FeishuContext{
			MessageID: "om_ref_msg_001",
		},
	}
	err = handler.OnQueryComplete(ctx, qRefused)
	if err != nil {
		t.Fatalf("OnQueryComplete for refused query failed: %v", err)
	}
	lastReply = fake.LastReply()
	if lastReply == nil || !strings.Contains(lastReply.Text, "拒绝") {
		t.Errorf("unexpected refused reply: %+v", lastReply)
	}

	// 4. Timeout query notice
	fake.Reset()
	qTimeout := &protocol.QueryDetailResponse{
		QueryID:          "qry_to_001",
		Status:           protocol.QueryStatusTimeout,
		TargetMemberID:   "mem_zhangsan",
		TargetMemberName: "张三",
		FeishuContext: &protocol.FeishuContext{
			MessageID: "om_to_msg_001",
		},
	}
	err = handler.OnQueryComplete(ctx, qTimeout)
	if err != nil {
		t.Fatalf("OnQueryComplete for timeout query failed: %v", err)
	}
	lastReply = fake.LastReply()
	if lastReply == nil || !strings.Contains(lastReply.Text, "超时") {
		t.Errorf("unexpected timeout reply: %+v", lastReply)
	}

	// 5. Error query notice
	fake.Reset()
	qError := &protocol.QueryDetailResponse{
		QueryID:          "qry_err_001",
		Status:           protocol.QueryStatusError,
		TargetMemberID:   "mem_zhangsan",
		TargetMemberName: "张三",
		ErrorMessage:     "LLM provider 503 unavailable",
		FeishuContext: &protocol.FeishuContext{
			MessageID: "om_err_msg_001",
		},
	}
	err = handler.OnQueryComplete(ctx, qError)
	if err != nil {
		t.Fatalf("OnQueryComplete for error query failed: %v", err)
	}
	lastReply = fake.LastReply()
	if lastReply == nil || !strings.Contains(lastReply.Text, "LLM provider 503 unavailable") {
		t.Errorf("unexpected error reply: %+v", lastReply)
	}

	// 6. Query with no Feishu context (e.g. from CLI or REST): no-op
	fake.Reset()
	qNonFeishu := &protocol.QueryDetailResponse{
		QueryID:        "qry_cli_001",
		Status:         protocol.QueryStatusCompleted,
		TargetMemberID: "mem_zhangsan",
		Answer:         "CLI answer",
	}
	err = handler.OnQueryComplete(ctx, qNonFeishu)
	if err != nil {
		t.Fatalf("OnQueryComplete for non-feishu query returned error: %v", err)
	}
	if len(fake.Replies()) != 0 {
		t.Errorf("non-feishu query should not trigger feishu replies")
	}
}

func TestHandlerBotSenderAndNonTextMessageIgnored(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	bindingStore := map[string]*protocol.FeishuBindingRequest{
		"mem_zhangsan": {AppID: "cli_zhangsan", AppSecret: "sec_zhangsan"},
	}

	dispatched := false
	handler := NewHandler(HandlerConfig{
		BaseURL: fake.URL(),
		BindingLookup: func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
			return bindingStore[memberID], nil
		},
		Dispatch: func(ctx context.Context, targetMemberID string, asker AskerInfo, queryText string, feishuCtx protocol.FeishuContext) (*DispatchResult, error) {
			dispatched = true
			return &DispatchResult{QueryID: "qry_none", Status: "dispatched"}, nil
		},
	})

	// 1. Message from bot/app sender
	botEvent := EventEnvelope{
		Schema: "2.0",
		Header: &EventHeader{EventID: "evt_bot_001", EventType: "im.message.receive_v1"},
		Event: &EventBody{
			Sender:  &EventSender{SenderType: "app"},
			Message: &EventMessage{MessageID: "om_bot", MessageType: "text", Content: `{"text":"bot message"}`},
		},
	}
	b, _ := json.Marshal(botEvent)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_zhangsan", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if dispatched {
		t.Errorf("message from app/bot sender should be ignored")
	}

	// 2. Non-text message (e.g. image)
	dispatched = false
	imgEvent := EventEnvelope{
		Schema: "2.0",
		Header: &EventHeader{EventID: "evt_img_001", EventType: "im.message.receive_v1"},
		Event: &EventBody{
			Sender:  &EventSender{SenderType: "user"},
			Message: &EventMessage{MessageID: "om_img", MessageType: "image", Content: `{"image_key":"img_123"}`},
		},
	}
	b, _ = json.Marshal(imgEvent)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_zhangsan", bytes.NewReader(b))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if dispatched {
		t.Errorf("non-text message should be ignored")
	}
}

func TestHandlerEdgeCases(t *testing.T) {
	handler := NewHandler(HandlerConfig{
		BindingLookup: func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
			if memberID == "mem_exists" {
				return &protocol.FeishuBindingRequest{AppID: "cli_123", AppSecret: "sec_123"}, nil
			}
			return nil, errors.New("not found")
		},
	})

	// GET method not allowed
	req := httptest.NewRequest(http.MethodGet, "/api/v1/feishu/webhook/mem_exists", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", rec.Code)
	}

	// Missing member ID
	req = httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/", bytes.NewReader([]byte("{}")))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for missing member ID, got %d", rec.Code)
	}

	// Member binding not found
	req = httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_unknown", bytes.NewReader([]byte("{}")))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 Not Found for unknown member, got %d", rec.Code)
	}

	// Malformed JSON
	req = httptest.NewRequest(http.MethodPost, "/api/v1/feishu/webhook/mem_exists", bytes.NewReader([]byte("not-json")))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for invalid json, got %d", rec.Code)
	}
}
