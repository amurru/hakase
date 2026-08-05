# hakase Configuration Reference

## Config file resolution

1. `./config.json` (project) - wins when present.
2. `~/.hakase/config.json` (user) - used when no project config exists.
3. Environment-only - when no file exists but `HAKASE_*` vars are set.

`$HAKASE_HOME` overrides `~/.hakase` for both the config fallback and user-level skills.

## Full schema

```json
{
  "provider": "gemini",
  "model_name": "gemini-2.5-flash",
  "api_key": "your_api_key",
  "base_url": "",
  "instruction": "You are a web automation agent harness.",
  "instruction_files": [],
  "context_files": {
    "max_chars": 20000,
    "apply_to": []
  },
  "mcp_server_url": "http://localhost:9223/mcp",
  "fallback_providers": ["openai"],
  "skill_dirs": [],
  "knowledge_dir": "",
  "provider_options": {},
  "chat_buffer_size": 1000,
  "show_thinking": false,
  "task_checkpoint": false,
  "thinking_level": "",
  "summary_model": "",
  "env_overrides": {},
  "delegate_timeout_seconds": 300,
  "debug": false,
  "sandbox": {
    "mode": "paths",
    "workspace_roots": ["."],
    "read_roots": [],
    "deny_roots": [],
    "allow_network": false,
    "allow_pip_install": true,
    "allow_fallback": false,
    "allowed_commands": [],
    "deny_patterns": [],
    "risk_threshold": ""
  },
  "loop_guard": {
    "max_output_tokens": 8192,
    "repetition_limit": 8,
    "max_text_without_tool": 20000
  },
  "approval": {
    "mode": "interactive",
    "expiry_seconds": 60
  }
}
```

## Providers

| Provider | Default model | Notes |
|---|---|---|
| `gemini` | `gemini-2.5-flash` | default; requires api_key |
| `openai` | `gpt-4o-mini` | requires api_key |
| `openai-compatible` | `gpt-4o-mini` | use `base_url` (e.g. Ollama `/v1`); api_key optional |

Empty `provider` defaults to `gemini`. `fallback_providers` is an ordered list tried if
the primary fails.

## Environment variables

| Variable | Overrides |
|---|---|
| `HAKASE_API_KEY` | `api_key` |
| `HAKASE_PROVIDER` | `provider` |
| `HAKASE_MODEL` | `model_name` |
| `HAKASE_BASE_URL` | `base_url` |
| `HAKASE_SUMMARY_MODEL` | `summary_model` |
| `HAKASE_DEBUG` | `debug` |
| `HAKASE_MAX_OUTPUT_TOKENS` | `loop_guard.max_output_tokens` |
| `HAKASE_HOME` | user home location (default `~/.hakase`) |

Env vars take precedence over file values. `HAKASE_*` vars are scrubbed from
subprocess environments (system_exec / sandboxed Python) so secrets never leak.

## Sandbox modes

- `paths` (default) - path confinement for file ops/downloads/python; system_exec
  absolute path args audited against read roots + trusted system dirs
  (`/usr /lib /bin /etc /proc /dev /sys /tmp /run`).
- `bubblewrap` - kernel-level bwrap isolation (PID/IPC/UTS/user namespaces, dropped
  caps, ro system dirs, optional network unshare). Falls back to `paths` if bwrap
  is not installed (logged warning).
- `landlock` - reserved for future in-process confinement.
- `off` - disables confinement (opt-in only).

## Knowledge dir

`knowledge_dir` defaults to `./knowledge`. A leading `~/` expands to the user's home,
so `"knowledge_dir": "~/.hakase/knowledge"` gives a user-global knowledge base that
persists across projects.
