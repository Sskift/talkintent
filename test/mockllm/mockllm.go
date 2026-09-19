package mockllm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Default constants for Mock LLM server configuration.
const (
	DefaultCannedResponse  = "Mock LLM answer: workspace is clean on branch main."
	DefaultRefusalText     = "Refusal: query contains restricted terms or violates privacy guardrails."
	DefaultThinkingContent = "Analyzing workspace git status and tool outputs..."

	ModeAuto      = "auto"
	ModeAll       = "all"
	ModeOpenAI    = "openai"
	ModeAnthropic = "anthropic"
)

var defaultScriptedTools = []string{"git_status", "git_diff"}

// Server is a test HTTP server mimicking OpenAI and Anthropic endpoints.
type Server struct {
	*httptest.Server
	mu sync.Mutex

	// Public fields preserved from skeleton compile contract
	RecordedCalls  []RecordedRequest
	CannedResponse string
	ToolToCall     string
	ToolArgs       string

	// Configuration knobs
	ForceCannedResponse bool
	RefuseSubstring     string
	RefusalText         string
	EmitThinkingBlock   bool
	ThinkingContent     string
	Latency             time.Duration
	FailOnce500         bool
	Fail500Count        int
	ScriptedTools       []string
	RequireAuth         bool
	Mode                string

	// Runtime metrics
	TotalRequests  int
	Total500Errors int
	TotalRefusals  int
	TotalToolCalls int
}

// RecordedRequest logs an incoming LLM invocation.
type RecordedRequest struct {
	Path       string         `json:"path"`
	Method     string         `json:"method"`
	Header     http.Header    `json:"header"`
	Body       map[string]any `json:"body"`
	ApiKeyMask string         `json:"api_key_mask,omitempty"`
}

// Config specifies initialization options for the mock server.
type Config struct {
	CannedResponse      string
	ForceCannedResponse bool
	ToolToCall          string
	ToolArgs            string
	RefuseSubstring     string
	RefusalText         string
	EmitThinkingBlock   bool
	ThinkingContent     string
	Latency             time.Duration
	FailOnce500         bool
	Fail500Count        int
	ScriptedTools       []string
	RequireAuth         bool
	Mode                string
}

// Option configures Server settings.
type Option func(*Config)

// WithCannedResponse sets the canned response.
func WithCannedResponse(resp string) Option {
	return func(c *Config) {
		c.CannedResponse = resp
	}
}

// WithForceCannedResponse forces the canned response to always be returned even after tool calls.
func WithForceCannedResponse(force bool) Option {
	return func(c *Config) {
		c.ForceCannedResponse = force
	}
}

// WithRefusal configures substring-triggered refusal.
func WithRefusal(substring, refusalText string) Option {
	return func(c *Config) {
		c.RefuseSubstring = substring
		if refusalText != "" {
			c.RefusalText = refusalText
		}
	}
}

// WithThinking configures whether to emit an Anthropic thinking block before tool_use.
func WithThinking(emit bool) Option {
	return func(c *Config) {
		c.EmitThinkingBlock = emit
	}
}

// WithThinkingContent sets the content of the thinking block.
func WithThinkingContent(content string) Option {
	return func(c *Config) {
		c.ThinkingContent = content
	}
}

// WithLatency configures simulated latency.
func WithLatency(d time.Duration) Option {
	return func(c *Config) {
		c.Latency = d
	}
}

// WithFailOnce500 injects a single HTTP 500 error on the next request.
func WithFailOnce500(fail bool) Option {
	return func(c *Config) {
		c.FailOnce500 = fail
	}
}

// WithFail500Count sets the exact number of HTTP 500 errors to inject.
func WithFail500Count(count int) Option {
	return func(c *Config) {
		c.Fail500Count = count
	}
}

// WithScriptedTools sets the ordered list of tools to invoke sequentially.
func WithScriptedTools(tools ...string) Option {
	return func(c *Config) {
		c.ScriptedTools = append([]string(nil), tools...)
	}
}

// WithRequireAuth configures whether requests require Authorization or x-api-key headers.
func WithRequireAuth(require bool) Option {
	return func(c *Config) {
		c.RequireAuth = require
	}
}

// WithMode sets the dialect mode ("auto", "openai", "anthropic").
func WithMode(mode string) Option {
	return func(c *Config) {
		c.Mode = mode
	}
}

// WithToolToCall sets a specific tool to invoke.
func WithToolToCall(name, args string) Option {
	return func(c *Config) {
		c.ToolToCall = name
		c.ToolArgs = args
	}
}

// DefaultConfig returns baseline configuration.
func DefaultConfig() Config {
	return Config{
		CannedResponse:  DefaultCannedResponse,
		RefusalText:     DefaultRefusalText,
		ThinkingContent: DefaultThinkingContent,
		ScriptedTools:   append([]string(nil), defaultScriptedTools...),
		Mode:            ModeAuto,
	}
}

// NewServerState creates a Server instance without an active httptest.Server listener.
// Useful for standalone binary or custom HTTP listeners.
func NewServerState(cfg Config) *Server {
	if cfg.CannedResponse == "" {
		cfg.CannedResponse = DefaultCannedResponse
	}
	if cfg.RefusalText == "" {
		cfg.RefusalText = DefaultRefusalText
	}
	if cfg.ThinkingContent == "" {
		cfg.ThinkingContent = DefaultThinkingContent
	}
	if len(cfg.ScriptedTools) == 0 {
		cfg.ScriptedTools = append([]string(nil), defaultScriptedTools...)
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeAuto
	}

	return &Server{
		CannedResponse:      cfg.CannedResponse,
		ForceCannedResponse: cfg.ForceCannedResponse,
		ToolToCall:          cfg.ToolToCall,
		ToolArgs:            cfg.ToolArgs,
		RefuseSubstring:     cfg.RefuseSubstring,
		RefusalText:         cfg.RefusalText,
		EmitThinkingBlock:   cfg.EmitThinkingBlock,
		ThinkingContent:     cfg.ThinkingContent,
		Latency:             cfg.Latency,
		FailOnce500:         cfg.FailOnce500,
		Fail500Count:        cfg.Fail500Count,
		ScriptedTools:       cfg.ScriptedTools,
		RequireAuth:         cfg.RequireAuth,
		Mode:                cfg.Mode,
	}
}

// NewServerWithConfig creates and starts an httptest.Server from a Config.
func NewServerWithConfig(cfg Config) *Server {
	s := NewServerState(cfg)
	s.Server = httptest.NewServer(s.Handler())
	return s
}

// NewServerWithOptions creates and starts an httptest.Server using functional options.
func NewServerWithOptions(opts ...Option) *Server {
	cfg := DefaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return NewServerWithConfig(cfg)
}

// NewServer creates and starts a new MockLLM test server with default settings.
// Preserves existing skeleton compile and behavior contract.
func NewServer() *Server {
	return NewServerWithOptions()
}

// Handler returns an http.Handler implementing the mock endpoints and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/models", s.handleModels)

	switch s.Mode {
	case ModeOpenAI:
		mux.HandleFunc("/v1/chat/completions", s.handleOpenAI)
	case ModeAnthropic:
		mux.HandleFunc("/v1/messages", s.handleAnthropic)
	default:
		mux.HandleFunc("/v1/chat/completions", s.handleOpenAI)
		mux.HandleFunc("/v1/messages", s.handleAnthropic)
	}

	return s.wrapMiddleware(mux)
}

// wrapMiddleware handles latency, error injection, auth enforcement, and call recording.
func (s *Server) wrapMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health checks bypass latency and error injection
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		s.mu.Lock()
		latency := s.Latency
		failOnce := s.FailOnce500
		failCount := s.Fail500Count
		if failOnce {
			s.FailOnce500 = false
		}
		if failCount > 0 {
			s.Fail500Count--
		}
		s.mu.Unlock()

		if latency > 0 {
			select {
			case <-time.After(latency):
			case <-r.Context().Done():
				return
			}
		}

		if failOnce || failCount > 0 {
			s.mu.Lock()
			s.Total500Errors++
			s.TotalRequests++
			rawAuthToken := extractAuthToken(r)
			s.RecordedCalls = append(s.RecordedCalls, RecordedRequest{
				Path:       r.URL.Path,
				Method:     r.Method,
				Header:     sanitizeHeaders(r.Header),
				ApiKeyMask: maskToken(rawAuthToken),
			})
			s.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			if r.URL.Path == "/v1/messages" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"type": "error",
					"error": map[string]any{
						"type":    "api_error",
						"message": "injected mock error: simulated upstream server failure",
					},
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"message": "injected mock error: simulated upstream server failure",
						"type":    "server_error",
						"code":    500,
					},
				})
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleHealthz responds with HTTP 200 for health checks.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// handleModels returns a list of mock models for OpenAI-compatible clients.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": "gpt-4o", "object": "model", "owned_by": "mockllm"},
			{"id": "claude-3-7-sonnet", "object": "model", "owned_by": "mockllm"},
		},
	})
}

func (s *Server) handleOpenAI(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.TotalRequests++

	// Check authentication if required
	rawAuthToken := extractAuthToken(r)
	if s.RequireAuth && rawAuthToken == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "missing valid api key in Authorization or x-api-key header",
				"type":    "invalid_request_error",
				"code":    401,
			},
		})
		return
	}

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	s.RecordedCalls = append(s.RecordedCalls, RecordedRequest{
		Path:       r.URL.Path,
		Method:     r.Method,
		Header:     sanitizeHeaders(r.Header),
		Body:       body,
		ApiKeyMask: maskToken(rawAuthToken),
	})

	w.Header().Set("Content-Type", "application/json")

	state := parseOpenAIConversation(body)

	// Check query refusal knob
	if s.RefuseSubstring != "" && containsSubstring(state.userQueries, s.RefuseSubstring) {
		s.TotalRefusals++
		if containsTool(state.offeredTools, "refuse") {
			s.TotalToolCalls++
			refuseArgs, _ := json.Marshal(map[string]string{"reason": s.RefusalText})
			s.sendOpenAIToolCall(w, "refuse", string(refuseArgs), state)
			return
		}
		s.sendOpenAIText(w, s.RefusalText, state)
		return
	}

	// Check explicit ToolToCall override (from skeleton contract)
	if s.ToolToCall != "" {
		s.TotalToolCalls++
		args := s.ToolArgs
		if args == "" {
			args = "{}"
		}
		s.sendOpenAIToolCall(w, s.ToolToCall, args, state)
		return
	}

	// If no tools are offered, return direct answer
	if len(state.offeredTools) == 0 {
		ans := s.determineFinalAnswer(state)
		s.sendOpenAIText(w, ans, state)
		return
	}

	// Determine next tool to call from scripted tools or offered tools
	nextTool := s.selectNextTool(state)
	if nextTool != "" {
		s.TotalToolCalls++
		args := defaultToolArgs(nextTool)
		s.sendOpenAIToolCall(w, nextTool, args, state)
		return
	}

	// All planned tools called; synthesize final answer quoting tool results
	ans := s.determineFinalAnswer(state)
	s.sendOpenAIText(w, ans, state)
}

func (s *Server) handleAnthropic(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.TotalRequests++

	// Check authentication if required
	rawAuthToken := extractAuthToken(r)
	if s.RequireAuth && rawAuthToken == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "authentication_error",
				"message": "missing valid api key in x-api-key or Authorization header",
			},
		})
		return
	}

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	s.RecordedCalls = append(s.RecordedCalls, RecordedRequest{
		Path:       r.URL.Path,
		Method:     r.Method,
		Header:     sanitizeHeaders(r.Header),
		Body:       body,
		ApiKeyMask: maskToken(rawAuthToken),
	})

	w.Header().Set("Content-Type", "application/json")

	state := parseAnthropicConversation(body)

	// Check query refusal knob
	if s.RefuseSubstring != "" && containsSubstring(state.userQueries, s.RefuseSubstring) {
		s.TotalRefusals++
		if containsTool(state.offeredTools, "refuse") {
			s.TotalToolCalls++
			argsMap := map[string]any{"reason": s.RefusalText}
			s.sendAnthropicToolCall(w, "refuse", argsMap, state)
			return
		}
		s.sendAnthropicText(w, s.RefusalText, state)
		return
	}

	// Check explicit ToolToCall override
	if s.ToolToCall != "" {
		s.TotalToolCalls++
		argsMap := parseArgsToMap(s.ToolArgs)
		s.sendAnthropicToolCall(w, s.ToolToCall, argsMap, state)
		return
	}

	// If no tools are offered, return direct answer
	if len(state.offeredTools) == 0 {
		ans := s.determineFinalAnswer(state)
		s.sendAnthropicText(w, ans, state)
		return
	}

	// Determine next tool to call from scripted tools or offered tools
	nextTool := s.selectNextTool(state)
	if nextTool != "" {
		s.TotalToolCalls++
		argsMap := defaultToolArgsMap(nextTool)
		s.sendAnthropicToolCall(w, nextTool, argsMap, state)
		return
	}

	// All planned tools called; synthesize final answer quoting tool results
	ans := s.determineFinalAnswer(state)
	s.sendAnthropicText(w, ans, state)
}

// selectNextTool picks the next tool to execute in the deterministic sequence.
func (s *Server) selectNextTool(state conversationState) string {
	calledSet := make(map[string]bool)
	for _, name := range state.calledTools {
		calledSet[name] = true
	}

	offeredSet := make(map[string]bool)
	for _, name := range state.offeredTools {
		offeredSet[name] = true
	}

	// 1. Try scripted tools in order
	var hasScriptedMatch bool
	for _, tool := range s.ScriptedTools {
		if offeredSet[tool] {
			hasScriptedMatch = true
			if !calledSet[tool] {
				return tool
			}
		}
	}
	if hasScriptedMatch {
		return ""
	}

	// 2. If none of the scripted tools matched offered tools, but client offered other tools,
	// run the first uncalled offered tool (excluding control tools like "refuse")
	for _, tool := range state.offeredTools {
		if tool == "refuse" {
			continue
		}
		if !calledSet[tool] {
			return tool
		}
	}

	return ""
}

// determineFinalAnswer produces the text answer, synthesizing tool findings or using canned answer.
func (s *Server) determineFinalAnswer(state conversationState) string {
	if s.ForceCannedResponse {
		return s.CannedResponse
	}
	if s.CannedResponse != "" && s.CannedResponse != DefaultCannedResponse {
		return s.CannedResponse
	}
	if len(state.toolOutputs) > 0 {
		return synthesizeToolFindings(state.toolOutputs)
	}
	return s.CannedResponse
}

func (s *Server) sendOpenAIToolCall(w http.ResponseWriter, toolName, args string, state conversationState) {
	callID := fmt.Sprintf("call_mock_%s_%d", toolName, time.Now().UnixNano()%1000000)
	resp := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-mock-tool-%d", time.Now().UnixNano()%1000000),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []any{
						map[string]any{
							"id":   callID,
							"type": "function",
							"function": map[string]any{
								"name":      toolName,
								"arguments": args,
							},
						},
					},
				},
				"finish_reason": "tool_calls",
			},
		},
		"usage": calculateUsage(state, 25),
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) sendOpenAIText(w http.ResponseWriter, content string, state conversationState) {
	resp := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-mock-ans-%d", time.Now().UnixNano()%1000000),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": calculateUsage(state, len(content)/4+15),
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) sendAnthropicToolCall(w http.ResponseWriter, toolName string, input map[string]any, state conversationState) {
	callID := fmt.Sprintf("toolu_mock_%s_%d", toolName, time.Now().UnixNano()%1000000)
	contentBlocks := make([]any, 0, 2)

	// Emit thinking block before tool_use if configured
	if s.EmitThinkingBlock {
		contentBlocks = append(contentBlocks, map[string]any{
			"type":     "thinking",
			"thinking": s.ThinkingContent,
		})
	}

	contentBlocks = append(contentBlocks, map[string]any{
		"type":  "tool_use",
		"id":    callID,
		"name":  toolName,
		"input": input,
	})

	resp := map[string]any{
		"id":          fmt.Sprintf("msg_mock_anth_tool_%d", time.Now().UnixNano()%1000000),
		"type":        "message",
		"role":        "assistant",
		"stop_reason": "tool_use",
		"content":     contentBlocks,
		"usage":       calculateAnthropicUsage(state, 30),
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) sendAnthropicText(w http.ResponseWriter, text string, state conversationState) {
	contentBlocks := make([]any, 0, 2)
	if s.EmitThinkingBlock {
		contentBlocks = append(contentBlocks, map[string]any{
			"type":     "thinking",
			"thinking": "Synthesizing workspace report from tool observations...",
		})
	}

	contentBlocks = append(contentBlocks, map[string]any{
		"type": "text",
		"text": text,
	})

	resp := map[string]any{
		"id":          fmt.Sprintf("msg_mock_anth_ans-%d", time.Now().UnixNano()%1000000),
		"type":        "message",
		"role":        "assistant",
		"stop_reason": "end_turn",
		"content":     contentBlocks,
		"usage":       calculateAnthropicUsage(state, len(text)/4+20),
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// Reset clears recorded calls and restores server configuration.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.RecordedCalls = nil
	s.ToolToCall = ""
	s.ToolArgs = ""
	s.ForceCannedResponse = false
	s.RefuseSubstring = ""
	s.RefusalText = DefaultRefusalText
	s.EmitThinkingBlock = false
	s.ThinkingContent = DefaultThinkingContent
	s.Latency = 0
	s.FailOnce500 = false
	s.Fail500Count = 0
	s.ScriptedTools = append([]string(nil), defaultScriptedTools...)
	s.TotalRequests = 0
	s.Total500Errors = 0
	s.TotalRefusals = 0
	s.TotalToolCalls = 0
	s.CannedResponse = DefaultCannedResponse
}

// CallsCount returns the number of recorded HTTP calls.
func (s *Server) CallsCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.RecordedCalls)
}

// LastCall returns the most recent recorded request.
func (s *Server) LastCall() (RecordedRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.RecordedCalls) == 0 {
		return RecordedRequest{}, false
	}
	return s.RecordedCalls[len(s.RecordedCalls)-1], true
}

// SetFailOnce500 configures a single HTTP 500 error injection.
func (s *Server) SetFailOnce500(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FailOnce500 = fail
}

// SetRefusal configures substring refusal.
func (s *Server) SetRefusal(substring, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RefuseSubstring = substring
	if text != "" {
		s.RefusalText = text
	}
}

// SetCannedResponse configures the canned answer.
func (s *Server) SetCannedResponse(resp string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CannedResponse = resp
}

// SetThinking configures Anthropic thinking block emission.
func (s *Server) SetThinking(emit bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.EmitThinkingBlock = emit
}

// SetLatency configures response latency.
func (s *Server) SetLatency(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Latency = d
}

// conversationState holds parsed details of an ongoing LLM dialogue.
type conversationState struct {
	offeredTools []string
	userQueries  []string
	calledTools  []string
	toolOutputs  map[string]string
}

func parseOpenAIConversation(body map[string]any) conversationState {
	state := conversationState{
		toolOutputs: make(map[string]string),
	}

	// Parse offered tools
	if toolsRaw, ok := body["tools"].([]any); ok {
		for _, t := range toolsRaw {
			if tm, ok := t.(map[string]any); ok {
				if fn, ok := tm["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok && name != "" {
						state.offeredTools = append(state.offeredTools, name)
					}
				}
			}
		}
	}

	toolCallIDToName := make(map[string]string)

	// Parse messages history
	if msgsRaw, ok := body["messages"].([]any); ok {
		for _, m := range msgsRaw {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role, _ := mm["role"].(string)

			// Collect user and system query text
			if role == "user" || role == "system" {
				if c, ok := mm["content"].(string); ok && c != "" {
					state.userQueries = append(state.userQueries, c)
				}
			}

			// Collect tool calls requested by assistant
			if role == "assistant" {
				if tcs, ok := mm["tool_calls"].([]any); ok {
					for _, tc := range tcs {
						if tcm, ok := tc.(map[string]any); ok {
							id, _ := tcm["id"].(string)
							if fn, ok := tcm["function"].(map[string]any); ok {
								name, _ := fn["name"].(string)
								if name != "" {
									state.calledTools = append(state.calledTools, name)
									if id != "" {
										toolCallIDToName[id] = name
									}
								}
							}
						}
					}
				}
			}

			// Collect tool results
			if role == "tool" {
				callID, _ := mm["tool_call_id"].(string)
				content, _ := mm["content"].(string)
				if name, exists := toolCallIDToName[callID]; exists {
					state.toolOutputs[name] = content
				}
			}
		}
	}

	return state
}

func parseAnthropicConversation(body map[string]any) conversationState {
	state := conversationState{
		toolOutputs: make(map[string]string),
	}

	// Parse system prompt
	if sys, ok := body["system"].(string); ok && sys != "" {
		state.userQueries = append(state.userQueries, sys)
	} else if sysBlocks, ok := body["system"].([]any); ok {
		for _, sb := range sysBlocks {
			if sbm, ok := sb.(map[string]any); ok {
				if t, ok := sbm["text"].(string); ok && t != "" {
					state.userQueries = append(state.userQueries, t)
				}
			}
		}
	}

	// Parse offered tools
	if toolsRaw, ok := body["tools"].([]any); ok {
		for _, t := range toolsRaw {
			if tm, ok := t.(map[string]any); ok {
				if name, ok := tm["name"].(string); ok && name != "" {
					state.offeredTools = append(state.offeredTools, name)
				}
			}
		}
	}

	toolCallIDToName := make(map[string]string)

	// Parse messages history
	if msgsRaw, ok := body["messages"].([]any); ok {
		for _, m := range msgsRaw {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role, _ := mm["role"].(string)

			switch role {
			case "user":
				if cStr, ok := mm["content"].(string); ok && cStr != "" {
					state.userQueries = append(state.userQueries, cStr)
				} else if cBlocks, ok := mm["content"].([]any); ok {
					for _, b := range cBlocks {
						bm, ok := b.(map[string]any)
						if !ok {
							continue
						}
						bType, _ := bm["type"].(string)
						if bType == "text" {
							if txt, ok := bm["text"].(string); ok && txt != "" {
								state.userQueries = append(state.userQueries, txt)
							}
						} else if bType == "tool_result" {
							toolID, _ := bm["tool_use_id"].(string)
							var contentStr string
							if c, ok := bm["content"].(string); ok {
								contentStr = c
							} else if cb, ok := bm["content"].([]any); ok {
								var sb strings.Builder
								for _, item := range cb {
									if im, ok := item.(map[string]any); ok {
										if t, ok := im["text"].(string); ok {
											sb.WriteString(t)
										}
									}
								}
								contentStr = sb.String()
							}
							if name, exists := toolCallIDToName[toolID]; exists {
								state.toolOutputs[name] = contentStr
							}
						}
					}
				}

			case "assistant":
				if cBlocks, ok := mm["content"].([]any); ok {
					for _, b := range cBlocks {
						bm, ok := b.(map[string]any)
						if !ok {
							continue
						}
						bType, _ := bm["type"].(string)
						if bType == "tool_use" {
							id, _ := bm["id"].(string)
							name, _ := bm["name"].(string)
							if name != "" {
								state.calledTools = append(state.calledTools, name)
								if id != "" {
									toolCallIDToName[id] = name
								}
							}
						}
					}
				}
			}
		}
	}

	return state
}

var (
	reBranchStatus = regexp.MustCompile(`(?m)^On branch\s+(\S+)`)
	reBranchShort  = regexp.MustCompile(`(?m)^##\s+(\S+?)(?:\.\.\.|\s|$)`)
	reBranchLabel  = regexp.MustCompile(`(?i)branch:?\s*(\S+)`)
	reModifiedFile = regexp.MustCompile(`(?m)^\s*(?:modified|new file|deleted|renamed):\s+(\S+)`)
	reShortStatus  = regexp.MustCompile(`(?m)^(?:\?\?|[MADRCU ]{2})\s+(\S+)`)
	reDiffFile     = regexp.MustCompile(`(?m)^diff --git a/\S+ b/(\S+)`)
)

// synthesizeToolFindings creates a deterministic response quoting real tool findings
// so unit and end-to-end tests can assert on extracted branch names and changed files.
func synthesizeToolFindings(toolOutputs map[string]string) string {
	branch := ""
	var changedFiles []string
	seenFiles := make(map[string]bool)

	// Combine all tool output text for inspection
	var allOutput strings.Builder
	for _, out := range toolOutputs {
		allOutput.WriteString(out)
		allOutput.WriteString("\n")
	}
	combined := allOutput.String()

	// Extract branch name
	if m := reBranchStatus.FindStringSubmatch(combined); len(m) > 1 {
		branch = m[1]
	} else if m := reBranchShort.FindStringSubmatch(combined); len(m) > 1 {
		branch = m[1]
	} else if m := reBranchLabel.FindStringSubmatch(combined); len(m) > 1 {
		branch = m[1]
	}

	// Extract modified files from git status and git diff
	for _, m := range reShortStatus.FindAllStringSubmatch(combined, -1) {
		if len(m) > 1 && !seenFiles[m[1]] {
			seenFiles[m[1]] = true
			changedFiles = append(changedFiles, m[1])
		}
	}
	for _, m := range reModifiedFile.FindAllStringSubmatch(combined, -1) {
		if len(m) > 1 && !seenFiles[m[1]] {
			seenFiles[m[1]] = true
			changedFiles = append(changedFiles, m[1])
		}
	}
	for _, m := range reDiffFile.FindAllStringSubmatch(combined, -1) {
		if len(m) > 1 && !seenFiles[m[1]] {
			seenFiles[m[1]] = true
			changedFiles = append(changedFiles, m[1])
		}
	}

	if branch != "" && len(changedFiles) > 0 {
		return fmt.Sprintf("Workspace analysis: currently on branch %s. Modified files: %s. Changes verified via git_status and git_diff.",
			branch, strings.Join(changedFiles, ", "))
	}
	if branch != "" && len(changedFiles) == 0 {
		return fmt.Sprintf("Workspace analysis: currently on branch %s. Working tree is clean.", branch)
	}
	if len(changedFiles) > 0 {
		return fmt.Sprintf("Workspace analysis: modified files: %s.", strings.Join(changedFiles, ", "))
	}

	// Fallback to summarizing tools run
	var toolNames []string
	for k := range toolOutputs {
		toolNames = append(toolNames, k)
	}
	if len(toolNames) > 0 {
		return fmt.Sprintf("Workspace inspection complete using %s. Working tree is clean on branch main.",
			strings.Join(toolNames, ", "))
	}

	return DefaultCannedResponse
}

func containsSubstring(haystack []string, needle string) bool {
	if needle == "" {
		return false
	}
	needleLower := strings.ToLower(needle)
	for _, s := range haystack {
		if strings.Contains(strings.ToLower(s), needleLower) {
			return true
		}
	}
	return false
}

func containsTool(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
}

func defaultToolArgs(toolName string) string {
	switch toolName {
	case "list_dir":
		return `{"path":"."}`
	case "read_file":
		return `{"path":"README.md"}`
	default:
		return "{}"
	}
}

func defaultToolArgsMap(toolName string) map[string]any {
	switch toolName {
	case "list_dir":
		return map[string]any{"path": "."}
	case "read_file":
		return map[string]any{"path": "README.md"}
	default:
		return map[string]any{}
	}
}

func parseArgsToMap(argsStr string) map[string]any {
	if argsStr == "" {
		return map[string]any{}
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(argsStr), &res); err == nil {
		return res
	}
	return map[string]any{}
}

func calculateUsage(state conversationState, completionTokens int) map[string]any {
	promptTokens := 120 + len(state.userQueries)*15 + len(state.calledTools)*30
	return map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	}
}

func calculateAnthropicUsage(state conversationState, outputTokens int) map[string]any {
	inputTokens := 115 + len(state.userQueries)*15 + len(state.calledTools)*25
	return map[string]any{
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
	}
}

// extractAuthToken reads either Authorization: Bearer <key> or x-api-key without printing.
func extractAuthToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		token = strings.TrimSpace(strings.TrimPrefix(token, "bearer "))
		return token
	}
	if key := r.Header.Get("x-api-key"); key != "" {
		return strings.TrimSpace(key)
	}
	return ""
}

// sanitizeHeaders masks sensitive tokens in headers to obey Rule 5 (fingerprints/lengths only).
func sanitizeHeaders(orig http.Header) http.Header {
	h := orig.Clone()
	if auth := h.Get("Authorization"); auth != "" {
		h.Set("Authorization", maskToken(auth))
	}
	if key := h.Get("x-api-key"); key != "" {
		h.Set("x-api-key", maskToken(key))
	}
	return h
}

// maskToken produces a non-sensitive length and fingerprint representation of a secret.
func maskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	prefix := ""
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		prefix = "Bearer "
		token = strings.TrimSpace(token[7:])
	}
	h := sha256.Sum256([]byte(token))
	fp := hex.EncodeToString(h[:4])
	return fmt.Sprintf("%s[len=%d, fp=%s]", prefix, len(token), fp)
}
