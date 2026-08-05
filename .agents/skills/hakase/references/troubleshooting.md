# hakase Troubleshooting

## Startup / config errors

| Error | Cause | Fix |
|---|---|---|
| `unsupported provider: <name>` | `provider` is not gemini/openai/openai-compatible | Fix value or leave empty (defaults to gemini) |
| `gemini provider requires an api_key` | missing key for gemini | Set `api_key` in config.json or `HAKASE_API_KEY` |
| `openai provider requires an api_key` | missing key for openai | Same |
| OpenAI-compatible endpoint unreachable | `base_url` wrong or server down | Confirm the server serves an OpenAI-compatible API (e.g. Ollama at `http://localhost:11434/v1`) and is reachable |
| `Failed to load config` | no config.json and no HAKASE_* env vars | `cp config.json.example config.json`, or use env vars |

## Skills not showing up

- Skills are discovered **only at startup**; restart hakase after adding one.
- The skill must live in a discovery path: project `.agents/skills` (etc.) or user
  `~/.hakase/skills` (etc.). Skills outside these paths are never loaded.
- The skill directory name must EXACTLY match the `name` in SKILL.md frontmatter.
- Invalid skills are skipped with a `[skills] Skipping invalid markdown skill ...`
  warning in the log. Common causes: missing/empty description, description >1024
  chars, name not matching the kebab-case regex, dir/frontmatter name mismatch.
- Check with `hakase skill list` and `hakase skill validate <dir>`.

## Sandbox / execution

- `bwrap` not installed: bubblewrap mode falls back to path confinement and logs a
  warning. Install bubblewrap for isolation, or set `sandbox.mode: "paths"`.
- `system_exec` path rejected: the absolute path arg falls outside read roots and
  trusted system dirs (`/usr /lib /bin /etc /proc /dev /sys /tmp /run`). Add the
  directory to `sandbox.read_roots`.
- `system_exec` killed after 120s: the default timeout. Use `system_exec_start`
  (background) or pass an explicit `timeout_seconds`.
- Python `ModuleNotFoundError` loops: auto-dependency resolution installs via pip and
  retries; if `allow_pip_install` is false in sandbox config, installs are blocked.

## Provider / model

- Model not responding as expected: check `thinking_level` (off disables thoughts) and
  `loop_guard.max_output_tokens` (default 8192 caps output).
- Stuck/degenerate output: the loop guard aborts on repetition (8 identical chunks)
  or text-only bloat (20000 runes without a tool call). Raise bounds in `loop_guard`
  if legitimately exceeded.
- Summaries use the primary model when `summary_model` is unset; set a cheaper model
  (e.g. `gemini-2.5-flash-lite`) to save cost.

## Knowledge base

- Dangling `[[wikilinks]]`: the agent reports them and offers to create the missing
  notes - only after user confirmation.
- `hakase knowledge lint` reports orphans, dangling links, and broken index; run it to
  diagnose knowledge issues.
- Notes appear duplicated between `knowledge/` and `knowledge/notes/`: the `notes/`
  subdirectory wins when a slug exists in both.

## General

- `HAKASE_*` env vars scrubbed from subprocesses: if a shell/Python command cannot see
  a HAKASE_ variable, that is intentional (secret hygiene). Use non-HAKASE_ names for
  cross-process data.
- Multi-line input: `Shift+Enter` inserts a newline; `Enter` sends.
- Focus cycling in the TUI: `Tab` / `Shift+Tab`.
- Model info unavailable in the status bar: `FetchModelInfo` failed (network/provider);
  non-fatal.
