# TalkIntent Topology 3: Cross-Machine Deployment

This deployment suite validates TalkIntent across physical/virtual machine boundaries:
- **Hub**: Running on a remote Linux server (`starpub-docker`, Ubuntu 22.04 LTS).
- **Client 1 (Windows)**: Developer workstation running on Windows 11 (`bin/talkintent.exe`).
- **Client 2 (Linux)**: Developer workstation running on the remote Linux host (`bin/talkintent`).

## Architecture & Network Topology

```
+-------------------------------------------------------------+
| Windows Dev Workstation (Local)                             |
|                                                             |
|  [talkintent.exe daemon]                                    |
|      |                                                      |
|      v                                                      |
|  [mockllm.exe :18822]                                       |
|      ^                                                      |
|      |                                                      |
|  [SSH Tunnel: localhost:18820 -> starpub-docker:18820]       |
+------------------------------|------------------------------+
                               | Encrypted SSH Tunnel
+------------------------------v------------------------------+
| Remote Host (starpub-docker: Linux amd64)                   |
|                                                             |
|  [TalkIntent Hub :18820] <------- [remote-dev daemon]       |
|                                         |                   |
|                                         v                   |
|                                   [mockllm :18821]          |
+-------------------------------------------------------------+
```

### Port Allocation (Isolated 18820-18839 range)
- **18820**: TalkIntent Hub on remote host (`127.0.0.1:18820`), forwarded locally via `ssh -N -L 18820:127.0.0.1:18820`.
- **18821**: Remote Mock LLM (`127.0.0.1:18821`) providing tool call responses for `remote-dev`.
- **18822**: Windows Mock LLM (`127.0.0.1:18822`) providing tool calls and privacy refusal for `win-laptop`.

## Verification Scenarios Covered

1. **Member Onboarding & Discovery**:
   - Remote Hub generates invites.
   - Windows client pairs over the SSH tunnel.
   - Remote client pairs directly on the host.
   - Both clients verify mutual presence via `talkintent members` (`online: true`).

2. **Bi-Directional Query Execution with Tool Use**:
   - Remote client queries Windows: `remote-dev` asks `win-laptop`. Windows daemon inspects real Git workspace on Windows, executes tools (`git_status`, `git_diff`), and returns synthesized answer.
   - Windows client queries Remote: `win-laptop` asks `remote-dev`, receiving tool-inspected results.

3. **Cross-Machine Offline Queuing**:
   - Windows daemon is terminated.
   - Remote submits an asynchronous query with `-wait=false`, receiving `status: queued`.
   - Windows daemon restarts and reconnects.
   - Pending query is automatically dispatched, executed, and completed.

4. **Sovereign Privacy Guardrail Refusal**:
   - Windows member configures natural-language privacy rules (`privacy-prompt.md`) guarding confidential topics (e.g. salary/compensation).
   - Remote queries the guarded topic.
   - Probe enforces the rule via its structured `refuse` tool; the Hub records `status: refused` with a reason and no tool execution or data leakage (the script fails on a chatty `completed`).

5. **Windows CLI Utility Verification**:
   - Skill installation (`talkintent skill install -dir ...` & `skill show`).
   - Privacy rule evaluation test (`talkintent privacy test ...`).
   - Windows background daemon lifecycle (`daemon start -detach`, `daemon status`, `daemon stop`).

6. **Clean Teardown**:
   - Comprehensive termination of local executables, remote processes, and network tunnels.

## Running the Acceptance Suite

Run from Git Bash on the Windows development box:
```bash
# Execute end-to-end acceptance suite
./deploy/xmachine/run-xmachine.sh

# Teardown at any time
./deploy/xmachine/stop.sh
```

Raw execution logs are written to `/c/tmp/ti-accept/xmach/` for auditing.
