package mockllm_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sskift/talkintent/test/mockllm"
)

func TestOpenAISimpleCompletion(t *testing.T) {
	s := mockllm.NewServer()
	defer s.Close()

	reqBody := map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]any{
			{"role": "user", "content": "What is the workspace status?"},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequest(http.MethodPost, s.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test-mock-key-1234567890")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	choices, ok := data["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("expected choices in response: %v", data)
	}
	firstChoice := choices[0].(map[string]any)
	msg := firstChoice["message"].(map[string]any)
	content := msg["content"].(string)

	if !strings.Contains(content, "Mock LLM answer") {
		t.Errorf("expected default canned response, got %q", content)
	}

	usage, ok := data["usage"].(map[string]any)
	if !ok || usage["total_tokens"] == nil {
		t.Errorf("expected usage counts, got %v", data["usage"])
	}

	// Verify call recording and credential masking (Rule 5)
	if s.CallsCount() != 1 {
		t.Errorf("expected 1 call recorded, got %d", s.CallsCount())
	}
	lastCall, ok := s.LastCall()
	if !ok {
		t.Fatalf("expected last call to exist")
	}
	authHeader := lastCall.Header.Get("Authorization")
	if strings.Contains(authHeader, "sk-test-mock-key") {
		t.Errorf("raw credential leaked in recorded header: %s", authHeader)
	}
	if !strings.Contains(authHeader, "Bearer [len=") {
		t.Errorf("expected masked bearer token, got %s", authHeader)
	}
}

func TestOpenAIScriptedToolLoop(t *testing.T) {
	s := mockllm.NewServer()
	defer s.Close()

	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "git_status",
				"description": "Show status",
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "git_diff",
				"description": "Show diff",
			},
		},
	}

	// --- Step 1: Initial query with tools available ---
	messages := []map[string]any{
		{"role": "user", "content": "How is the login refactor going?"},
	}

	step1Req := map[string]any{
		"model":    "gpt-4o",
		"messages": messages,
		"tools":    tools,
	}
	bodyBytes, _ := json.Marshal(step1Req)

	resp, err := http.Post(s.URL+"/v1/chat/completions", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("step 1 request failed: %v", err)
	}
	defer resp.Body.Close()

	var step1Data map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&step1Data)
	choices := step1Data["choices"].([]any)
	firstChoice := choices[0].(map[string]any)
	if firstChoice["finish_reason"] != "tool_calls" {
		t.Fatalf("expected finish_reason 'tool_calls', got %v", firstChoice["finish_reason"])
	}
	msg := firstChoice["message"].(map[string]any)
	toolCalls := msg["tool_calls"].([]any)
	firstCall := toolCalls[0].(map[string]any)
	fn := firstCall["function"].(map[string]any)
	if fn["name"] != "git_status" {
		t.Fatalf("expected first scripted tool to be git_status, got %v", fn["name"])
	}
	callID1 := firstCall["id"].(string)

	// --- Step 2: Provide git_status output, expect git_diff ---
	messages = append(messages, msg)
	gitStatusOutput := "On branch feat/login-refactor\nChanges not staged for commit:\n  modified:   internal/auth/login.go\n  modified:   internal/auth/session.go\n"
	messages = append(messages, map[string]any{
		"role":         "tool",
		"tool_call_id": callID1,
		"content":      gitStatusOutput,
	})

	step2Req := map[string]any{
		"model":    "gpt-4o",
		"messages": messages,
		"tools":    tools,
	}
	bodyBytes, _ = json.Marshal(step2Req)

	resp2, err := http.Post(s.URL+"/v1/chat/completions", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("step 2 request failed: %v", err)
	}
	defer resp2.Body.Close()

	var step2Data map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&step2Data)
	choices2 := step2Data["choices"].([]any)
	firstChoice2 := choices2[0].(map[string]any)
	if firstChoice2["finish_reason"] != "tool_calls" {
		t.Fatalf("expected finish_reason 'tool_calls', got %v", firstChoice2["finish_reason"])
	}
	msg2 := firstChoice2["message"].(map[string]any)
	toolCalls2 := msg2["tool_calls"].([]any)
	secondCall := toolCalls2[0].(map[string]any)
	fn2 := secondCall["function"].(map[string]any)
	if fn2["name"] != "git_diff" {
		t.Fatalf("expected second scripted tool to be git_diff, got %v", fn2["name"])
	}
	callID2 := secondCall["id"].(string)

	// --- Step 3: Provide git_diff output, expect final synthesized answer ---
	messages = append(messages, msg2)
	gitDiffOutput := "diff --git a/internal/auth/login.go b/internal/auth/login.go\n--- a/internal/auth/login.go\n+++ b/internal/auth/login.go\n@@ -10,3 +10,5 @@\n"
	messages = append(messages, map[string]any{
		"role":         "tool",
		"tool_call_id": callID2,
		"content":      gitDiffOutput,
	})

	step3Req := map[string]any{
		"model":    "gpt-4o",
		"messages": messages,
		"tools":    tools,
	}
	bodyBytes, _ = json.Marshal(step3Req)

	resp3, err := http.Post(s.URL+"/v1/chat/completions", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("step 3 request failed: %v", err)
	}
	defer resp3.Body.Close()

	var step3Data map[string]any
	_ = json.NewDecoder(resp3.Body).Decode(&step3Data)
	choices3 := step3Data["choices"].([]any)
	firstChoice3 := choices3[0].(map[string]any)
	if firstChoice3["finish_reason"] != "stop" {
		t.Fatalf("expected finish_reason 'stop', got %v", firstChoice3["finish_reason"])
	}
	msg3 := firstChoice3["message"].(map[string]any)
	finalAnswer := msg3["content"].(string)

	// Verify that final synthesized answer quotes branch name and modified files!
	if !strings.Contains(finalAnswer, "feat/login-refactor") {
		t.Errorf("expected final answer to quote branch 'feat/login-refactor', got %q", finalAnswer)
	}
	if !strings.Contains(finalAnswer, "internal/auth/login.go") {
		t.Errorf("expected final answer to quote changed file 'internal/auth/login.go', got %q", finalAnswer)
	}
	if !strings.Contains(finalAnswer, "internal/auth/session.go") {
		t.Errorf("expected final answer to quote changed file 'internal/auth/session.go', got %q", finalAnswer)
	}
}

func TestAnthropicScriptedToolLoopWithThinking(t *testing.T) {
	s := mockllm.NewServerWithOptions(
		mockllm.WithThinking(true),
		mockllm.WithThinkingContent("Inspecting live workspace status and recent git commits..."),
	)
	defer s.Close()

	tools := []map[string]any{
		{
			"name":        "git_status",
			"description": "Check git status",
			"input_schema": map[string]any{
				"type": "object",
			},
		},
		{
			"name":        "git_diff",
			"description": "Check git diff",
			"input_schema": map[string]any{
				"type": "object",
			},
		},
	}

	// --- Step 1: Initial query ---
	messages := []map[string]any{
		{"role": "user", "content": "What is Zhang San currently working on?"},
	}

	step1Req := map[string]any{
		"model":    "claude-3-7-sonnet",
		"system":   "You are the TalkIntent developer probe agent.",
		"messages": messages,
		"tools":    tools,
	}
	bodyBytes, _ := json.Marshal(step1Req)

	req1, _ := http.NewRequest(http.MethodPost, s.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("x-api-key", "anthropic-mock-key-abcdef123456")

	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("step 1 request failed: %v", err)
	}
	defer resp1.Body.Close()

	var step1Data map[string]any
	_ = json.NewDecoder(resp1.Body).Decode(&step1Data)
	if step1Data["stop_reason"] != "tool_use" {
		t.Fatalf("expected stop_reason 'tool_use', got %v", step1Data["stop_reason"])
	}

	contentBlocks := step1Data["content"].([]any)
	if len(contentBlocks) < 2 {
		t.Fatalf("expected at least 2 content blocks (thinking + tool_use), got %d", len(contentBlocks))
	}

	// Block 0 must be thinking block
	b0 := contentBlocks[0].(map[string]any)
	if b0["type"] != "thinking" {
		t.Errorf("expected block 0 to be 'thinking', got %v", b0["type"])
	}
	if !strings.Contains(b0["thinking"].(string), "workspace") {
		t.Errorf("expected thinking text, got %v", b0["thinking"])
	}

	// Block 1 must be tool_use for git_status
	b1 := contentBlocks[1].(map[string]any)
	if b1["type"] != "tool_use" {
		t.Errorf("expected block 1 to be 'tool_use', got %v", b1["type"])
	}
	if b1["name"] != "git_status" {
		t.Errorf("expected tool_use name 'git_status', got %v", b1["name"])
	}
	toolUseID1 := b1["id"].(string)

	// --- Step 2: Return tool_result for git_status ---
	messages = append(messages, map[string]any{
		"role":    "assistant",
		"content": contentBlocks,
	})
	messages = append(messages, map[string]any{
		"role": "user",
		"content": []map[string]any{
			{
				"type":        "tool_result",
				"tool_use_id": toolUseID1,
				"content":     "On branch feature/auth-v2\nChanges not staged for commit:\n  modified:   token.go\n",
			},
		},
	})

	step2Req := map[string]any{
		"model":    "claude-3-7-sonnet",
		"messages": messages,
		"tools":    tools,
	}
	bodyBytes, _ = json.Marshal(step2Req)

	req2, _ := http.NewRequest(http.MethodPost, s.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("step 2 request failed: %v", err)
	}
	defer resp2.Body.Close()

	var step2Data map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&step2Data)
	if step2Data["stop_reason"] != "tool_use" {
		t.Fatalf("expected stop_reason 'tool_use', got %v", step2Data["stop_reason"])
	}

	contentBlocks2 := step2Data["content"].([]any)
	// Find tool_use block
	var toolUse2 map[string]any
	for _, b := range contentBlocks2 {
		bm := b.(map[string]any)
		if bm["type"] == "tool_use" {
			toolUse2 = bm
			break
		}
	}
	if toolUse2 == nil || toolUse2["name"] != "git_diff" {
		t.Fatalf("expected tool_use git_diff, got %v", toolUse2)
	}
	toolUseID2 := toolUse2["id"].(string)

	// --- Step 3: Return tool_result for git_diff, expect final answer quoting results ---
	messages = append(messages, map[string]any{
		"role":    "assistant",
		"content": contentBlocks2,
	})
	messages = append(messages, map[string]any{
		"role": "user",
		"content": []map[string]any{
			{
				"type":        "tool_result",
				"tool_use_id": toolUseID2,
				"content":     "diff --git a/token.go b/token.go\n--- a/token.go\n+++ b/token.go\n@@ -1,3 +1,4 @@\n+package token\n",
			},
		},
	})

	step3Req := map[string]any{
		"model":    "claude-3-7-sonnet",
		"messages": messages,
		"tools":    tools,
	}
	bodyBytes, _ = json.Marshal(step3Req)

	req3, _ := http.NewRequest(http.MethodPost, s.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("step 3 request failed: %v", err)
	}
	defer resp3.Body.Close()

	var step3Data map[string]any
	_ = json.NewDecoder(resp3.Body).Decode(&step3Data)
	if step3Data["stop_reason"] != "end_turn" {
		t.Fatalf("expected stop_reason 'end_turn', got %v", step3Data["stop_reason"])
	}

	contentBlocks3 := step3Data["content"].([]any)
	var finalAnswer string
	for _, b := range contentBlocks3 {
		bm := b.(map[string]any)
		if bm["type"] == "text" {
			finalAnswer = bm["text"].(string)
		}
	}

	if !strings.Contains(finalAnswer, "feature/auth-v2") {
		t.Errorf("expected final answer to quote branch 'feature/auth-v2', got %q", finalAnswer)
	}
	if !strings.Contains(finalAnswer, "token.go") {
		t.Errorf("expected final answer to quote modified file 'token.go', got %q", finalAnswer)
	}

	// Verify Anthropic token usage
	usage, ok := step3Data["usage"].(map[string]any)
	if !ok || usage["input_tokens"] == nil || usage["output_tokens"] == nil {
		t.Errorf("expected Anthropic input_tokens and output_tokens, got %v", step3Data["usage"])
	}
}

func TestRefusalKnob(t *testing.T) {
	s := mockllm.NewServerWithOptions(
		mockllm.WithRefusal("CONFIDENTIAL", "I am not allowed to discuss confidential topics."),
	)
	defer s.Close()

	// 1. OpenAI dialect refusal
	openaiReq := map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]any{
			{"role": "user", "content": "Tell me about the CONFIDENTIAL project."},
		},
		"tools": []map[string]any{
			{"type": "function", "function": map[string]any{"name": "git_status"}},
		},
	}
	bodyBytes, _ := json.Marshal(openaiReq)
	resp, err := http.Post(s.URL+"/v1/chat/completions", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("openai request failed: %v", err)
	}
	defer resp.Body.Close()

	var data map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&data)
	choices := data["choices"].([]any)
	firstChoice := choices[0].(map[string]any)
	if firstChoice["finish_reason"] != "stop" {
		t.Errorf("expected finish_reason stop on refusal, got %v", firstChoice["finish_reason"])
	}
	msg := firstChoice["message"].(map[string]any)
	if msg["content"] != "I am not allowed to discuss confidential topics." {
		t.Errorf("unexpected refusal content: %v", msg["content"])
	}

	// 2. Anthropic dialect refusal
	anthropicReq := map[string]any{
		"model": "claude-3-7-sonnet",
		"messages": []map[string]any{
			{"role": "user", "content": "Show me confidential file diffs please"},
		},
	}
	bodyBytes, _ = json.Marshal(anthropicReq)
	resp2, err := http.Post(s.URL+"/v1/messages", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("anthropic request failed: %v", err)
	}
	defer resp2.Body.Close()

	var data2 map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&data2)
	if data2["stop_reason"] != "end_turn" {
		t.Errorf("expected stop_reason end_turn on refusal, got %v", data2["stop_reason"])
	}
	cBlocks := data2["content"].([]any)
	tBlock := cBlocks[0].(map[string]any)
	if tBlock["text"] != "I am not allowed to discuss confidential topics." {
		t.Errorf("unexpected refusal text: %v", tBlock["text"])
	}
}

func TestRefusalKnob_StructuredToolCall(t *testing.T) {
	s := mockllm.NewServerWithOptions(
		mockllm.WithRefusal("CONFIDENTIAL", "I am not allowed to discuss confidential topics."),
	)
	defer s.Close()

	// 1. OpenAI dialect refusal with refuse tool offered
	openaiReq := map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]any{
			{"role": "user", "content": "Tell me about the CONFIDENTIAL project."},
		},
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "refuse",
					"description": "Refuse query",
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(openaiReq)
	resp, err := http.Post(s.URL+"/v1/chat/completions", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("openai request failed: %v", err)
	}
	defer resp.Body.Close()

	var data map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&data)
	choices := data["choices"].([]any)
	firstChoice := choices[0].(map[string]any)
	if firstChoice["finish_reason"] != "tool_calls" {
		t.Fatalf("expected finish_reason 'tool_calls' on refusal when refuse tool offered, got %v", firstChoice["finish_reason"])
	}
	msg := firstChoice["message"].(map[string]any)
	toolCalls := msg["tool_calls"].([]any)
	tc := toolCalls[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	if fn["name"] != "refuse" {
		t.Fatalf("expected tool name 'refuse', got %v", fn["name"])
	}
	var args struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
		t.Fatalf("failed to unmarshal refuse arguments: %v", err)
	}
	if args.Reason != "I am not allowed to discuss confidential topics." {
		t.Fatalf("unexpected refusal reason in tool call: %q", args.Reason)
	}

	// 2. Anthropic dialect refusal with refuse tool offered
	anthropicReq := map[string]any{
		"model": "claude-3-7-sonnet",
		"messages": []map[string]any{
			{"role": "user", "content": "Show me CONFIDENTIAL file diffs please"},
		},
		"tools": []map[string]any{
			{
				"name":        "refuse",
				"description": "Refuse query",
			},
		},
	}
	bodyBytes, _ = json.Marshal(anthropicReq)
	resp2, err := http.Post(s.URL+"/v1/messages", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("anthropic request failed: %v", err)
	}
	defer resp2.Body.Close()

	var data2 map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&data2)
	if data2["stop_reason"] != "tool_use" {
		t.Fatalf("expected stop_reason 'tool_use' on refusal when refuse tool offered, got %v", data2["stop_reason"])
	}
	cBlocks := data2["content"].([]any)
	var foundRefuse bool
	for _, b := range cBlocks {
		bm := b.(map[string]any)
		if bm["type"] == "tool_use" && bm["name"] == "refuse" {
			foundRefuse = true
			input := bm["input"].(map[string]any)
			if input["reason"] != "I am not allowed to discuss confidential topics." {
				t.Fatalf("unexpected Anthropic refusal reason: %v", input["reason"])
			}
		}
	}
	if !foundRefuse {
		t.Fatalf("expected tool_use block for 'refuse' in Anthropic response, got: %v", cBlocks)
	}
}

func TestErrorInjection500Once(t *testing.T) {
	s := mockllm.NewServerWithOptions(
		mockllm.WithFailOnce500(true),
	)
	defer s.Close()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`

	// First call should fail with 500
	resp1, err := http.Post(s.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("call 1 failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500 on first call, got %d", resp1.StatusCode)
	}

	var errBody map[string]any
	_ = json.NewDecoder(resp1.Body).Decode(&errBody)
	if errBody["error"] == nil {
		t.Errorf("expected error field in 500 response, got %v", errBody)
	}

	// Second call should succeed with 200 OK
	resp2, err := http.Post(s.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("call 2 failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 on second call, got %d", resp2.StatusCode)
	}
}

func TestAnthropicErrorInjection500(t *testing.T) {
	s := mockllm.NewServerWithOptions(
		mockllm.WithFailOnce500(true),
	)
	defer s.Close()

	reqBody := `{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"hello"}]}`

	resp, err := http.Post(s.URL+"/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", resp.StatusCode)
	}

	var data map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&data)
	if data["type"] != "error" {
		t.Errorf("expected type 'error' for Anthropic 500, got %v", data["type"])
	}
}

func TestLatencyKnob(t *testing.T) {
	s := mockllm.NewServerWithOptions(
		mockllm.WithLatency(80 * time.Millisecond),
	)
	defer s.Close()

	start := time.Now()
	resp, err := http.Post(s.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	if elapsed < 70*time.Millisecond {
		t.Errorf("expected latency >= 70ms, got %v", elapsed)
	}
}

func TestRequireAuth(t *testing.T) {
	s := mockllm.NewServerWithOptions(
		mockllm.WithRequireAuth(true),
	)
	defer s.Close()

	reqBody := `{"messages":[{"role":"user","content":"test"}]}`

	// 1. Missing auth header -> 401
	resp, err := http.Post(s.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}

	// 2. With x-api-key -> 200 OK
	reqWithKey, _ := http.NewRequest(http.MethodPost, s.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	reqWithKey.Header.Set("Content-Type", "application/json")
	reqWithKey.Header.Set("x-api-key", "my-valid-api-key")

	resp2, err := http.DefaultClient.Do(reqWithKey)
	if err != nil {
		t.Fatalf("request with x-api-key failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with x-api-key, got %d", resp2.StatusCode)
	}

	// 3. With Authorization: Bearer -> 200 OK
	reqWithBearer, _ := http.NewRequest(http.MethodPost, s.URL+"/v1/messages", strings.NewReader(reqBody))
	reqWithBearer.Header.Set("Content-Type", "application/json")
	reqWithBearer.Header.Set("Authorization", "Bearer sk-test-bearer-key")

	resp3, err := http.DefaultClient.Do(reqWithBearer)
	if err != nil {
		t.Fatalf("request with Bearer failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with Bearer, got %d", resp3.StatusCode)
	}
}

func TestHealthzAndModels(t *testing.T) {
	s := mockllm.NewServer()
	defer s.Close()

	// Healthz
	resp, err := http.Get(s.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for /healthz, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("expected status ok in /healthz, got %s", string(body))
	}

	// Models
	resp2, err := http.Get(s.URL + "/v1/models")
	if err != nil {
		t.Fatalf("models failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for /v1/models, got %d", resp2.StatusCode)
	}
}

func TestServerReset(t *testing.T) {
	s := mockllm.NewServer()
	defer s.Close()

	s.SetRefusal("TEST", "refusal text")
	s.SetFailOnce500(true)
	s.SetLatency(100 * time.Millisecond)

	_, _ = http.Post(s.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if s.CallsCount() == 0 {
		t.Fatalf("expected recorded calls before reset")
	}

	s.Reset()

	if s.CallsCount() != 0 {
		t.Errorf("expected 0 recorded calls after Reset, got %d", s.CallsCount())
	}
	if s.FailOnce500 {
		t.Errorf("expected FailOnce500 false after Reset")
	}
	if s.Latency != 0 {
		t.Errorf("expected Latency 0 after Reset")
	}
	if s.RefuseSubstring != "" {
		t.Errorf("expected RefuseSubstring empty after Reset")
	}
}

func TestConcurrentRequests(t *testing.T) {
	s := mockllm.NewServer()
	defer s.Close()

	var wg sync.WaitGroup
	concurrentCount := 20

	for i := 0; i < concurrentCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reqBody := fmt.Sprintf(`{"messages":[{"role":"user","content":"query %d"}]}`, idx)
			endpoint := "/v1/chat/completions"
			if idx%2 == 0 {
				endpoint = "/v1/messages"
			}
			resp, err := http.Post(s.URL+endpoint, "application/json", strings.NewReader(reqBody))
			if err != nil {
				t.Errorf("concurrent request %d failed: %v", idx, err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("request %d got status %d", idx, resp.StatusCode)
			}
		}(i)
	}

	wg.Wait()
	if s.CallsCount() != concurrentCount {
		t.Errorf("expected %d recorded calls, got %d", concurrentCount, s.CallsCount())
	}
}
