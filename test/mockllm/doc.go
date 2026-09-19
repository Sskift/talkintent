// Package mockllm provides a deterministic mock HTTP server supporting both
// OpenAI (/v1/chat/completions) and Anthropic (/v1/messages) tool-calling dialects
// for automated unit, end-to-end integration tests, and standalone deployment.
//
// Key features include:
//   - Dual dialect support: OpenAI (/v1/chat/completions) and Anthropic (/v1/messages)
//   - Deterministic scripted tool execution: sequentially invokes git_status then
//     git_diff (or custom/offered tools in documented order), then synthesizes a final
//     answer quoting tool outputs (current branch name, modified files)
//   - Knobs:
//   - Canned answer override (WithCannedResponse)
//   - Query substring refusal (WithRefusal): returns a refusal message without calling tools
//   - Anthropic 'thinking' block emission before tool_use (WithThinking) to test skip logic
//   - Response latency simulation (WithLatency)
//   - Error injection: HTTP 500 once or N times (WithFailOnce500, WithFail500Count)
//   - Authentication: accepts both 'x-api-key' and 'Authorization: Bearer <token>' headers;
//     never logs raw credentials (Rule 5: lengths and fingerprints only)
//   - Realistic token usage accounting in both dialects
//   - Usable as an in-process httptest server (NewServer, NewServerWithOptions) or as a
//     standalone runnable binary (test/mockllm/cmd/mockllm)
package mockllm
