package llm

import (
	"context"

	"github.com/Sskift/talkintent/internal/protocol"
)

// Supported dialect constants.
const (
	DialectOpenAI    = "openai"
	DialectAnthropic = "anthropic"
)

// ToolDefinition describes a function the LLM can invoke.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall represents a specific tool invocation request from the model.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatMessage represents a single message in the LLM conversation history.
type ChatMessage struct {
	Role       string     `json:"role"` // "system", "user", "assistant", "tool"
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ChatRequest encapsulates the parameters for calling an LLM.
type ChatRequest struct {
	Model        string           `json:"model"`
	SystemPrompt string           `json:"system_prompt"`
	Messages     []ChatMessage    `json:"messages"`
	Tools        []ToolDefinition `json:"tools,omitempty"`
	Temperature  float64          `json:"temperature"`
	MaxTokens    int              `json:"max_tokens"`
}

// ChatResponse holds the output of an LLM turn.
type ChatResponse struct {
	Content   string              `json:"content"`
	ToolCalls []ToolCall          `json:"tool_calls,omitempty"`
	Usage     protocol.TokenUsage `json:"usage"`
}

// Provider defines the interface that both OpenAI-compatible and Anthropic
// client adapters must satisfy.
type Provider interface {
	Dialect() string
	ChatWithTools(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
}
