package protocol

import "encoding/json"

// Version1 is the current wire protocol version.
const Version1 = "v1"

// Message type constants for WebSocket frames.
const (
	TypeDaemonHello   = "daemon_hello"
	TypeHubAck        = "hub_ack"
	TypeHeartbeatPing = "heartbeat_ping"
	TypeHeartbeatPong = "heartbeat_pong"
	TypeQueryRequest  = "query_request"
	TypeQueryResponse = "query_response"
	TypeQueryCancel   = "query_cancel"
)

// Standard error code constants.
const (
	ErrCodeUnauthorized    = "UNAUTHORIZED"
	ErrCodeForbidden       = "FORBIDDEN"
	ErrCodeInvalidArgument = "INVALID_ARGUMENT"
	ErrCodeAmbiguousTarget = "AMBIGUOUS_TARGET"
	ErrCodeNotFound        = "NOT_FOUND"
	ErrCodeConflict        = "CONFLICT"
	ErrCodeRateLimited     = "RATE_LIMITED"
	ErrCodeInternalError   = "INTERNAL_ERROR"
	ErrCodeTargetOffline   = "TARGET_OFFLINE"
)

// Query status constants.
const (
	QueryStatusPending    = "pending"
	QueryStatusDispatched = "dispatched"
	QueryStatusQueued     = "queued"
	QueryStatusSuccess    = "success"
	QueryStatusCompleted  = "completed"
	QueryStatusRefused    = "refused"
	QueryStatusError      = "error"
	QueryStatusTimeout    = "timeout"
	QueryStatusExpired    = "expired"
)

// Envelope is the standard JSON envelope for all WebSocket messages.
type Envelope struct {
	Version   string          `json:"version"`
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp int64           `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// ErrorDetail represents the structured error body returned in REST responses.
type ErrorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// ErrorResponse is the standard HTTP error response container.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}
