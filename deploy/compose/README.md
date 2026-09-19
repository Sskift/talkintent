# TalkIntent Docker Compose Multi-Container Deployment

This deployment topology validates TalkIntent in a multi-node environment on a single machine using Docker Compose.

## Architecture

- **`talkintent-hub`**: Central Hub listening on `:8080`.
- **`mock-llm`**: Mock OpenAI/Anthropic tool-calling server listening on `:9090`.
- **`client-alice`**: Client daemon running as member Alice, with a sample Go repository workspace.
- **`client-bob`**: Client daemon running as member Bob, with a sample Python repository workspace.
- **`client-charlie`**: Client daemon running as member Charlie, with an offline/reconnecting test profile.

## Running the Scenario

```bash
docker compose -f deploy/compose/docker-compose.yml up --build -d
```

## Automated Assertions

The test runner verifies:
1. All 3 clients pair with the Hub using admin-generated invite codes.
2. Alice submits a query to Bob ("What branch are you on and what files are modified?").
3. Bob's daemon executes the probe against `mock-llm`, inspects git status, and returns the response.
4. Alice receives the completed response within 5 seconds.
5. Inbound and outbound audit records appear in the Hub's JSONL log.
6. A query targeting Charlie while Charlie's container is stopped gets queued; restarting Charlie triggers immediate delivery and completion.
