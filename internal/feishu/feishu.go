package feishu

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client handles interaction with the Feishu Open Platform APIs.
type Client struct {
	appID      string
	appSecret  string
	baseURL    string
	httpClient *http.Client

	mu        sync.RWMutex
	token     string
	expiresAt time.Time
}

// NewClient creates a new Feishu API client.
func NewClient(appID, appSecret, baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://open.feishu.cn"
	}
	return &Client{
		appID:      appID,
		appSecret:  appSecret,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// SetHTTPClient overrides the default HTTP client (e.g. for testing).
func (c *Client) SetHTTPClient(client *http.Client) {
	if client != nil {
		c.httpClient = client
	}
}

// EvictToken explicitly invalidates the cached tenant_access_token.
func (c *Client) EvictToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.expiresAt = time.Time{}
}

// GetTenantAccessToken retrieves or reuses a cached tenant_access_token.
// Automatically refreshes token 5 minutes before expiry (DESIGN.md §6).
func (c *Client) GetTenantAccessToken(ctx context.Context) (string, error) {
	c.mu.RLock()
	if c.token != "" && time.Now().Before(c.expiresAt.Add(-5*time.Minute)) {
		t := c.token
		c.mu.RUnlock()
		return t, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.expiresAt.Add(-5*time.Minute)) {
		return c.token, nil
	}

	url := c.baseURL + "/open-apis/auth/v3/tenant_access_token/internal"
	reqBody := map[string]string{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal token request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request token failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parse token response failed: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("feishu token api error: %s (code %d)", result.Msg, result.Code)
	}
	if result.TenantAccessToken == "" {
		return "", errors.New("empty tenant_access_token returned by feishu")
	}

	c.token = result.TenantAccessToken
	c.expiresAt = time.Now().Add(time.Duration(result.Expire) * time.Second)
	return c.token, nil
}

// ReplyMessage sends an IM reply back to a Feishu chat or thread.
// Implements token refresh on 400/401 invalid token per DESIGN.md §6.
func (c *Client) ReplyMessage(ctx context.Context, messageID, replyContent string) error {
	contentObj := map[string]string{"text": replyContent}
	contentBytes, err := json.Marshal(contentObj)
	if err != nil {
		return fmt.Errorf("marshal reply content failed: %w", err)
	}

	payload := map[string]string{
		"content":  string(contentBytes),
		"msg_type": "text",
	}
	url := fmt.Sprintf("%s/open-apis/im/v1/messages/%s/reply", c.baseURL, messageID)
	return c.doPostWithTokenRetry(ctx, url, payload)
}

// ReplyTextMessage sends a plain text reply to a Feishu message.
func (c *Client) ReplyTextMessage(ctx context.Context, messageID, text string) error {
	return c.ReplyMessage(ctx, messageID, text)
}

// SendMessage sends an IM message to a chat or user.
func (c *Client) SendMessage(ctx context.Context, receiveIDType, receiveID, msgType, content string) error {
	if receiveIDType == "" {
		receiveIDType = "open_id"
	}
	url := fmt.Sprintf("%s/open-apis/im/v1/messages?receive_id_type=%s", c.baseURL, receiveIDType)
	payload := map[string]string{
		"receive_id": receiveID,
		"msg_type":   msgType,
		"content":    content,
	}
	return c.doPostWithTokenRetry(ctx, url, payload)
}

// SendTextMessage sends a plain text message to a chat or user.
func (c *Client) SendTextMessage(ctx context.Context, receiveIDType, receiveID, text string) error {
	contentObj := map[string]string{"text": text}
	contentBytes, err := json.Marshal(contentObj)
	if err != nil {
		return fmt.Errorf("marshal message content failed: %w", err)
	}
	return c.SendMessage(ctx, receiveIDType, receiveID, "text", string(contentBytes))
}

// SendChatMessage sends a plain text message to a specific chat ID (group or p2p).
func (c *Client) SendChatMessage(ctx context.Context, chatID, text string) error {
	return c.SendTextMessage(ctx, "chat_id", chatID, text)
}

// SendUserMessage sends a plain text message directly to a user's OpenID.
func (c *Client) SendUserMessage(ctx context.Context, openID, text string) error {
	return c.SendTextMessage(ctx, "open_id", openID, text)
}

// isTokenError checks if an HTTP response or Feishu JSON body indicates an invalid/expired token.
func isTokenError(statusCode int, code int, msg string) bool {
	if statusCode == http.StatusUnauthorized {
		return true
	}
	// Common Feishu token error codes:
	// 99991663: tenant_access_token invalid
	// 99991664: tenant_access_token expired
	// 99991661: app token invalid
	// 99991668: token missing
	if code == 99991663 || code == 99991664 || code == 99991661 || code == 99991668 {
		return true
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "access_token") && (strings.Contains(lower, "invalid") || strings.Contains(lower, "expired")) {
		return true
	}
	return false
}

func (c *Client) doPostWithTokenRetry(ctx context.Context, url string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload failed: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.GetTenantAccessToken(ctx)
		if err != nil {
			return fmt.Errorf("get tenant access token failed: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json; charset=utf-8")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("http request failed: %w", err)
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response failed: %w", err)
		}

		var apiResp struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.Unmarshal(bodyBytes, &apiResp)

		if isTokenError(resp.StatusCode, apiResp.Code, apiResp.Msg) {
			// Evict token and retry once
			c.EvictToken()
			if attempt == 0 {
				continue
			}
		}

		if resp.StatusCode >= 400 {
			return fmt.Errorf("feishu api failed with status %d: %s", resp.StatusCode, string(bodyBytes))
		}
		if apiResp.Code != 0 {
			return fmt.Errorf("feishu api error code %d: %s", apiResp.Code, apiResp.Msg)
		}

		return nil
	}

	return errors.New("request failed after token refresh retry")
}

// CalculateSignature computes the Feishu signature:
// SHA256(timestamp + nonce + encryptKey + rawBody)
func CalculateSignature(timestamp, nonce, encryptKey string, rawBody []byte) string {
	h := sha256.New()
	h.Write([]byte(timestamp))
	h.Write([]byte(nonce))
	h.Write([]byte(encryptKey))
	h.Write(rawBody)
	return hex.EncodeToString(h.Sum(nil))
}

// VerifySignature validates the Feishu signature against timestamp, nonce, key, and raw body
// using constant-time comparison (F12).
func VerifySignature(timestamp, nonce, encryptKey string, rawBody []byte, expectedSig string) bool {
	calculated := CalculateSignature(timestamp, nonce, encryptKey, rawBody)
	if len(calculated) != len(expectedSig) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(calculated), []byte(expectedSig)) == 1
}

// VerifyTimestampFreshness checks if a timestamp is within maxAgeSeconds (default: 300s) of now (F12).
// Accepts both seconds (10 digits) and milliseconds (13 digits).
func VerifyTimestampFreshness(timestampStr string, maxAgeSeconds int64) bool {
	if maxAgeSeconds <= 0 {
		maxAgeSeconds = 300
	}
	ts, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return false
	}
	// Defensively handle millisecond timestamps
	if ts > 1e11 {
		ts = ts / 1000
	}
	now := time.Now().Unix()
	diff := now - ts
	if diff < -60 || diff > maxAgeSeconds {
		return false
	}
	return true
}

// EncryptPayload encrypts plaintext using AES-256-CBC according to Feishu specification:
// Key is SHA-256(encryptKey), IV is the first 16 bytes of keyHash, PKCS#7 padded, base64 encoded.
func EncryptPayload(plain []byte, encryptKey string) (string, error) {
	if encryptKey == "" {
		return "", errors.New("encryptKey cannot be empty")
	}

	keyHash := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return "", fmt.Errorf("create cipher failed: %w", err)
	}

	blockSize := aes.BlockSize
	paddingLen := blockSize - (len(plain) % blockSize)
	padText := bytes.Repeat([]byte{byte(paddingLen)}, paddingLen)
	padded := append(plain, padText...)

	iv := keyHash[:blockSize]
	mode := cipher.NewCBCEncrypter(block, iv)
	ciphertext := make([]byte, len(padded))
	mode.CryptBlocks(ciphertext, padded)

	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptPayload decrypts AES-CBC-256 payload according to Feishu specification (F31):
// IV is the first 16 bytes of SHA-256(encryptKey), and encryptedBase64 is pure ciphertext.
func DecryptPayload(encryptedBase64, encryptKey string) ([]byte, error) {
	if encryptKey == "" {
		return nil, errors.New("encryptKey cannot be empty")
	}

	cipherData, err := base64.StdEncoding.DecodeString(encryptedBase64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode failed: %w", err)
	}

	keyHash := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return nil, fmt.Errorf("create cipher failed: %w", err)
	}

	if len(cipherData) < aes.BlockSize {
		return nil, errors.New("ciphertext too short")
	}

	if len(cipherData)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext is not a multiple of the block size")
	}

	iv := keyHash[:aes.BlockSize]

	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(cipherData))
	mode.CryptBlocks(plain, cipherData)

	// PKCS#7 unpadding with strict byte verification
	length := len(plain)
	if length == 0 {
		return nil, errors.New("empty plain text")
	}
	padding := int(plain[length-1])
	if padding < 1 || padding > aes.BlockSize || padding > length {
		return nil, errors.New("invalid padding")
	}
	for i := 0; i < padding; i++ {
		if plain[length-1-i] != byte(padding) {
			return nil, errors.New("invalid padding bytes")
		}
	}

	return plain[:length-padding], nil
}
