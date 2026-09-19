# TalkIntent Claude Code Skill

Ask a teammate's live dev workspace questions asynchronously without interrupting them.

## Usage

```bash
/talkintent <teammate-name-or-alias> <question>
```

Examples:
- `/talkintent 张三 现在登录模块重构得怎么样了，有没有新的结构体定义？`
- `/talkintent lisi 本地服务跑在什么端口上？`
- `/talkintent wangwu feature/auth 分支最近改了哪些文件？`

## Execution

When invoked, the skill runs:
```bash
talkintent ask --target "<target>" --query "<query>" --wait
```

The Hub dispatches the query to the teammate's on-site background daemon, which runs a lightweight LLM probe agent sandboxed in their local workspace. The probe checks live git diffs, branch state, recent edits, configs, and open ports under that teammate's sovereign natural-language privacy guardrails (`privacy-prompt.md`). Only the synthesized final answer leaves their machine.
