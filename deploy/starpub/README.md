# TalkIntent StarPub Multi-User Host Deployment

This deployment topology validates TalkIntent on a bare Linux host (such as a remote dev server without Docker or container privileges), using multiple local Linux system users to simulate independent developer workstations.

## Scenario Setup

A single root/admin script creates three isolated Unix users:
- `dev-alice` (home: `/home/dev-alice`)
- `dev-bob` (home: `/home/dev-bob`)
- `dev-charlie` (home: `/home/dev-charlie`)

Each user runs their own `talkintent daemon` with their own `~/.talkintent/config.json`, workspaces, and local privacy rules.

## Real LLM Validation

Unlike the Docker Compose test which uses `mockllm`, StarPub runs against a real Anthropic-compatible or OpenAI-compatible model endpoint (e.g., AsterGate or upstream API).
- Member credentials (`TALKINTENT_LLM_KEY`, `TALKINTENT_LLM_URL`, `TALKINTENT_LLM_MODEL`) are configured in each user's local config.
- The test script verifies privacy prompt enforcement on live git repositories under `/home/dev-alice/workspace` and `/home/dev-bob/workspace`.
