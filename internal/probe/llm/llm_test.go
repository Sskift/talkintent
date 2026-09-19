package llm

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sskift/talkintent/internal/config"
)

func TestOpenAIProvider_ChatWithTools(t *testing.T) {
	// Fake OpenAI HTTP server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req openAIChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// First turn: return tool call
		if len(req.Messages) <= 2 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "chatcmpl-123",
				"choices": [
					{
						"message": {
							"role": "assistant",
							"tool_calls": [
								{
									"id": "call_abc123",
									"type": "function",
									"function": {
										"name": "git_status",
										"arguments": "{}"
									}
								}
							]
						},
						"finish_reason": "tool_calls"
					}
				],
				"usage": {
					"prompt_tokens": 15,
					"completion_tokens": 10,
					"total_tokens": 25
				}
			}`))
			return
		}

		// Second turn: return final answer
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-124",
			"choices": [
				{
					"message": {
						"role": "assistant",
						"content": "Work is progressing on auth branch"
					},
					"finish_reason": "stop"
				}
			],
			"usage": {
				"prompt_tokens": 40,
				"completion_tokens": 15,
				"total_tokens": 55
			}
		}`))
	}))
	defer srv.Close()

	cfg := config.LLMConfig{
		Provider:  DialectOpenAI,
		BaseURL:   srv.URL,
		APIKey:    "test-key",
		Model:     "gpt-4o",
		MaxTokens: 1000,
	}

	provider, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := context.Background()

	// Turn 1
	req1 := &ChatRequest{
		SystemPrompt: "You are a probe",
		Messages: []ChatMessage{
			{Role: "user", Content: "status?"},
		},
		Tools: []ToolDefinition{
			{Name: "git_status", Description: "get status"},
		},
	}

	resp1, err := provider.ChatWithTools(ctx, req1)
	if err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}
	if len(resp1.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp1.ToolCalls))
	}
	if resp1.ToolCalls[0].Name != "git_status" || resp1.ToolCalls[0].ID != "call_abc123" {
		t.Errorf("unexpected tool call: %+v", resp1.ToolCalls[0])
	}
	if resp1.Usage.TotalTokens != 25 {
		t.Errorf("expected 25 total tokens, got %d", resp1.Usage.TotalTokens)
	}

	// Turn 2
	req2 := &ChatRequest{
		SystemPrompt: "You are a probe",
		Messages: []ChatMessage{
			{Role: "user", Content: "status?"},
			{Role: "assistant", ToolCalls: resp1.ToolCalls},
			{Role: "tool", ToolCallID: "call_abc123", Content: "On branch feature/auth-v2"},
		},
	}

	resp2, err := provider.ChatWithTools(ctx, req2)
	if err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}
	if !strings.Contains(resp2.Content, "auth branch") {
		t.Errorf("expected final answer, got: %s", resp2.Content)
	}
	if resp2.Usage.TotalTokens != 55 {
		t.Errorf("expected 55 total tokens, got %d", resp2.Usage.TotalTokens)
	}
}

func TestAnthropicProvider_ThinkingBlockSkipped(t *testing.T) {
	// Fake Anthropic server emitting thinking blocks
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "anthropic-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			http.Error(w, "invalid version header", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Anthropic response with a thinking block followed by tool_use
		_, _ = w.Write([]byte(`{
			"id": "msg_01X",
			"type": "message",
			"role": "assistant",
			"content": [
				{
					"type": "thinking",
					"thinking": "The user is asking about the git branch. I should use git_status."
				},
				{
					"type": "text",
					"text": "Let me inspect the workspace."
				},
				{
					"type": "tool_use",
					"id": "toolu_019",
					"name": "git_status",
					"input": {}
				}
			],
			"stop_reason": "tool_use",
			"usage": {
				"input_tokens": 120,
				"output_tokens": 45
			}
		}`))
	}))
	defer srv.Close()

	cfg := config.LLMConfig{
		Provider:  DialectAnthropic,
		BaseURL:   srv.URL,
		APIKey:    "anthropic-key",
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 2000,
	}

	provider, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create anthropic provider: %v", err)
	}

	ctx := context.Background()
	req := &ChatRequest{
		SystemPrompt: "You are a probe",
		Messages: []ChatMessage{
			{Role: "user", Content: "What branch is active?"},
		},
		Tools: []ToolDefinition{
			{Name: "git_status", Description: "git status"},
		},
	}

	resp, err := provider.ChatWithTools(ctx, req)
	if err != nil {
		t.Fatalf("anthropic request failed: %v", err)
	}

	// Verify thinking block was skipped and didn't crash
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "toolu_019" || resp.ToolCalls[0].Name != "git_status" {
		t.Errorf("unexpected tool call: %+v", resp.ToolCalls[0])
	}
	if resp.Content != "Let me inspect the workspace." {
		t.Errorf("expected text content, got: %q", resp.Content)
	}
	if resp.Usage.PromptTokens != 120 || resp.Usage.CompletionTokens != 45 || resp.Usage.TotalTokens != 165 {
		t.Errorf("unexpected usage: %+v", resp.Usage)
	}
}

func TestLLMProvider_TLSConfig(t *testing.T) {
	// Start an HTTPS test server with self-signed TLS cert
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-tls",
			"choices": [
				{
					"message": {
						"role": "assistant",
						"content": "secure response"
					},
					"finish_reason": "stop"
				}
			],
			"usage": {
				"prompt_tokens": 5,
				"completion_tokens": 5,
				"total_tokens": 10
			}
		}`))
	}))
	defer tlsSrv.Close()

	// Export server cert to a PEM file (bare leaf cert)
	cert := tlsSrv.Certificate()
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})

	tmp := t.TempDir()
	caPath := filepath.Join(tmp, "ca.pem")
	if err := os.WriteFile(caPath, pemData, 0644); err != nil {
		t.Fatalf("failed to write ca cert: %v", err)
	}

	// 1. Test connection with CAFile pointing to the bare leaf cert
	cfgWithCA := config.LLMConfig{
		Provider: DialectOpenAI,
		BaseURL:  tlsSrv.URL,
		CAFile:   caPath,
		Model:    "test-model",
	}

	providerCA, err := NewProvider(cfgWithCA)
	if err != nil {
		t.Fatalf("failed to create provider with CA: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	respCA, err := providerCA.ChatWithTools(ctx, &ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("expected TLS handshake with CAFile to succeed, got: %v", err)
	}
	if respCA.Content != "secure response" {
		t.Errorf("unexpected response: %s", respCA.Content)
	}

	// 2. Test connection with InsecureSkipVerify: true
	cfgInsecure := config.LLMConfig{
		Provider:           DialectOpenAI,
		BaseURL:            tlsSrv.URL,
		InsecureSkipVerify: true,
		Model:              "test-model",
	}

	providerInsecure, err := NewProvider(cfgInsecure)
	if err != nil {
		t.Fatalf("failed to create insecure provider: %v", err)
	}

	respInsecure, err := providerInsecure.ChatWithTools(ctx, &ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("expected InsecureSkipVerify handshake to succeed, got: %v", err)
	}
	if respInsecure.Content != "secure response" {
		t.Errorf("unexpected response: %s", respInsecure.Content)
	}
}
