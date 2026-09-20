package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// HandlerConfig configures the Feishu event handler.
type HandlerConfig struct {
	// BindingLookup retrieves Feishu bot credentials for a member. Required.
	BindingLookup BindingLookupFunc

	// Dispatch routes the incoming query to the target member's daemon or offline queue. Required.
	Dispatch DispatchFunc

	// BaseURL overrides the Feishu Open Platform API base URL (useful for mock/fake servers).
	// Default: "https://open.feishu.cn".
	BaseURL string

	// Deduplicator deduplicates incoming events and messages.
	// If nil, a default deduplicator is created.
	Deduplicator *Deduplicator

	// Logger receives diagnostic logs. If nil, slog.Default() is used.
	Logger *slog.Logger

	// ClientFactory allows injecting custom Client instances (e.g. for testing).
	ClientFactory func(appID, appSecret, baseURL string) *Client
}

// Handler coordinates Feishu event processing and asynchronous reply dispatch.
type Handler struct {
	cfg       HandlerConfig
	dedupe    *Deduplicator
	logger    *slog.Logger
	clientsMu sync.Mutex
	clients   map[string]*Client // keyed by appID
}

// NewHandler creates a new Feishu event handler.
func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.feishu.cn"
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

// EvictClient removes cached clients matching the specified appID.
func (h *Handler) EvictClient(appID string) {
	if appID == "" {
		return
	}
	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()
	for k := range h.clients {
		if k == appID || strings.HasPrefix(k, appID+":") {
			delete(h.clients, k)
		}
	}
}

// GetClient retrieves or creates a Client for the specified credentials.
func (h *Handler) GetClient(binding *protocol.FeishuBindingRequest) *Client {
	if binding == nil {
		return nil
	}
	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()

	baseURL := h.cfg.BaseURL
	if binding.BaseURL != "" {
		baseURL = binding.BaseURL
	}

	secretHash := sha256.Sum256([]byte(binding.AppSecret))
	key := fmt.Sprintf("%s:%x:%s", binding.AppID, secretHash[:8], baseURL)
	if client, ok := h.clients[key]; ok {
		return client
	}

	var client *Client
	if h.cfg.ClientFactory != nil {
		client = h.cfg.ClientFactory(binding.AppID, binding.AppSecret, baseURL)
	} else {
		client = NewClient(binding.AppID, binding.AppSecret, baseURL)
	}
	h.clients[key] = client
	return client
}

// ProcessEvent processes an incoming Feishu event payload received via long connection.
func (h *Handler) ProcessEvent(ctx context.Context, targetMemberID string, rawBody []byte) error {
	var env EventEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		return fmt.Errorf("invalid json event payload: %w", err)
	}

	if env.Header == nil || env.Event == nil {
		return nil
	}

	if env.Header.EventType != "im.message.receive_v1" {
		return nil
	}

	// Ignore messages sent by apps/bots to prevent reply loops
	if env.Event.Sender != nil && env.Event.Sender.SenderType != "" && env.Event.Sender.SenderType != "user" {
		return nil
	}

	msg := env.Event.Message
	if msg == nil {
		return nil
	}

	// Deduplication by event_id and message_id
	eventID := env.Header.EventID
	msgID := msg.MessageID
	if (eventID != "" && h.dedupe.CheckAndRecord(eventID)) || (msgID != "" && h.dedupe.CheckAndRecord(msgID)) {
		return nil
	}

	// Only text messages are supported
	if msg.MessageType != "text" {
		return nil
	}

	queryText, err := StripMentions(msg.Content, msg.Mentions)
	if err != nil || queryText == "" {
		return nil
	}

	var binding *protocol.FeishuBindingRequest
	if h.cfg.BindingLookup != nil {
		binding, _ = h.cfg.BindingLookup(ctx, targetMemberID)
	}

	appID := ""
	if binding != nil {
		appID = binding.AppID
	}

	// Prepare Asker & Feishu Context
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
		AppID:     appID,
	}

	// Asynchronous dispatch to Hub router
	if h.cfg.Dispatch != nil {
		go func() {
			dispatchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			res, err := h.cfg.Dispatch(dispatchCtx, targetMemberID, asker, queryText, feishuCtx)
			if err != nil {
				h.logger.Error("Feishu dispatch failed", "err", err, "member_id", targetMemberID)
				return
			}

			// If target is offline and query was queued, send offline acknowledgment
			if res != nil && res.Status == protocol.QueryStatusQueued && binding != nil {
				targetName := res.TargetMemberName
				if targetName == "" {
					targetName = targetMemberID
				}
				ackText := fmt.Sprintf("[TalkIntent 提示]\n%s 目前离线，您的提问已加入排队队列，将在其上线后自动处理。", targetName)
				client := h.GetClient(binding)
				if client != nil {
					if err := client.ReplyMessage(dispatchCtx, msg.MessageID, ackText); err != nil {
						h.logger.Warn("Failed to send offline ack reply", "err", err, "msg_id", msg.MessageID)
					}
				}
			}
		}()
	}

	return nil
}

// OnQueryComplete is the async completion hook called by the Hub when a query reaches a terminal state.
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
