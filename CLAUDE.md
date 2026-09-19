# TalkIntent — agent working notes

TalkIntent = distributed on-site probe agents (per developer, local daemon) + a central Hub,
so teammates can ask "what is 张三 working on / how is the login refactor going" and get an
answer synthesized from the *live* dev workspace (git diff, branch, recent edits, docs, open
ports) — without interrupting the person, and under that person's own natural-language
privacy rules. Spec: `docs/DESIGN.md`, wire format: `docs/PROTOCOL.md`.

## Stack (fixed decisions — do not re-litigate)
- Go 1.26, module `github.com/Sskift/talkintent`. ONE binary `talkintent` with subcommands
  (`hub`, `pair`, `daemon`, `ask`, `members`, `history`, `llm`, `privacy`, `web`, `skill`, `status`, `version`).
- Deps are pinned in `go.mod`; only `github.com/coder/websocket` (+stdlib). Do NOT add
  dependencies without stating why in the final report. No cgo, no sqlite.
- Storage: append-only JSONL under the hub data dir + in-memory indexes. Tokens hashed (sha256) at rest.
- Web UI: vanilla HTML/JS/CSS embedded via `embed.FS` under `web/static`. No build step, no framework.
- Probe agent: LLM tool-use loop supporting BOTH OpenAI-compatible `/v1/chat/completions` (tools)
  and Anthropic `/v1/messages` (tools). Credentials live only on the answering member's machine.
  LLM config MUST support `ca_file` (PEM; a bare leaf cert in the pool must be accepted — Go does this natively),
  `tls_server_name` (SNI/hostname override when dialing by IP), and `insecure_skip_verify` (explicit opt-in, logged loudly)
  because real self-hosted gateways use private CAs.
  Anthropic responses may contain `thinking` blocks before `tool_use` — skip them, don't crash.
- Privacy = natural-language prompt files (`~/.talkintent/privacy-prompt.md` + `<workspace>/.talkintent/privacy-prompt.md`)
  injected as a guardrail block in the probe system prompt; plus built-in hard denylist for secret files
  and an optional regex post-redactor as defense in depth.
- Everything the probe reads stays on the member's machine; only the final answer (+ tool *names*) goes to the hub.

## Environment gotchas (Windows Git Bash dev box)
- Go toolchain: `export PATH=$HOME/go-sdk/go/bin:$PATH` (Go 1.26.1). `GOPROXY=https://goproxy.cn,direct` is already set.
- The Bash tool truncates commands > ~8KB ("unexpected EOF while looking for matching quote") — write files with the
  Write tool, never via long heredocs. Never run `find /`.
- All files LF (`.gitattributes` enforces). Paths in tests must use `filepath` and work on Windows AND Linux.
- `go build ./... && go vet ./... && go test ./...` must pass before you report done. First cold build takes ~30s.
- Don't commit or push unless told; the orchestrator commits. Don't touch `go.mod`/`go.sum` except via `go mod tidy`
  when explicitly asked.

## Code style
- Plain Go, stdlib `flag`-based subcommands, `log/slog` for logging, `context` everywhere, errors wrapped with `%w`.
- Small packages under `internal/`; no global mutable state except in `main`.
- Comments explain *why*, not *what*. Keep the density of the surrounding file.
- Chinese-facing UI/CLI strings are fine (team is Chinese); code identifiers and docs in English.
