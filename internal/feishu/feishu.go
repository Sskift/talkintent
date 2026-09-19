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
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// GetTenantAccessToken retrieves or reuses a cached tenant_access_token.
func (c *Client) GetTenantAccessToken(ctx context.Context) (string, error) {
	c.mu.RLock()
	if c.token != "" && time.Now().Before(c.expiresAt.Add(-2*time.Minute)) {
		t := c.token
		c.mu.RUnlock()
		return t, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.expiresAt.Add(-2*time.Minute)) {
		return c.token, nil
	}

	url := c.baseURL + "/open-apis/auth/v3/tenant_access_token/internal"
	reqBody := map[string]string{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	}
	b, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
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

	c.token = result.TenantAccessToken
	c.expiresAt = time.Now().Add(time.Duration(result.Expire) * time.Second)
	return c.token, nil
}

// ReplyMessage sends an IM reply back to a Feishu chat or thread.
func (c *Client) ReplyMessage(ctx context.Context, messageID, replyContent string) error {
	token, err := c.GetTenantAccessToken(ctx)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/open-apis/im/v1/messages/%s/reply", c.baseURL, messageID)
	contentObj := map[string]string{"text": replyContent}
	contentBytes, _ := json.Marshal(contentObj)

	payload := map[string]string{
		"content":  string(contentBytes),
		"msg_type": "text",
	}
	b, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("reply failed with status %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}

// VerifySignature validates the Feishu signature against timestamp, nonce, key, and raw body
// using constant-time comparison.
func VerifySignature(timestamp, nonce, encryptKey string, rawBody []byte, expectedSig string) bool {
	h := sha256.New()
	h.Write([]byte(timestamp))
	h.Write([]byte(nonce))
	h.Write([]byte(encryptKey))
	h.Write(rawBody)
	calculated := hex.EncodeToString(h.Sum(nil))
	if len(calculated) != len(expectedSig) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(calculated), []byte(expectedSig)) == 1
}

// VerifyTimestampFreshness checks if a timestamp is within maxAgeSeconds (default: 300s) of now.
func VerifyTimestampFreshness(timestampStr string, maxAgeSeconds int64) bool {
	if maxAgeSeconds <= 0 {
		maxAgeSeconds = 300
	}
	ts, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	diff := now - ts
	if diff < -60 || diff > maxAgeSeconds {
		return false
	}
	return true
}

// DecryptPayload decrypts AES-CBC-256 payload according to Feishu specification:
// IV is the first 16 bytes of SHA-256(encryptKey), and encryptedBase64 is pure ciphertext.
func DecryptPayload(encryptedBase64, encryptKey string) ([]byte, error) {
	cipherData, err := base64.StdEncoding.DecodeString(encryptedBase64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode failed: %w", err)
	}

	keyHash := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return nil, err
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

	// PKCS#7 unpadding
	length := len(plain)
	if length == 0 {
		return nil, errors.New("empty plain text")
	}
	padding := int(plain[length-1])
	if padding < 1 || padding > aes.BlockSize || padding > length {
		return nil, errors.New("invalid padding")
	}

	return plain[:length-padding], nil
}
