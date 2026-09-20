package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sskift/talkintent/internal/protocol"
)

func TestHandlerMessageEventDispatchAndAck(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	bindingStore := map[string]*protocol.FeishuBindingRequest{
		"mem_zhangsan": {
			AppID:     "cli_zhangsan_bot",
			AppSecret: "sec_zhangsan_bot",
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

	if err := handler.ProcessEvent(context.Background(), "mem_zhangsan", eventJSON); err != nil {
		t.Fatalf("ProcessEvent failed: %v", err)
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
	if err := handler.ProcessEvent(context.Background(), "mem_zhangsan", eventJSON); err != nil {
		t.Fatalf("first delivery failed: %v", err)
	}

	// Immediate re-delivery of identical event
	if err := handler.ProcessEvent(context.Background(), "mem_zhangsan", eventJSON); err != nil {
		t.Fatalf("second delivery failed: %v", err)
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

func TestHandlerOfflineQueuedAck(t *testing.T) {
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
	if err := handler.ProcessEvent(context.Background(), "mem_lisi", eventJSON); err != nil {
		t.Fatalf("ProcessEvent failed: %v", err)
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

func TestHandlerOnQueryComplete(t *testing.T) {
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
}

func TestHandlerIgnoreNonTextMessageAndBot(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	dispatchCalled := false
	handler := NewHandler(HandlerConfig{
		BaseURL: fake.URL(),
		Dispatch: func(ctx context.Context, targetMemberID string, asker AskerInfo, queryText string, feishuCtx protocol.FeishuContext) (*DispatchResult, error) {
			dispatchCalled = true
			return nil, nil
		},
	})

	// 1. Sender is bot
	event := EventEnvelope{
		Header: &EventHeader{EventType: "im.message.receive_v1", EventID: "evt_bot"},
		Event: &EventBody{
			Sender:  &EventSender{SenderType: "app"},
			Message: &EventMessage{MessageType: "text", Content: `{"text":"hello"}`},
		},
	}
	data, _ := json.Marshal(event)
	_ = handler.ProcessEvent(context.Background(), "mem_1", data)

	// 2. Message type is image
	event2 := EventEnvelope{
		Header: &EventHeader{EventType: "im.message.receive_v1", EventID: "evt_img"},
		Event: &EventBody{
			Sender:  &EventSender{SenderType: "user"},
			Message: &EventMessage{MessageType: "image", Content: `{"image_key":"img_123"}`},
		},
	}
	data2, _ := json.Marshal(event2)
	_ = handler.ProcessEvent(context.Background(), "mem_1", data2)

	time.Sleep(50 * time.Millisecond)
	if dispatchCalled {
		t.Errorf("bot sender and non-text messages must not be dispatched")
	}
}

func TestHandlerClientCacheEviction(t *testing.T) {
	h := NewHandler(HandlerConfig{})
	b1 := &protocol.FeishuBindingRequest{
		AppID:     "cli_rot_1",
		AppSecret: "secret_old",
	}
	c1 := h.GetClient(b1)
	c1Repeat := h.GetClient(b1)
	if c1 != c1Repeat {
		t.Fatalf("expected same cached client for identical credentials")
	}

	// Rotate secret
	b2 := &protocol.FeishuBindingRequest{
		AppID:     "cli_rot_1",
		AppSecret: "secret_new",
	}
	c2 := h.GetClient(b2)
	if c1 == c2 {
		t.Fatalf("expected new client instance after secret rotation")
	}

	// EvictClient
	h.EvictClient("cli_rot_1")
	c3 := h.GetClient(b2)
	if c2 == c3 {
		t.Fatalf("expected new client instance after EvictClient")
	}
}
