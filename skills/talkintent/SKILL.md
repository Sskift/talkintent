---
description: Ask a teammate's live dev workspace questions asynchronously (git diff, branch, uncommitted edits, ports) without interrupting them.
globs: 
---

# TalkIntent — Live Dev Workspace Perception Skill

TalkIntent allows teammates and Claude Code to asynchronously query a colleague's *live* dev workspace
(git diff, active branch, uncommitted edits, recently modified files, configuration, listening ports)
without interrupting them, strictly bounded by the colleague's sovereign natural-language privacy rules (`privacy-prompt.md`).

All file reads and tool executions stay on the colleague's local machine; only the synthesized final answer leaves.

## When to Use This Skill

Use this skill whenever:
1. You or the user want to know what a teammate is working on right now (e.g. "what is 张三 working on?").
2. You need to check the progress or status of a feature, bugfix, or refactor owned by a colleague (e.g. "how is the login refactor going in 李四's workspace?").
3. You need to check local development state (e.g. "what port is the auth service running on for wangwu?", "are there uncommitted changes on the auth branch?").
4. You need to discover available teammates, their aliases, and who is currently online or offline.

## Discovery: Checking Team Members and Availability

Before querying, or when the user asks who is online, run:

```bash
talkintent members
```

Or for JSON output:
```bash
talkintent members --json
```

This returns a list of registered team members, their IDs, canonical names, aliases, and online/offline status.

## Querying a Workspace: `talkintent ask`

You can run `talkintent ask` in either explicit mode, natural language mode, or via the Claude Code `/talkintent` command.

### 1. Claude Code Command Syntax
```bash
/talkintent <teammate-name-or-alias> <question>
```

Examples:
- `/talkintent 张三 现在登录模块重构得怎么样了，有没有新的结构体定义？`
- `/talkintent lisi 本地服务跑在什么端口上？`
- `/talkintent wangwu feature/auth 分支最近改动了哪些文件？`

### 2. Explicit Target Mode
Use `--to` (or `--target`) with the teammate's canonical name or alias:
```bash
talkintent ask --to "<teammate-name-or-alias>" --query "<question>" --wait
```

Examples:
```bash
talkintent ask --to "张三" --query "现在登录模块重构得怎么样了，有没有新的结构体定义？" --wait
talkintent ask --to "lisi" --query "本地 auth 服务跑在什么端口上？" --wait
talkintent ask --to "wangwu" --query "feature/auth 分支最近改动了哪些文件？" --wait
```

### 2. Natural Language Mode
Pass the natural language question directly. The CLI automatically resolves the target teammate from the query:
```bash
talkintent ask "问一下张三现在登录模块重构得怎么样了"
talkintent ask "问下李四本地服务端口是多少"
```

### Available Flags for `talkintent ask`
- `--to <target>` or `--target <target>`: Teammate name or alias.
- `--query <text>` or `-q <text>`: Question to ask.
- `--wait`: (Default `true`) Wait for the on-site probe agent to inspect and return the answer.
- `--timeout <seconds>`: (Default `60`) Maximum seconds to wait for probe execution.
- `--workspace <name>`: Optional target workspace identifier if the colleague monitors multiple repositories.
- `--ttl <seconds>`: (Default `86400` / 24 hours) Offline queue time-to-live if the colleague is offline.
- `--json`: Format the output as JSON for programmatic consumption.

## Interpreting Query Outcomes

The CLI outputs the synthesized answer and metadata:
- `completed`: The probe ran on the teammate's machine, inspected the workspace with tools (e.g. `git_status`, `git_diff`, `read_file`), and synthesized the answer.
- `queued`: The teammate's daemon is currently offline. The query is queued on the Hub and will be answered when their laptop/daemon reconnects.
- `refused`: The question touched an area restricted by the teammate's sovereign privacy rules (`privacy-prompt.md`), reported via the structured refuse tool.
- `error` / `timeout`: The probe encountered an execution error or exceeded the timeout budget.

## Privacy & Security Guarantees

- **No Remote Code Execution**: The probe runs only read-only sandboxed inspection tools.
- **Local Sovereignty**: Tools execute locally on the target's machine. Nothing the probe reads leaves the machine except the final synthesized answer and tool names.
- **Credential Protection**: Hard denylists block access to `.env`, private keys, and cloud credentials; automatic regex redaction strips any leaked tokens or private IPs.
