package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStripMentions(t *testing.T) {
	tests := []struct {
		name        string
		contentJSON string
		mentions    []Mention
		expected    string
	}{
		{
			name:        "mention at start",
			contentJSON: `{"text":"@_user_1 现在登录模块进展如何？"}`,
			mentions:    []Mention{{Key: "@_user_1", Name: "TalkIntent Bot"}},
			expected:    "现在登录模块进展如何？",
		},
		{
			name:        "mention by bot name",
			contentJSON: `{"text":"@TalkIntent Bot 请汇报一下当前工作"}`,
			mentions:    []Mention{{Key: "@_user_1", Name: "TalkIntent Bot"}},
			expected:    "请汇报一下当前工作",
		},
		{
			name:        "multiple mentions",
			contentJSON: `{"text":"@_user_1 @_user_2 张三在哪个分支？"}`,
			mentions: []Mention{
				{Key: "@_user_1", Name: "Bot1"},
				{Key: "@_user_2", Name: "Bot2"},
			},
			expected: "张三在哪个分支？",
		},
		{
			name:        "no mentions",
			contentJSON: `{"text":"直接查询李四的工作进度"}`,
			mentions:    nil,
			expected:    "直接查询李四的工作进度",
		},
		{
			name:        "only mention",
			contentJSON: `{"text":"@_user_1"}`,
			mentions:    []Mention{{Key: "@_user_1", Name: "Bot"}},
			expected:    "",
		},
		{
			name:        "plain text non-json fallback",
			contentJSON: `简单的纯文本提问`,
			mentions:    nil,
			expected:    "简单的纯文本提问",
		},
		{
			name:        "escaped quotes in json",
			contentJSON: `{"text":"@_user_1 查看 \"auth-v2\" 分支的最近提交"}`,
			mentions:    []Mention{{Key: "@_user_1", Name: "Bot"}},
			expected:    `查看 "auth-v2" 分支的最近提交`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := StripMentions(tc.contentJSON, tc.mentions)
			if err != nil {
				t.Fatalf("StripMentions returned unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Errorf("got %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestDeduplicator(t *testing.T) {
	d := NewDeduplicator(100*time.Millisecond, 5)

	// First time seeing ID
	if d.CheckAndRecord("evt_001") {
		t.Errorf("first check should return false (not duplicate)")
	}

	// Immediate second check should report duplicate
	if !d.CheckAndRecord("evt_001") {
		t.Errorf("second check should return true (duplicate)")
	}

	// Different ID is not duplicate
	if d.CheckAndRecord("evt_002") {
		t.Errorf("new id should return false")
	}

	// Empty ID ignored
	if d.CheckAndRecord("") {
		t.Errorf("empty ID should return false")
	}

	// Wait for TTL expiration
	time.Sleep(150 * time.Millisecond)
	if d.CheckAndRecord("evt_001") {
		t.Errorf("after TTL expiration, ID should no longer be duplicate")
	}

	// Test capacity eviction
	d.Clear()
	for i := 0; i < 10; i++ {
		d.CheckAndRecord(strconv.Itoa(i))
	}
	if d.Size() > 5 {
		t.Errorf("deduplicator should bound size to maxSize, got %d", d.Size())
	}
}

func TestClientWithFakeServer(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	client := NewClient("cli_test_app_id", "sec_test_app_secret", fake.URL())

	// 1. Get Tenant Access Token
	ctx := context.Background()
	token, err := client.GetTenantAccessToken(ctx)
	if err != nil {
		t.Fatalf("GetTenantAccessToken failed: %v", err)
	}
	if token != "mock_tenant_access_token_default" {
		t.Fatalf("unexpected token: %s", token)
	}

	// 2. Token should be cached
	fake.SetToken("new_token_should_not_be_fetched_yet")
	cachedToken, err := client.GetTenantAccessToken(ctx)
	if err != nil {
		t.Fatalf("GetTenantAccessToken failed: %v", err)
	}
	if cachedToken != token {
		t.Fatalf("expected cached token %s, got %s", token, cachedToken)
	}

	// 3. Evict token and fetch new one
	client.EvictToken()
	newToken, err := client.GetTenantAccessToken(ctx)
	if err != nil {
		t.Fatalf("GetTenantAccessToken failed: %v", err)
	}
	if newToken != "new_token_should_not_be_fetched_yet" {
		t.Fatalf("expected new token, got %s", newToken)
	}

	// 4. ReplyMessage
	err = client.ReplyMessage(ctx, "om_test_msg_001", "这是测试回复内容")
	if err != nil {
		t.Fatalf("ReplyMessage failed: %v", err)
	}
	replies := fake.Replies()
	if len(replies) != 1 {
		t.Fatalf("expected 1 reply recorded, got %d", len(replies))
	}
	if replies[0].MessageID != "om_test_msg_001" {
		t.Errorf("reply messageID got %s, want om_test_msg_001", replies[0].MessageID)
	}
	if replies[0].Text != "这是测试回复内容" {
		t.Errorf("reply text got %s, want 这是测试回复内容", replies[0].Text)
	}

	// 5. SendTextMessage to chat
	err = client.SendChatMessage(ctx, "oc_chat_999", "直接向群聊发消息")
	if err != nil {
		t.Fatalf("SendChatMessage failed: %v", err)
	}
	lastMsg := fake.LastMessage()
	if lastMsg == nil {
		t.Fatalf("expected message to be recorded")
	}
	if lastMsg.ReceiveIDType != "chat_id" || lastMsg.ReceiveID != "oc_chat_999" {
		t.Errorf("unexpected receive target: %s=%s", lastMsg.ReceiveIDType, lastMsg.ReceiveID)
	}
	if lastMsg.Text != "直接向群聊发消息" {
		t.Errorf("unexpected message text: %s", lastMsg.Text)
	}

	// 6. SendUserMessage to user open_id
	err = client.SendUserMessage(ctx, "ou_user_888", "私聊消息")
	if err != nil {
		t.Fatalf("SendUserMessage failed: %v", err)
	}
	lastMsg = fake.LastMessage()
	if lastMsg == nil || lastMsg.ReceiveID != "ou_user_888" {
		t.Errorf("unexpected user message: %+v", lastMsg)
	}
}

func TestClientTokenRefreshRetryOn400(t *testing.T) {
	fake := NewFakeServer()
	defer fake.Close()

	client := NewClient("cli_retry_app", "sec_retry", fake.URL())
	ctx := context.Background()

	// Initial token
	_, err := client.GetTenantAccessToken(ctx)
	if err != nil {
		t.Fatalf("initial token fetch failed: %v", err)
	}

	// Simulate reply failure with 401 Unauthorized (token invalid)
	fake.SimulateReplyError(http.StatusUnauthorized, `{"code":99991663,"msg":"tenant_access_token invalid"}`)

	// Call reply - should fail because both initial and retry encountered 401
	err = client.ReplyMessage(ctx, "om_fail_msg", "test")
	if err == nil {
		t.Errorf("expected reply to fail when error persists")
	}

	// Now simulate error only on first call, clear it on subsequent call
	fake.Reset()
	callCount := 0
	client.httpClient = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "/reply") {
				callCount++
				if callCount == 1 {
					// First attempt returns token invalid
					rec := httptest.NewRecorder()
					rec.WriteHeader(http.StatusUnauthorized)
					rec.Body.WriteString(`{"code":99991663,"msg":"tenant_access_token expired"}`)
					return rec.Result(), nil
				}
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
	}

	err = client.ReplyMessage(ctx, "om_retry_msg", "retry successful")
	if err != nil {
		t.Fatalf("ReplyMessage should succeed after token retry, got: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 attempts to reply endpoint, got %d", callCount)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
