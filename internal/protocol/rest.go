package protocol

// InviteCreateRequest is used by administrators to generate a new invite code.
type InviteCreateRequest struct {
	TargetName     string   `json:"target_name"`
	Aliases        []string `json:"aliases,omitempty"`
	ExpiresInHours int      `json:"expires_in_hours,omitempty"`
}

// InviteCreateResponse is returned when an invite code is generated.
type InviteCreateResponse struct {
	Code       string `json:"code"`
	ExpiresAt  int64  `json:"expires_at"`
	TargetName string `json:"target_name"`
}

// PairRequest is sent by a client to exchange an invite code for a permanent token.
type PairRequest struct {
	InviteCode    string `json:"invite_code"`
	MachineName   string `json:"machine_name"`
	ClientVersion string `json:"client_version"`
}

// PairResponse is returned when pairing succeeds.
type PairResponse struct {
	MemberID   string `json:"member_id"`
	MemberName string `json:"member_name"`
	Token      string `json:"token"`
	HubURL     string `json:"hub_url"`
}

// TargetResolveResponse is returned when target resolution is ambiguous.
type TargetResolveResponse struct {
	Status     string            `json:"status"` // "ambiguous"
	Candidates []CandidateMember `json:"candidates"`
}

// CandidateMember is returned when a query target matches multiple members ambiguously.
type CandidateMember struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MatchedAlias string `json:"matched_alias,omitempty"`
}

// MemberInfo represents a registered team member.
type MemberInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Aliases      []string `json:"aliases"`
	Online       bool     `json:"online"`
	LastSeenAt   int64    `json:"last_seen_at"`
	MachineName  string   `json:"machine_name,omitempty"`
	Workspaces   []string `json:"workspaces,omitempty"`
	HasFeishuBot bool     `json:"has_feishu_bot"`
}

// MemberListResponse is returned by GET /api/v1/members.
type MemberListResponse struct {
	Members []MemberInfo `json:"members"`
}

// MemberUpdateRequest allows updating member name and aliases.
type MemberUpdateRequest struct {
	Name    string   `json:"name,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

// QuerySubmitRequest is sent by askers to submit a new question.
type QuerySubmitRequest struct {
	Target          string `json:"target"`
	Query           string `json:"query"`
	TargetWorkspace string `json:"target_workspace,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
	TTLSeconds      int    `json:"ttl_seconds,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
	Wait            bool   `json:"wait,omitempty"`
}

// QuerySubmitResponse is returned immediately when a query is accepted.
type QuerySubmitResponse struct {
	QueryID          string `json:"query_id"`
	Status           string `json:"status"` // "dispatched", "queued"
	TargetMemberID   string `json:"target_member_id"`
	TargetMemberName string `json:"target_member_name"`
	QueuePosition    int    `json:"queue_position,omitempty"`
	TTLExpiresAt     int64  `json:"ttl_expires_at,omitempty"`
	CreatedAt        int64  `json:"created_at"`
}

// FeishuContext holds metadata for asynchronous IM replies back to Feishu chats.
type FeishuContext struct {
	MessageID string `json:"message_id"`
	ChatID    string `json:"chat_id"`
	AppID     string `json:"app_id"`
}

// QueryDetailResponse provides full status, metrics, and answer for a query.
type QueryDetailResponse struct {
	QueryID          string         `json:"query_id"`
	Status           string         `json:"status"` // "dispatched", "queued", "completed", "refused", "error", "timeout", "expired"
	AskerID          string         `json:"asker_id"`
	AskerName        string         `json:"asker_name"`
	TargetMemberID   string         `json:"target_member_id"`
	TargetMemberName string         `json:"target_member_name"`
	TargetWorkspace  string         `json:"target_workspace,omitempty"`
	Query            string         `json:"query"`
	Answer           string         `json:"answer,omitempty"`
	ToolsUsed        []string       `json:"tools_used,omitempty"`
	DurationMS       int64          `json:"duration_ms,omitempty"`
	TokenUsage       TokenUsage     `json:"token_usage,omitempty"`
	QueuePosition    int            `json:"queue_position,omitempty"`
	TTLExpiresAt     int64          `json:"ttl_expires_at,omitempty"`
	CreatedAt        int64          `json:"created_at"`
	CompletedAt      int64          `json:"completed_at,omitempty"`
	ErrorMessage     string         `json:"error_message,omitempty"`
	FeishuContext    *FeishuContext `json:"feishu_context,omitempty"`
	Origin           string         `json:"origin,omitempty"` // "rest", "feishu", "cli"
}

// AuditLogEntry represents a single historical query audit record.
type AuditLogEntry struct {
	QueryID          string   `json:"query_id"`
	Timestamp        int64    `json:"timestamp"`
	AskerID          string   `json:"asker_id"`
	AskerName        string   `json:"asker_name"`
	AskerType        string   `json:"asker_type"`
	TargetMemberID   string   `json:"target_member_id"`
	TargetMemberName string   `json:"target_member_name"`
	Query            string   `json:"query"`
	Status           string   `json:"status"`
	Answer           string   `json:"answer,omitempty"`
	ToolsUsed        []string `json:"tools_used,omitempty"`
	DurationMS       int64    `json:"duration_ms,omitempty"`
}

// AuditListResponse is returned for inbound/outbound audit queries.
type AuditListResponse struct {
	Total   int             `json:"total"`
	Entries []AuditLogEntry `json:"entries"`
}

// FeishuBindingRequest contains credentials for a member's Feishu bot.
type FeishuBindingRequest struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
	BaseURL   string `json:"base_url,omitempty"`
}

// FeishuBindingResponse contains the configured Feishu bot status.
type FeishuBindingResponse struct {
	Bound       bool   `json:"bound"`
	AppID       string `json:"app_id,omitempty"`
	Status      string `json:"status,omitempty"`
	Error       string `json:"error,omitempty"`
	ConnectedAt string `json:"connected_at,omitempty"`
	Reconnects  int    `json:"reconnects"`
}
