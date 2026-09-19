package protocol

// WorkspaceInfo contains metadata about a registered developer workspace on a client.
type WorkspaceInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	RootPath         string `json:"root_path"`
	HasPrivacyPrompt bool   `json:"has_privacy_prompt"`
}

// DaemonHelloPayload is sent by client daemons upon connecting to the Hub.
type DaemonHelloPayload struct {
	SessionID      string          `json:"session_id,omitempty"`
	MemberID       string          `json:"member_id"`
	ClientVersion  string          `json:"client_version"`
	MachineName    string          `json:"machine_name"`
	OS             string          `json:"os"`
	Arch           string          `json:"arch"`
	MaxConcurrency int             `json:"max_concurrency"`
	Workspaces     []WorkspaceInfo `json:"workspaces"`
}

// HubAckPayload is sent by the Hub in response to DaemonHelloPayload.
type HubAckPayload struct {
	Authenticated        bool   `json:"authenticated"`
	SessionID            string `json:"session_id,omitempty"`
	HeartbeatIntervalSec int    `json:"heartbeat_interval_sec"`
	PendingQueriesCount  int    `json:"pending_queries_count"`
}

// HeartbeatPingPayload is periodically sent by the daemon to keep connection alive.
type HeartbeatPingPayload struct {
	ActiveProbeCount int `json:"active_probe_count"`
}

// HeartbeatPongPayload is the Hub's response to HeartbeatPingPayload.
type HeartbeatPongPayload struct {
	ServerTime int64 `json:"server_time"`
}

// TokenUsage holds token consumption statistics for an LLM probe run.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// QueryRequestPayload is pushed by the Hub to the daemon to execute an on-site probe.
type QueryRequestPayload struct {
	QueryID         string `json:"query_id"`
	AskerID         string `json:"asker_id"`
	AskerName       string `json:"asker_name"`
	AskerType       string `json:"asker_type"` // "member", "feishu", "anonymous"
	Query           string `json:"query"`
	TargetWorkspace string `json:"target_workspace,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	CreatedAt       int64  `json:"created_at"`
}

// QueryResponsePayload is sent by the daemon back to the Hub with probe results.
type QueryResponsePayload struct {
	QueryID      string     `json:"query_id"`
	Status       string     `json:"status"` // "completed" (or "success"), "refused", "error", "timeout"
	Answer       string     `json:"answer,omitempty"`
	ToolsUsed    []string   `json:"tools_used,omitempty"`
	DurationMS   int64      `json:"duration_ms"`
	TokenUsage   TokenUsage `json:"token_usage"`
	ErrorMessage string     `json:"error_message,omitempty"`
}

// QueryCancelPayload is sent by the Hub to abort an in-flight query.
type QueryCancelPayload struct {
	QueryID string `json:"query_id"`
	Reason  string `json:"reason"`
}
