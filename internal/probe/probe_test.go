package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/probe/llm"
	"github.com/Sskift/talkintent/internal/protocol"
)

func TestProbeAgent_FullLoopOpenAI(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "main.go")
	_ = os.WriteFile(testFile, []byte("package main\nfunc main() {}\n"), 0644)

	// Fake OpenAI server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		// If last message is tool result, give final answer
		if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "tool" {
			_, _ = w.Write([]byte(`{
				"id": "cmpl-2",
				"choices": [
					{
						"message": {
							"role": "assistant",
							"content": "Found main.go in workspace"
						},
						"finish_reason": "stop"
					}
				],
				"usage": {
					"prompt_tokens": 100,
					"completion_tokens": 20,
					"total_tokens": 120
				}
			}`))
			return
		}

		// First turn: call list_dir
		_, _ = w.Write([]byte(`{
			"id": "cmpl-1",
			"choices": [
				{
					"message": {
						"role": "assistant",
						"tool_calls": [
							{
								"id": "call_ld",
								"type": "function",
								"function": {
									"name": "list_dir",
									"arguments": "{\"path\": \".\"}"
								}
							}
						]
					},
					"finish_reason": "tool_calls"
				}
			],
			"usage": {
				"prompt_tokens": 50,
				"completion_tokens": 15,
				"total_tokens": 65
			}
		}`))
	}))
	defer srv.Close()

	llmCfg := config.LLMConfig{
		Provider:  llm.DialectOpenAI,
		BaseURL:   srv.URL,
		APIKey:    "test-key",
		Model:     "gpt-4o",
		MaxTokens: 2000,
		MaxSteps:  5,
	}

	agent := NewDefaultAgent(nil)
	ws := config.WorkspaceConfig{
		Name:     "test-ws",
		RootPath: tmp,
	}

	res, err := agent.Run(context.Background(), &RunRequest{
		QueryID:   "qry_probe_01",
		Query:     "what files exist in the project?",
		Workspace: ws,
		LLMConfig: llmCfg,
	})
	if err != nil {
		t.Fatalf("agent.Run failed: %v", err)
	}

	if res.Status != protocol.QueryStatusCompleted {
		t.Errorf("expected status completed, got: %s", res.Status)
	}
	if !strings.Contains(res.Answer, "Found main.go") {
		t.Errorf("unexpected answer: %s", res.Answer)
	}
	if len(res.ToolsUsed) != 1 || res.ToolsUsed[0] != "list_dir" {
		t.Errorf("unexpected tools used: %v", res.ToolsUsed)
	}
	if res.TokenUsage.TotalTokens != 185 {
		t.Errorf("expected total tokens 185, got %d", res.TokenUsage.TotalTokens)
	}
}

func TestProbeAgent_FullLoopAnthropic(t *testing.T) {
	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "service.go"), []byte("package service"), 0644)

	// Fake Anthropic server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		if len(req.Messages) >= 3 {
			// Return final answer
			_, _ = w.Write([]byte(`{
				"id": "msg_02",
				"type": "message",
				"role": "assistant",
				"content": [
					{
						"type": "thinking",
						"thinking": "Synthesizing answer"
					},
					{
						"type": "text",
						"text": "Service is written in Go."
					}
				],
				"stop_reason": "end_turn",
				"usage": {
					"input_tokens": 150,
					"output_tokens": 30
				}
			}`))
			return
		}

		// First turn: call list_dir
		_, _ = w.Write([]byte(`{
			"id": "msg_01",
			"type": "message",
			"role": "assistant",
			"content": [
				{
					"type": "thinking",
					"thinking": "Inspecting directory"
				},
				{
					"type": "tool_use",
					"id": "toolu_01",
					"name": "list_dir",
					"input": {"path": "."}
				}
			],
			"stop_reason": "tool_use",
			"usage": {
				"input_tokens": 80,
				"output_tokens": 20
			}
		}`))
	}))
	defer srv.Close()

	llmCfg := config.LLMConfig{
		Provider:  llm.DialectAnthropic,
		BaseURL:   srv.URL,
		APIKey:    "test-anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 2000,
	}

	agent := NewDefaultAgent(nil)
	ws := config.WorkspaceConfig{
		Name:     "service-ws",
		RootPath: tmp,
	}

	res, err := agent.Run(context.Background(), &RunRequest{
		QueryID:   "qry_probe_anthropic",
		Query:     "tell me about the service",
		Workspace: ws,
		LLMConfig: llmCfg,
	})
	if err != nil {
		t.Fatalf("anthropic agent.Run failed: %v", err)
	}

	if res.Status != protocol.QueryStatusCompleted {
		t.Errorf("expected status completed, got %s", res.Status)
	}
	if !strings.Contains(res.Answer, "Service is written in Go.") {
		t.Errorf("unexpected answer: %s", res.Answer)
	}
	if res.TokenUsage.TotalTokens != (80 + 20 + 150 + 30) {
		t.Errorf("expected total tokens 280, got %d", res.TokenUsage.TotalTokens)
	}
}

func TestProbeAgent_StepLimitFallback(t *testing.T) {
	tmp := t.TempDir()

	// Provider that always asks for tool calls
	infiniteToolProvider := &infiniteToolProvider{}

	agent := NewDefaultAgent(func(cfg config.LLMConfig) (llm.Provider, error) {
		return infiniteToolProvider, nil
	})

	ws := config.WorkspaceConfig{
		Name:     "loop-ws",
		RootPath: tmp,
	}

	maxSteps := 3
	req := &RunRequest{
		QueryID:   "qry_step_limit",
		Query:     "loop forever?",
		Workspace: ws,
		LLMConfig: config.LLMConfig{
			MaxSteps: maxSteps,
		},
	}

	res, err := agent.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("agent.Run failed: %v", err)
	}

	if res.Status != protocol.QueryStatusCompleted {
		t.Errorf("expected completed on step limit fallback, got: %s", res.Status)
	}
	if infiniteToolProvider.calls != maxSteps+1 {
		t.Errorf("expected %d calls (steps + summary), got %d", maxSteps+1, infiniteToolProvider.calls)
	}
}

type infiniteToolProvider struct {
	calls int
}

func (p *infiniteToolProvider) Dialect() string { return "openai" }
func (p *infiniteToolProvider) ChatWithTools(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	if len(req.Tools) == 0 {
		// Summary call
		return &llm.ChatResponse{
			Content: "Synthesized summary after max steps reached",
			Usage:   protocol.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}, nil
	}
	return &llm.ChatResponse{
		ToolCalls: []llm.ToolCall{
			{
				ID:        "tool_call_loop",
				Name:      "recent_files",
				Arguments: "{}",
			},
		},
		Usage: protocol.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

func TestProbeAgent_ContextTimeout(t *testing.T) {
	tmp := t.TempDir()

	hangingProvider := &hangingProvider{}

	agent := NewDefaultAgent(func(cfg config.LLMConfig) (llm.Provider, error) {
		return hangingProvider, nil
	})

	ws := config.WorkspaceConfig{
		Name:     "hang-ws",
		RootPath: tmp,
	}

	req := &RunRequest{
		QueryID:   "qry_timeout",
		Query:     "will timeout",
		Workspace: ws,
		Timeout:   50 * time.Millisecond,
		LLMConfig: config.LLMConfig{},
	}

	res, err := agent.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("expected nil error returned, got: %v", err)
	}
	if res.Status != protocol.QueryStatusTimeout {
		t.Errorf("expected QueryStatusTimeout, got: %s", res.Status)
	}
	if res.ErrorMessage == "" {
		t.Errorf("expected non-empty ErrorMessage on timeout")
	}
}

type hangingProvider struct{}

func (p *hangingProvider) Dialect() string { return "openai" }
func (p *hangingProvider) ChatWithTools(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunPrivacyTestHelper(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":    "assistant",
						"content": "Privacy dry-run response",
					},
				},
			},
		})
	}))
	defer ts.Close()

	tmp := t.TempDir()

	ws := config.WorkspaceConfig{
		Name:     "dryrun-ws",
		RootPath: tmp,
	}

	res, err := RunPrivacyTest(context.Background(), ws, "dry run test", config.LLMConfig{
		Provider: "openai",
		Model:    "test",
		BaseURL:  ts.URL,
	})

	if err != nil {
		t.Fatalf("expected RunPrivacyTest to return payload, got err: %v", err)
	}
	if res == nil {
		t.Fatalf("expected non-nil response payload")
	}
	if res.Status != protocol.QueryStatusCompleted {
		t.Errorf("expected QueryStatusCompleted, got: %s", res.Status)
	}
	if res.Answer != "Privacy dry-run response" {
		t.Errorf("expected 'Privacy dry-run response', got: %q", res.Answer)
	}
}

func TestProbeAgent_ToolOutputRedactedInConversation(t *testing.T) {
	tmp := t.TempDir()
	// Create a file with a private IP and a sensitive token
	sampleFile := filepath.Join(tmp, "sample.txt")
	_ = os.WriteFile(sampleFile, []byte("Server IP: 192.168.1.100\nAPI Key: sk-proj-1234567890abcdef1234567890\n"), 0644)

	var sawToolMessage bool
	var toolMessageContent string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")

		for _, m := range req.Messages {
			if m.Role == "tool" {
				sawToolMessage = true
				toolMessageContent = m.Content
				_, _ = w.Write([]byte(`{
					"id": "cmpl-tool-done",
					"choices": [{"message": {"role": "assistant", "content": "Done"}, "finish_reason": "stop"}],
					"usage": {"prompt_tokens": 50, "completion_tokens": 10, "total_tokens": 60}
				}`))
				return
			}
		}

		// First turn: invoke read_file
		_, _ = w.Write([]byte(`{
			"id": "cmpl-call-read",
			"choices": [
				{
					"message": {
						"role": "assistant",
						"tool_calls": [
							{
								"id": "call_rf",
								"type": "function",
								"function": {
									"name": "read_file",
									"arguments": "{\"path\": \"sample.txt\"}"
								}
							}
						]
					},
					"finish_reason": "tool_calls"
				}
			],
			"usage": {"prompt_tokens": 30, "completion_tokens": 15, "total_tokens": 45}
		}`))
	}))
	defer srv.Close()

	agent := NewDefaultAgent(nil)
	ws := config.WorkspaceConfig{
		Name:     "redact-ws",
		RootPath: tmp,
	}

	res, err := agent.Run(context.Background(), &RunRequest{
		QueryID:   "qry_redact_tool_out",
		Query:     "read sample.txt",
		Workspace: ws,
		LLMConfig: config.LLMConfig{
			Provider:  "openai",
			BaseURL:   srv.URL,
			APIKey:    "test-key",
			Model:     "gpt-4o",
			MaxTokens: 1000,
		},
	})
	if err != nil {
		t.Fatalf("agent.Run failed: %v", err)
	}
	if res.Status != protocol.QueryStatusCompleted {
		t.Fatalf("expected completed status, got: %s", res.Status)
	}
	if !sawToolMessage {
		t.Fatalf("expected server to see tool message in conversation")
	}

	// Verify that private IP and API token were redacted from the tool message sent to LLM
	if strings.Contains(toolMessageContent, "192.168.1.100") {
		t.Errorf("private IP leaked into tool message sent to LLM: %s", toolMessageContent)
	}
	if strings.Contains(toolMessageContent, "sk-proj-1234567890abcdef1234567890") {
		t.Errorf("API token leaked into tool message sent to LLM: %s", toolMessageContent)
	}
	if !strings.Contains(toolMessageContent, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in tool message sent to LLM, got: %s", toolMessageContent)
	}
}

type scriptedProvider struct {
	dialect   string
	responses []*llm.ChatResponse
	idx       int
}

func (p *scriptedProvider) Dialect() string {
	if p.dialect != "" {
		return p.dialect
	}
	return "openai"
}

func (p *scriptedProvider) ChatWithTools(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if p.idx >= len(p.responses) {
		return &llm.ChatResponse{Content: "End of scripted responses"}, nil
	}
	resp := p.responses[p.idx]
	p.idx++
	return resp, nil
}

func TestProbeAgent_RefuseToolCall_Redacted(t *testing.T) {
	tmp := t.TempDir()

	refuseArgs, _ := json.Marshal(map[string]string{
		"reason": "Restricted branch; secret token sk-proj-1234567890abcdef1234567890 at 10.1.2.3 cannot be shared.",
	})

	prov := &scriptedProvider{
		responses: []*llm.ChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{
						ID:        "call_refuse_01",
						Name:      RefuseToolName,
						Arguments: string(refuseArgs),
					},
				},
				Usage: protocol.TokenUsage{PromptTokens: 50, CompletionTokens: 20, TotalTokens: 70},
			},
		},
	}

	agent := NewDefaultAgent(func(cfg config.LLMConfig) (llm.Provider, error) {
		return prov, nil
	})

	ws := config.WorkspaceConfig{
		Name:     "test-ws",
		RootPath: tmp,
	}

	res, err := agent.Run(context.Background(), &RunRequest{
		QueryID:   "qry_refuse_test",
		Query:     "tell me secrets",
		Workspace: ws,
	})
	if err != nil {
		t.Fatalf("agent.Run failed: %v", err)
	}

	if res.Status != protocol.QueryStatusRefused {
		t.Fatalf("expected status 'refused', got: %s", res.Status)
	}
	if strings.Contains(res.Answer, "sk-proj-1234567890abcdef1234567890") {
		t.Errorf("secret token was not redacted from refusal answer: %s", res.Answer)
	}
	if strings.Contains(res.Answer, "10.1.2.3") {
		t.Errorf("private IP was not redacted from refusal answer: %s", res.Answer)
	}
	if !strings.Contains(res.Answer, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in refusal answer, got: %s", res.Answer)
	}
	if len(res.ToolsUsed) != 0 {
		t.Errorf("expected 0 tools used since refuse tool was called immediately, got: %v", res.ToolsUsed)
	}
	if res.TokenUsage.TotalTokens != 70 {
		t.Errorf("expected 70 total tokens, got: %d", res.TokenUsage.TotalTokens)
	}
}

func TestProbeAgent_RefuseToolCall_WithPriorTools(t *testing.T) {
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "sample.txt")
	_ = os.WriteFile(testFile, []byte("some internal data"), 0644)

	refuseArgs, _ := json.Marshal(map[string]string{
		"reason": "Confidential repository area cannot be disclosed per privacy rules.",
	})

	prov := &scriptedProvider{
		responses: []*llm.ChatResponse{
			// Turn 1: run read_file
			{
				ToolCalls: []llm.ToolCall{
					{
						ID:        "call_rf_01",
						Name:      "read_file",
						Arguments: `{"path":"sample.txt"}`,
					},
				},
				Usage: protocol.TokenUsage{PromptTokens: 30, CompletionTokens: 10, TotalTokens: 40},
			},
			// Turn 2: invoke refuse
			{
				ToolCalls: []llm.ToolCall{
					{
						ID:        "call_refuse_02",
						Name:      RefuseToolName,
						Arguments: string(refuseArgs),
					},
				},
				Usage: protocol.TokenUsage{PromptTokens: 60, CompletionTokens: 15, TotalTokens: 75},
			},
		},
	}

	agent := NewDefaultAgent(func(cfg config.LLMConfig) (llm.Provider, error) {
		return prov, nil
	})

	ws := config.WorkspaceConfig{
		Name:     "test-ws",
		RootPath: tmp,
	}

	res, err := agent.Run(context.Background(), &RunRequest{
		QueryID:   "qry_refuse_prior",
		Query:     "read and evaluate",
		Workspace: ws,
	})
	if err != nil {
		t.Fatalf("agent.Run failed: %v", err)
	}

	if res.Status != protocol.QueryStatusRefused {
		t.Fatalf("expected status 'refused', got: %s", res.Status)
	}
	if !strings.Contains(res.Answer, "Confidential repository area cannot be disclosed") {
		t.Errorf("unexpected refusal answer: %s", res.Answer)
	}
	// Tools used must reflect what actually ran prior to refusal
	if len(res.ToolsUsed) != 1 || res.ToolsUsed[0] != "read_file" {
		t.Fatalf("expected tools_used to be ['read_file'], got: %v", res.ToolsUsed)
	}
	if res.TokenUsage.TotalTokens != 115 {
		t.Errorf("expected 115 total tokens, got: %d", res.TokenUsage.TotalTokens)
	}
}

func TestProbeAgent_NormalAnswer_Completed(t *testing.T) {
	tmp := t.TempDir()

	prov := &scriptedProvider{
		responses: []*llm.ChatResponse{
			{
				Content: "All services are running normally on main branch.",
				Usage:   protocol.TokenUsage{PromptTokens: 40, CompletionTokens: 15, TotalTokens: 55},
			},
		},
	}

	agent := NewDefaultAgent(func(cfg config.LLMConfig) (llm.Provider, error) {
		return prov, nil
	})

	ws := config.WorkspaceConfig{
		Name:     "test-ws",
		RootPath: tmp,
	}

	res, err := agent.Run(context.Background(), &RunRequest{
		QueryID:   "qry_normal",
		Query:     "how is the service?",
		Workspace: ws,
	})
	if err != nil {
		t.Fatalf("agent.Run failed: %v", err)
	}

	if res.Status != protocol.QueryStatusCompleted {
		t.Fatalf("expected status 'completed', got: %s", res.Status)
	}
	if res.Answer != "All services are running normally on main branch." {
		t.Errorf("unexpected answer: %s", res.Answer)
	}
	if len(res.ToolsUsed) != 0 {
		t.Errorf("expected 0 tools used, got: %v", res.ToolsUsed)
	}
}

func TestProbeAgent_TextRefusalFallback(t *testing.T) {
	tmp := t.TempDir()

	prov := &scriptedProvider{
		responses: []*llm.ChatResponse{
			{
				Content: "REFUSED: The requested file contains confidential credentials sk-proj-abcdef1234567890abcdef1234567890.",
				Usage:   protocol.TokenUsage{PromptTokens: 40, CompletionTokens: 15, TotalTokens: 55},
			},
		},
	}

	agent := NewDefaultAgent(func(cfg config.LLMConfig) (llm.Provider, error) {
		return prov, nil
	})

	ws := config.WorkspaceConfig{
		Name:     "test-ws",
		RootPath: tmp,
	}

	res, err := agent.Run(context.Background(), &RunRequest{
		QueryID:   "qry_text_refusal",
		Query:     "show me secrets",
		Workspace: ws,
	})
	if err != nil {
		t.Fatalf("agent.Run failed: %v", err)
	}

	if res.Status != protocol.QueryStatusRefused {
		t.Fatalf("expected status 'refused', got: %s", res.Status)
	}
	if strings.HasPrefix(res.Answer, "REFUSED:") {
		t.Errorf("refusal answer should strip prefix, got: %s", res.Answer)
	}
	if strings.Contains(res.Answer, "sk-proj-abcdef1234567890abcdef1234567890") {
		t.Errorf("secret token was not redacted: %s", res.Answer)
	}
	if !strings.Contains(res.Answer, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in answer, got: %s", res.Answer)
	}
}

func TestProbeAgent_FullLoopAnthropic_RefuseTool(t *testing.T) {
	tmp := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_anth_refuse",
			"type": "message",
			"role": "assistant",
			"content": [
				{
					"type": "thinking",
					"thinking": "Checking workspace privacy rules"
				},
				{
					"type": "tool_use",
					"id": "toolu_refuse_01",
					"name": "refuse",
					"input": {
						"reason": "Branch details for feature/auth-v2 are confidential. Server at 192.168.1.50 cannot be queried."
					}
				}
			],
			"stop_reason": "tool_use",
			"usage": {
				"input_tokens": 100,
				"output_tokens": 25
			}
		}`))
	}))
	defer srv.Close()

	llmCfg := config.LLMConfig{
		Provider:  llm.DialectAnthropic,
		BaseURL:   srv.URL,
		APIKey:    "test-anthropic-key",
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 1000,
	}

	agent := NewDefaultAgent(nil)
	ws := config.WorkspaceConfig{
		Name:     "anth-refuse-ws",
		RootPath: tmp,
	}

	res, err := agent.Run(context.Background(), &RunRequest{
		QueryID:   "qry_anth_refuse",
		Query:     "what is Bob doing on auth-v2?",
		Workspace: ws,
		LLMConfig: llmCfg,
	})
	if err != nil {
		t.Fatalf("anthropic agent.Run failed: %v", err)
	}

	if res.Status != protocol.QueryStatusRefused {
		t.Fatalf("expected status 'refused', got: %s", res.Status)
	}
	if strings.Contains(res.Answer, "192.168.1.50") {
		t.Errorf("private IP leaked in Anthropic refusal: %s", res.Answer)
	}
	if !strings.Contains(res.Answer, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in Anthropic refusal answer, got: %s", res.Answer)
	}
	if !strings.Contains(res.Answer, "Branch details for feature/auth-v2 are confidential") {
		t.Errorf("expected refusal text in answer, got: %s", res.Answer)
	}
}
