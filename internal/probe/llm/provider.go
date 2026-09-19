package llm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/protocol"
)

// NewProvider creates an LLM Provider instance based on configuration.
func NewProvider(cfg config.LLMConfig) (Provider, error) {
	client, err := BuildHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build LLM HTTP client: %w", err)
	}

	dialect := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch dialect {
	case DialectAnthropic:
		return NewAnthropicProvider(cfg, client), nil
	case DialectOpenAI, "":
		return NewOpenAIProvider(cfg, client), nil
	default:
		return nil, fmt.Errorf("unsupported LLM dialect: %q (expected %q or %q)", cfg.Provider, DialectOpenAI, DialectAnthropic)
	}
}

// BuildHTTPClient constructs an http.Client honoring TLS settings:
// CAFile (bare leaf or custom CA pool), TLSServerName (SNI override), and InsecureSkipVerify (logged loudly).
func BuildHTTPClient(cfg config.LLMConfig) (*http.Client, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if cfg.CAFile != "" {
		pemData, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read ca_file %q: %w", cfg.CAFile, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pemData) {
			// Bare leaf certificate in pool
			pool = x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pemData) {
				return nil, fmt.Errorf("failed to parse valid PEM certificate from ca_file %q", cfg.CAFile)
			}
		}
		tlsConfig.RootCAs = pool
	}

	if cfg.TLSServerName != "" {
		tlsConfig.ServerName = cfg.TLSServerName
	}

	if cfg.InsecureSkipVerify {
		tlsConfig.InsecureSkipVerify = true
		slog.Warn("SECURITY WARNING: insecure_skip_verify is enabled for LLM client; TLS certificate verification is disabled")
	}

	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		Proxy:           http.ProxyFromEnvironment,
	}

	timeout := cfg.Timeout()
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}, nil
}

// -------------------------------------------------------------------------
// OpenAI Provider (/v1/chat/completions)
// -------------------------------------------------------------------------

type OpenAIProvider struct {
	cfg    config.LLMConfig
	client *http.Client
}

func NewOpenAIProvider(cfg config.LLMConfig, client *http.Client) *OpenAIProvider {
	return &OpenAIProvider{
		cfg:    cfg,
		client: client,
	}
}

func (p *OpenAIProvider) Dialect() string {
	return DialectOpenAI
}

type openAIToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIChatMessage struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIChatCompletionRequest struct {
	Model       string              `json:"model"`
	Messages    []openAIChatMessage `json:"messages"`
	Tools       []openAITool        `json:"tools,omitempty"`
	Temperature float64             `json:"temperature"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
}

type openAIChatCompletionChoice struct {
	Message struct {
		Role      string           `json:"role"`
		Content   *string          `json:"content"`
		ToolCalls []openAIToolCall `json:"tool_calls"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

type openAIChatCompletionResponse struct {
	ID      string                       `json:"id"`
	Choices []openAIChatCompletionChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

func (p *OpenAIProvider) ChatWithTools(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	if model == "" {
		model = "gpt-4o"
	}

	var messages []openAIChatMessage

	// System prompt if provided
	if req.SystemPrompt != "" {
		sysContent := req.SystemPrompt
		messages = append(messages, openAIChatMessage{
			Role:    "system",
			Content: &sysContent,
		})
	}

	// Convert conversation history
	for _, m := range req.Messages {
		var contentPtr *string
		if m.Content != "" || len(m.ToolCalls) == 0 {
			c := m.Content
			contentPtr = &c
		}

		var toolCalls []openAIToolCall
		for _, tc := range m.ToolCalls {
			toolCalls = append(toolCalls, openAIToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: openAIFunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}

		messages = append(messages, openAIChatMessage{
			Role:       m.Role,
			Content:    contentPtr,
			ToolCallID: m.ToolCallID,
			ToolCalls:  toolCalls,
		})
	}

	var toolsList []openAITool
	for _, t := range req.Tools {
		toolsList = append(toolsList, openAITool{
			Type: "function",
			Function: openAIToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.cfg.Tokens()
	}

	temperature := req.Temperature
	if temperature <= 0 && p.cfg.Temperature > 0 {
		temperature = p.cfg.Temperature
	}

	bodyObj := openAIChatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Tools:       toolsList,
		Temperature: temperature,
		MaxTokens:   maxTokens,
	}

	data, err := json.Marshal(bodyObj)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal openai request: %w", err)
	}

	endpoint := p.resolveEndpoint()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read openai response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var openAIResp openAIChatCompletionResponse
	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return nil, fmt.Errorf("failed to decode openai response: %w", err)
	}

	if openAIResp.Error != nil {
		return nil, fmt.Errorf("openai error: %s", openAIResp.Error.Message)
	}

	if len(openAIResp.Choices) == 0 {
		return nil, errors.New("openai returned no choices")
	}

	choice := openAIResp.Choices[0]
	var content string
	if choice.Message.Content != nil {
		content = *choice.Message.Content
	}

	var parsedToolCalls []ToolCall
	for _, tc := range choice.Message.ToolCalls {
		parsedToolCalls = append(parsedToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	return &ChatResponse{
		Content:   content,
		ToolCalls: parsedToolCalls,
		Usage: protocol.TokenUsage{
			PromptTokens:     openAIResp.Usage.PromptTokens,
			CompletionTokens: openAIResp.Usage.CompletionTokens,
			TotalTokens:      openAIResp.Usage.TotalTokens,
		},
	}, nil
}

func (p *OpenAIProvider) resolveEndpoint() string {
	base := strings.TrimRight(strings.TrimSpace(p.cfg.BaseURL), "/")
	if base == "" {
		return "https://api.openai.com/v1/chat/completions"
	}
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// -------------------------------------------------------------------------
// Anthropic Provider (/v1/messages)
// -------------------------------------------------------------------------

type AnthropicProvider struct {
	cfg    config.LLMConfig
	client *http.Client
}

func NewAnthropicProvider(cfg config.LLMConfig, client *http.Client) *AnthropicProvider {
	return &AnthropicProvider{
		cfg:    cfg,
		client: client,
	}
}

func (p *AnthropicProvider) Dialect() string {
	return DialectAnthropic
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"` // "user" or "assistant"
	Content []anthropicContentBlock `json:"content"`
}

type anthropicMessagesRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
}

type anthropicMessagesResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *AnthropicProvider) ChatWithTools(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.cfg.Tokens()
	}

	temperature := req.Temperature
	if temperature <= 0 && p.cfg.Temperature > 0 {
		temperature = p.cfg.Temperature
	}

	var toolsList []anthropicTool
	for _, t := range req.Tools {
		toolsList = append(toolsList, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}

	// Convert conversation history ensuring alternating "user" and "assistant" roles.
	// Tool results in Anthropic protocol are user-role messages containing tool_result blocks.
	var anthropicMsgs []anthropicMessage

	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role: "user",
				Content: []anthropicContentBlock{
					{
						Type: "text",
						Text: m.Content,
					},
				},
			})

		case "assistant":
			var blocks []anthropicContentBlock
			if m.Content != "" {
				blocks = append(blocks, anthropicContentBlock{
					Type: "text",
					Text: m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				var inputMap any
				if err := json.Unmarshal([]byte(tc.Arguments), &inputMap); err != nil {
					inputMap = map[string]any{}
				}
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: inputMap,
				})
			}
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    "assistant",
				Content: blocks,
			})

		case "tool":
			// If previous message was also "user", merge tool_result block into it
			block := anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}
			if len(anthropicMsgs) > 0 && anthropicMsgs[len(anthropicMsgs)-1].Role == "user" {
				anthropicMsgs[len(anthropicMsgs)-1].Content = append(
					anthropicMsgs[len(anthropicMsgs)-1].Content,
					block,
				)
			} else {
				anthropicMsgs = append(anthropicMsgs, anthropicMessage{
					Role:    "user",
					Content: []anthropicContentBlock{block},
				})
			}
		}
	}

	reqPayload := anthropicMessagesRequest{
		Model:       model,
		System:      req.SystemPrompt,
		Messages:    anthropicMsgs,
		Tools:       toolsList,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}

	data, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal anthropic request: %w", err)
	}

	endpoint := p.resolveEndpoint()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("x-api-key", p.cfg.APIKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read anthropic response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("anthropic api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var anthropicResp anthropicMessagesResponse
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		return nil, fmt.Errorf("failed to decode anthropic response: %w", err)
	}

	if anthropicResp.Error != nil {
		return nil, fmt.Errorf("anthropic error: %s", anthropicResp.Error.Message)
	}

	var answerText strings.Builder
	var toolCalls []ToolCall

	for _, block := range anthropicResp.Content {
		switch block.Type {
		case "thinking":
			// Per CLAUDE.md & design specification:
			// Anthropic responses may contain thinking blocks before tool_use. Skip them, don't crash.
			continue
		case "text":
			answerText.WriteString(block.Text)
		case "tool_use":
			argsBytes, err := json.Marshal(block.Input)
			if err != nil {
				argsBytes = []byte("{}")
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: string(argsBytes),
			})
		}
	}

	return &ChatResponse{
		Content:   answerText.String(),
		ToolCalls: toolCalls,
		Usage: protocol.TokenUsage{
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
		},
	}, nil
}

func (p *AnthropicProvider) resolveEndpoint() string {
	base := strings.TrimRight(strings.TrimSpace(p.cfg.BaseURL), "/")
	if base == "" {
		return "https://api.anthropic.com/v1/messages"
	}
	if strings.HasSuffix(base, "/messages") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}
