package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Sskift/talkintent/internal/protocol"
)

// AskerInfo identifies who sent the message from Feishu.
type AskerInfo struct {
	OpenID   string `json:"open_id"`
	UserID   string `json:"user_id,omitempty"`
	UnionID  string `json:"union_id,omitempty"`
	ChatID   string `json:"chat_id"`
	ChatType string `json:"chat_type"` // "p2p", "group"
}

// DisplayName returns a human-readable identifier for audit logging.
func (a AskerInfo) DisplayName() string {
	if a.OpenID != "" {
		return fmt.Sprintf("Feishu user (%s)", a.OpenID)
	}
	if a.UserID != "" {
		return fmt.Sprintf("Feishu user (%s)", a.UserID)
	}
	return "Feishu user"
}

// DispatchResult holds the routing result returned by the Hub.
type DispatchResult struct {
	QueryID          string `json:"query_id"`
	Status           string `json:"status"` // "dispatched", "queued"
	TargetMemberName string `json:"target_member_name,omitempty"`
	QueuePosition    int    `json:"queue_position,omitempty"`
}

// DispatchFunc is the callback that submits a parsed query into the Hub's routing engine.
type DispatchFunc func(ctx context.Context, targetMemberID string, asker AskerInfo, queryText string, feishuCtx protocol.FeishuContext) (*DispatchResult, error)

// BindingLookupFunc looks up Feishu bot credentials for a target member.
type BindingLookupFunc func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error)

// HandlerConfig configures the Feishu webhook HTTP handler.
type HandlerConfig struct {
	// BindingLookup retrieves Feishu bot credentials for a member. Required.
	BindingLookup BindingLookupFunc

	// Dispatch routes the incoming query to the target member's daemon or offline queue. Required.
	Dispatch DispatchFunc

	// BaseURL overrides the Feishu Open Platform API base URL (useful for mock/fake servers).
	// Default: "https://open.feishu.cn".
	BaseURL string

	// PathPrefix is stripped from r.URL.Path to determine the member ID.
	// Default: "/api/v1/feishu/webhook/".
	PathPrefix string

	// MaxSkewSeconds is the maximum allowed timestamp difference for signature validation (F12).
	// Default: 300 seconds.
	MaxSkewSeconds int64

	// Deduplicator deduplicates incoming events and messages.
	// If nil, a default deduplicator is created.
	Deduplicator *Deduplicator

	// Logger receives diagnostic logs. If nil, slog.Default() is used.
	Logger *slog.Logger

	// ClientFactory allows injecting custom Client instances (e.g. for testing).
	ClientFactory func(appID, appSecret, baseURL string) *Client
}

// Handler handles Feishu webhook HTTP callbacks and provides asynchronous reply dispatch.
type Handler struct {
	cfg       HandlerConfig
	dedupe    *Deduplicator
	logger    *slog.Logger
	clientsMu sync.Mutex
	clients   map[string]*Client // keyed by appID
}

// NewHandler creates a new Feishu webhook handler.
func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.feishu.cn"
	}
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = "/api/v1/feishu/webhook/"
	}
	if cfg.MaxSkewSeconds <= 0 {
		cfg.MaxSkewSeconds = 300
	}
	dedupe := cfg.Deduplicator
	if dedupe == nil {
		dedupe = NewDeduplicator(30*time.Minute, 10000)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Handler{
		cfg:     cfg,
		dedupe:  dedupe,
		logger:  logger,
		clients: make(map[string]*Client),
	}
}

// Deduplicator returns the deduplication engine used by the handler.
func (h *Handler) Deduplicator() *Deduplicator {
	return h.dedupe
}

// GetClient retrieves or creates a Client for the specified credentials.
func (h *Handler) GetClient(binding *protocol.FeishuBindingRequest) *Client {
	if binding == nil {
		return nil
	}
	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()

	key := binding.AppID
	if client, ok := h.clients[key]; ok {
		return client
	}

	var client *Client
	if h.cfg.ClientFactory != nil {
		client = h.cfg.ClientFactory(binding.AppID, binding.AppSecret, h.cfg.BaseURL)
	} else {
		client = NewClient(binding.AppID, binding.AppSecret, h.cfg.BaseURL)
	}
	h.clients[key] = client
	return client
}

// ExtractMemberID resolves the target member ID from the request URL.
func (h *Handler) ExtractMemberID(r *http.Request) string {
	if q := r.URL.Query().Get("member_id"); q != "" {
		return strings.TrimSpace(q)
	}

	path := r.URL.Path
	if h.cfg.PathPrefix != "" && strings.HasPrefix(path, h.cfg.PathPrefix) {
		return strings.Trim(strings.TrimPrefix(path, h.cfg.PathPrefix), "/")
	}

	if idx := strings.Index(path, "/webhook/"); idx != -1 {
		return strings.Trim(path[idx+len("/webhook/"):], "/")
	}

	return strings.Trim(path, "/")
}

// ServeHTTP implements http.Handler for Feishu webhook subscriptions.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	memberID := h.ExtractMemberID(r)
	if memberID == "" {
		http.Error(w, "missing member id in webhook path", http.StatusBadRequest)
		return
	}

	if h.cfg.BindingLookup == nil {
		http.Error(w, "binding lookup not configured", http.StatusInternalServerError)
		return
	}

	binding, err := h.cfg.BindingLookup(r.Context(), memberID)
	if err != nil || binding == nil {
		http.Error(w, "feishu binding not found for member", http.StatusNotFound)
		return
	}

	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "read request body failed", http.StatusBadRequest)
		return
	}

	// 1. Signature and Timestamp Verification (F12)
	timestamp := r.Header.Get("X-Lark-Request-Timestamp")
	nonce := r.Header.Get("X-Lark-Request-Nonce")
	signature := r.Header.Get("X-Lark-Signature")

	if binding.EncryptKey != "" && signature != "" {
		if !VerifyTimestampFreshness(timestamp, h.cfg.MaxSkewSeconds) {
			http.Error(w, "expired timestamp", http.StatusUnauthorized)
			return
		}
		if !VerifySignature(timestamp, nonce, binding.EncryptKey, rawBody, signature) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// 2. Parse Envelope
	var env EventEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}

	// 3. Plain URL Verification Challenge (PROTOCOL.md §4.1)
	if env.Type == "url_verification" || (env.Challenge != "" && env.Encrypt == "") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": env.Challenge})
		return
	}

	// 4. Encrypted Payload Decryption (PROTOCOL.md §4.1, F31)
	if env.Encrypt != "" {
		if binding.EncryptKey == "" {
			http.Error(w, "encrypted payload received but no encrypt_key configured", http.StatusBadRequest)
			return
		}
		plainBytes, err := DecryptPayload(env.Encrypt, binding.EncryptKey)
		if err != nil {
			http.Error(w, "payload decryption failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Reset and unmarshal decrypted payload
		env = EventEnvelope{}
		if err := json.Unmarshal(plainBytes, &env); err != nil {
			http.Error(w, "invalid decrypted json payload", http.StatusBadRequest)
			return
		}

		// Encrypted URL Verification Challenge
		if env.Type == "url_verification" || env.Challenge != "" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": env.Challenge})
			return
		}
	}

	// 5. Verify Event Payload
	if env.Header == nil || env.Event == nil {
		// Acknowledge other non-event webhooks with 200 OK
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte("{}"))
		return
	}

	if env.Header.EventType != "im.message.receive_v1" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte("{}"))
		return
	}

	// Ignore messages sent by apps/bots to prevent reply loops
	if env.Event.Sender != nil && env.Event.Sender.SenderType != "" && env.Event.Sender.SenderType != "user" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte("{}"))
		return
	}

	msg := env.Event.Message
	if msg == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte("{}"))
		return
	}

	// 6. Deduplication by event_id and message_id
	eventID := env.Header.EventID
	msgID := msg.MessageID
	if (eventID != "" && h.dedupe.CheckAndRecord(eventID)) || (msgID != "" && h.dedupe.CheckAndRecord(msgID)) {
		// Duplicate event, acknowledge immediately without re-dispatching
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte("{}"))
		return
	}

	// Only text messages are supported
	if msg.MessageType != "text" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte("{}"))
		return
	}

	queryText, err := StripMentions(msg.Content, msg.Mentions)
	if err != nil || queryText == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte("{}"))
		return
	}

	// 7. Prepare Asker & Feishu Context
	asker := AskerInfo{
		ChatID:   msg.ChatID,
		ChatType: msg.ChatType,
	}
	if env.Event.Sender != nil && env.Event.Sender.SenderID != nil {
		asker.OpenID = env.Event.Sender.SenderID.OpenID
		asker.UserID = env.Event.Sender.SenderID.UserID
		asker.UnionID = env.Event.Sender.SenderID.UnionID
	}

	feishuCtx := protocol.FeishuContext{
		MessageID: msg.MessageID,
		ChatID:    msg.ChatID,
		AppID:     binding.AppID,
	}

	// Acknowledge Feishu immediately (Feishu requires response within 3s)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte("{}"))

	// 8. Asynchronous Dispatch to Hub Router
	if h.cfg.Dispatch != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			res, err := h.cfg.Dispatch(ctx, memberID, asker, queryText, feishuCtx)
			if err != nil {
				h.logger.Error("Feishu dispatch failed", "err", err, "member_id", memberID)
				return
			}

			// If target is offline and query was queued, send offline acknowledgment (F40)
			if res != nil && res.Status == protocol.QueryStatusQueued {
				targetName := res.TargetMemberName
				if targetName == "" {
					targetName = memberID
				}
				ackText := fmt.Sprintf("[TalkIntent 提示]\n%s 目前离线，您的提问已加入排队队列，将在其上线后自动处理。", targetName)
				client := h.GetClient(binding)
				if client != nil {
					if err := client.ReplyMessage(ctx, msg.MessageID, ackText); err != nil {
						h.logger.Warn("Failed to send offline ack reply", "err", err, "msg_id", msg.MessageID)
					}
				}
			}
		}()
	}
}

// OnQueryComplete is the async completion hook called by the Hub when a query reaches a terminal state (F40).
// Replies back to the original Feishu message with the answer or failure notice.
func (h *Handler) OnQueryComplete(ctx context.Context, q *protocol.QueryDetailResponse) error {
	if q == nil || q.FeishuContext == nil || q.FeishuContext.MessageID == "" {
		return nil
	}

	if h.cfg.BindingLookup == nil {
		return errors.New("binding lookup not configured")
	}

	binding, err := h.cfg.BindingLookup(ctx, q.TargetMemberID)
	if err != nil || binding == nil {
		return fmt.Errorf("feishu binding lookup failed for target member %s: %w", q.TargetMemberID, err)
	}

	client := h.GetClient(binding)
	if client == nil {
		return errors.New("failed to acquire feishu client")
	}

	target := q.TargetMemberName
	if target == "" {
		target = q.TargetMemberID
	}

	var replyText string
	switch q.Status {
	case protocol.QueryStatusCompleted, "success":
		replyText = fmt.Sprintf("[TalkIntent 自动回答]\n%s", q.Answer)
	case protocol.QueryStatusExpired:
		replyText = fmt.Sprintf("[TalkIntent 提示]\n提问已过期：未能等到 %s 上线处理。", target)
	case protocol.QueryStatusRefused:
		replyText = fmt.Sprintf("[TalkIntent 提示]\n查询被拒绝：%s 的隐私规则限制了此提问。", target)
		if strings.TrimSpace(q.Answer) != "" {
			replyText += "\n" + q.Answer
		}
	case protocol.QueryStatusTimeout:
		replyText = fmt.Sprintf("[TalkIntent 提示]\n查询超时：%s 未能在时限内响应。", target)
	case protocol.QueryStatusError:
		errMsg := q.ErrorMessage
		if errMsg == "" {
			errMsg = "执行出现错误"
		}
		replyText = fmt.Sprintf("[TalkIntent 提示]\n查询失败：%s", errMsg)
	default:
		if q.Answer != "" {
			replyText = fmt.Sprintf("[TalkIntent 自动回答]\n%s", q.Answer)
		} else {
			return nil
		}
	}

	return client.ReplyMessage(ctx, q.FeishuContext.MessageID, replyText)
}
