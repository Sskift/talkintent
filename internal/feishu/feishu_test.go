package feishu

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestVerifySignature(t *testing.T) {
	timestamp := "1726828800"
	nonce := "random_nonce_123"
	encryptKey := "test_encrypt_key_32bytes_sample!"
	body := []byte(`{"test":"payload"}`)

	sig := CalculateSignature(timestamp, nonce, encryptKey, body)
	if len(sig) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(sig))
	}

	// Valid signature
	if !VerifySignature(timestamp, nonce, encryptKey, body, sig) {
		t.Errorf("expected signature to verify successfully")
	}

	// Tampered nonce
	if VerifySignature(timestamp, "tampered_nonce", encryptKey, body, sig) {
		t.Errorf("tampered nonce should fail signature verification")
	}

	// Tampered timestamp
	if VerifySignature("1726828801", nonce, encryptKey, body, sig) {
		t.Errorf("tampered timestamp should fail signature verification")
	}

	// Tampered body
	if VerifySignature(timestamp, nonce, encryptKey, []byte(`{"test":"tampered"}`), sig) {
		t.Errorf("tampered body should fail signature verification")
	}

	// Wrong key
	if VerifySignature(timestamp, nonce, "wrong_key", body, sig) {
		t.Errorf("wrong key should fail signature verification")
	}

	// Wrong length signature
	if VerifySignature(timestamp, nonce, encryptKey, body, "short_sig") {
		t.Errorf("short signature should fail verification")
	}
}

func TestVerifyTimestampFreshness(t *testing.T) {
	now := time.Now().Unix()

	// Current time: valid
	if !VerifyTimestampFreshness(strconv.FormatInt(now, 10), 300) {
		t.Errorf("expected current timestamp to be fresh")
	}

	// 100 seconds ago: valid within 300s window
	if !VerifyTimestampFreshness(strconv.FormatInt(now-100, 10), 300) {
		t.Errorf("expected timestamp 100s ago to be fresh")
	}

	// 301 seconds ago: expired
	if VerifyTimestampFreshness(strconv.FormatInt(now-301, 10), 300) {
		t.Errorf("expected timestamp 301s ago to be expired")
	}

	// 30 seconds into the future (clock skew): allowed up to 60s
	if !VerifyTimestampFreshness(strconv.FormatInt(now+30, 10), 300) {
		t.Errorf("expected timestamp 30s in future to be accepted")
	}

	// 65 seconds into the future: rejected
	if VerifyTimestampFreshness(strconv.FormatInt(now+65, 10), 300) {
		t.Errorf("expected timestamp 65s in future to be rejected")
	}

	// Millisecond timestamp support (13 digits)
	nowMilli := time.Now().UnixMilli()
	if !VerifyTimestampFreshness(strconv.FormatInt(nowMilli, 10), 300) {
		t.Errorf("expected millisecond timestamp to be accepted")
	}

	// Malformed timestamp string
	if VerifyTimestampFreshness("not-a-number", 300) {
		t.Errorf("expected invalid timestamp string to be rejected")
	}
}

func TestEncryptAndDecryptPayload(t *testing.T) {
	encryptKey := "my_super_secret_feishu_key_123!"

	tests := []struct {
		name  string
		plain string
	}{
		{"empty string", ""},
		{"short message", "hello world"},
		{"exact 16 bytes", "1234567890123456"},
		{"exact 32 bytes", "12345678901234561234567890123456"},
		{"json event", `{"challenge":"xyz123","token":"tok_abc","type":"url_verification"}`},
		{"chinese characters", "张三正在重构登录模块，目前进度约80%"},
		{"large payload", strings.Repeat("TalkIntent probe agent sandbox testing; ", 100)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encrypted, err := EncryptPayload([]byte(tc.plain), encryptKey)
			if err != nil {
				t.Fatalf("EncryptPayload failed: %v", err)
			}
			if encrypted == "" {
				t.Fatalf("encrypted string should not be empty")
			}

			decrypted, err := DecryptPayload(encrypted, encryptKey)
			if err != nil {
				t.Fatalf("DecryptPayload failed: %v", err)
			}
			if string(decrypted) != tc.plain {
				t.Fatalf("decrypted text mismatch: got %q, want %q", string(decrypted), tc.plain)
			}
		})
	}
}

func TestDecryptPayloadErrors(t *testing.T) {
	encryptKey := "valid_test_key_12345"

	// Empty key
	if _, err := EncryptPayload([]byte("hello"), ""); err == nil {
		t.Errorf("expected error with empty encryptKey in EncryptPayload")
	}
	if _, err := DecryptPayload("abc", ""); err == nil {
		t.Errorf("expected error with empty encryptKey in DecryptPayload")
	}

	// Invalid base64
	if _, err := DecryptPayload("not_base64!@#$", encryptKey); err == nil {
		t.Errorf("expected error on invalid base64")
	}

	// Ciphertext too short (< 16 bytes)
	if _, err := DecryptPayload("AAAA", encryptKey); err == nil {
		t.Errorf("expected error on ciphertext too short")
	}

	// Ciphertext not multiple of block size (16 bytes)
	// 20 bytes in base64:
	b20 := "AAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if _, err := DecryptPayload(b20, encryptKey); err == nil {
		t.Errorf("expected error on ciphertext not multiple of block size")
	}

	// Decrypt with wrong key should fail padding verification
	enc, err := EncryptPayload([]byte("test payload for wrong key"), encryptKey)
	if err != nil {
		t.Fatalf("EncryptPayload failed: %v", err)
	}
	if _, err := DecryptPayload(enc, "wrong_encrypt_key_9999"); err == nil {
		t.Errorf("expected decryption to fail with wrong key")
	}
}

func TestDecryptPayloadIVSpecificationF31(t *testing.T) {
	// Verify that DecryptPayload adheres to F31:
	// IV is the first 16 bytes of SHA-256(encryptKey), NOT sliced from ciphertext.
	encryptKey := "feishu_compliance_key_test"
	plain := []byte("feishu F31 verification test payload")

	encrypted, err := EncryptPayload(plain, encryptKey)
	if err != nil {
		t.Fatalf("EncryptPayload failed: %v", err)
	}

	keyHash := sha256.Sum256([]byte(encryptKey))
	expectedIV := keyHash[:16]

	// Decrypt and ensure plain matches
	decrypted, err := DecryptPayload(encrypted, encryptKey)
	if err != nil {
		t.Fatalf("DecryptPayload failed: %v", err)
	}
	if string(decrypted) != string(plain) {
		t.Fatalf("decrypted mismatch: got %q, want %q", string(decrypted), string(plain))
	}

	// Ensure IV is indeed 16 bytes
	if len(expectedIV) != 16 {
		t.Fatalf("expected 16 byte IV, got %d", len(expectedIV))
	}
}

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
	// DESIGN.md §6: In case of 400 invalid token, evicts cache and re-fetches
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
