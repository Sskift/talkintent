#!/usr/bin/env python3
"""Probe whether an Anthropic-compatible endpoint supports tool use.

Credentials are read from either a Claude Code settings.json (env.ANTHROPIC_*) or a
KEY=VALUE env file (ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL / ANTHROPIC_MODEL).
Prints only status + response shape; never prints credentials.

usage: probe-llm.py [--settings PATH | --env PATH] [MODEL]
"""
import json, os, ssl, sys, urllib.request

args = sys.argv[1:]
base = tok = model = None
if args and args[0] == "--settings":
    cfg = json.load(open(os.path.expanduser(args[1])))["env"]
    base, tok = cfg["ANTHROPIC_BASE_URL"], cfg.get("ANTHROPIC_AUTH_TOKEN") or cfg.get("ANTHROPIC_API_KEY")
    model = cfg.get("ANTHROPIC_DEFAULT_HAIKU_MODEL")
    args = args[2:]
elif args and args[0] == "--env":
    kv = {}
    for line in open(os.path.expanduser(args[1])):
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            k, v = line.split("=", 1)
            kv[k.strip()] = v.strip().strip('"').strip("'")
    base, tok, model = kv["ANTHROPIC_BASE_URL"], kv["ANTHROPIC_API_KEY"], kv.get("ANTHROPIC_MODEL")
    args = args[2:]
else:
    print("need --settings or --env"); sys.exit(2)
if args:
    model = args[0]
base = base.rstrip("/")
print("endpoint host=%s model=%s token_len=%d" % (base.split("//")[-1].split("/")[0], model, len(tok)))

body = {
    "model": model,
    "max_tokens": 200,
    "tools": [{
        "name": "git_status",
        "description": "Return git status of the workspace",
        "input_schema": {"type": "object", "properties": {}, "additionalProperties": False},
    }],
    "tool_choice": {"type": "auto"},
    "messages": [{"role": "user", "content": "Call the git_status tool now, then summarize."}],
}
req = urllib.request.Request(base + "/v1/messages", data=json.dumps(body).encode(),
                             headers={"content-type": "application/json", "x-api-key": tok,
                                      "authorization": "Bearer " + tok, "anthropic-version": "2023-06-01",
                                      "user-agent": "talkintent-probe/0.1 (+Go-http-client)"})
try:
    ctx = ssl.create_default_context()
    if os.environ.get("PROBE_INSECURE") == "1":  # test-only: private CA not available locally
        ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
    with urllib.request.urlopen(req, timeout=90, context=ctx) as r:
        d = json.load(r)
        kinds = [b.get("type") for b in d.get("content", [])]
        print("status 200 model=%s stop=%s blocks=%s usage=%s" % (d.get("model"), d.get("stop_reason"), kinds, d.get("usage")))
except urllib.error.HTTPError as e:
    print("HTTP", e.code, e.read()[:300].decode(errors="replace"))
except Exception as e:
    print("ERR", type(e).__name__, str(e)[:200])
