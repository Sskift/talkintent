package mockllm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Server is a test HTTP server mimicking OpenAI and Anthropic endpoints.
type Server struct {
	*httptest.Server
	mu             sync.Mutex
	RecordedCalls  []RecordedRequest
	CannedResponse string
	ToolToCall     string
	ToolArgs       string
}

// RecordedRequest logs an incoming LLM invocation.
type RecordedRequest struct {
	Path   string
	Header http.Header
	Body   map[string]any
}

// NewServer creates and starts a new MockLLM test server.
func NewServer() *Server {
	s := &Server{
		CannedResponse: "Mock LLM answer: workspace is clean on branch main.",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleOpenAI)
	mux.HandleFunc("/v1/messages", s.handleAnthropic)

	s.Server = httptest.NewServer(mux)
	return s
}

func (s *Server) handleOpenAI(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.RecordedCalls = append(s.RecordedCalls, RecordedRequest{
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
		Body:   body,
	})

	w.Header().Set("Content-Type", "application/json")

	if s.ToolToCall != "" {
		resp := map[string]any{
			"id":      "chatcmpl-mock-tool",
			"object":  "chat.completion",
			"created": 1726828800,
			"choices": []any{
				map[string]any{
					"index": 0,
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []any{
							map[string]any{
								"id":   "call_01J8MOCKTOOL",
								"type": "function",
								"function": map[string]any{
									"name":      s.ToolToCall,
									"arguments": s.ToolArgs,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 20,
				"total_tokens":      120,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	resp := map[string]any{
		"id":      "chatcmpl-mock-001",
		"object":  "chat.completion",
		"created": 1726828800,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": s.CannedResponse,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     150,
			"completion_tokens": 40,
			"total_tokens":      190,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleAnthropic(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.RecordedCalls = append(s.RecordedCalls, RecordedRequest{
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
		Body:   body,
	})

	w.Header().Set("Content-Type", "application/json")

	if s.ToolToCall != "" {
		resp := map[string]any{
			"id":          "msg_mock_anth_tool",
			"type":        "message",
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "call_01J8MOCKTOOL",
					"name":  s.ToolToCall,
					"input": map[string]any{},
				},
			},
			"usage": map[string]any{
				"input_tokens":  120,
				"output_tokens": 30,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	resp := map[string]any{
		"id":          "msg_mock_anth_001",
		"type":        "message",
		"role":        "assistant",
		"stop_reason": "end_turn",
		"content": []any{
			map[string]any{
				"type": "text",
				"text": s.CannedResponse,
			},
		},
		"usage": map[string]any{
			"input_tokens":  140,
			"output_tokens": 45,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// Reset clears recorded calls and reset configuration.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RecordedCalls = nil
	s.ToolToCall = ""
	s.ToolArgs = ""
	s.CannedResponse = fmt.Sprintf("Default answer from Mock LLM (%s)", s.URL)
}
