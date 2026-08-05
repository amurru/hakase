---
name: hakase
description: Knowledge about the hakase agent itself - its identity, architecture, sub-agents, tools, configuration, skills system, knowledge base, sandbox and safety model, user home (~/.hakase), CLI commands, and how to extend or operate it. Use when the user asks "who are you", "what can you do", or asks how to configure, customize, extend, debug, or operate hakase (config.json, providers, environment variables, skills, knowledge base, sandbox, TUI shortcuts, troubleshooting).
license: MIT
metadata:
  author: hakase
  version: 1.0.0
allowed-tools: read_file, search_files, list_skills, load_markdown_skill, save_knowledge, recall_knowledge, search_knowledge, system_exec
---

# The hakase Agent - Self-Knowledge

This skill is the authoritative reference for **hakase itself**: what it is, how it is built, how it is configured, and how it is extended. Load it whenever the user asks a question about the agent (identity, capabilities, configuration, internals) or asks you to do something related to operating or extending hakase (edit `config.json`, add a skill, use the knowledge base, interpret an error, understand a safety behavior).

## 1. Identity & Purpose

- **hakase** is a terminal-based, high-autonomy, general-purpose AI research and navigation agent written in **Go** (module `amurru/hakase`), inspired by the Hermes Agent framework.
- It orchestrates multiple specialized sub-agents through a Google **ADK v2** root orchestrator, powered by configurable LLM providers (Gemini default, OpenAI, OpenAI-compatible).
- The whole interaction happens in a split-pane terminal TUI built with Bubble Tea (charm.land/bubbletea/v2).
- Source of truth for the self-description: the system-prompt constants in `agent.go` (`HakaseSystemInstruction`, `GeneralPurposeSystemInstruction`, `CodeInterpreterSystemInstruction`, `buildOrchestratorInstruction()`) and `README.md`.

## 2. Architecture: Agents & Delegation

There are four ADK agents. The orchestrator is the root agent; the other three are sub-agents, not tools:

| Agent | Role | Key tools |
|---|---|---|
| **orchestrator** (root) | Coordinates everything; delegates to sub-agents by intent; owns planning, knowledge, skills | task tools, knowledge tools, file ops, system_exec, delegate_task, list/load skill tools |
| **web_researcher** | Searches/navigates the web, downloads files, extracts content | MCP browser toolset (Lightpanda at `localhost:9223`), download tool |
| **code_interpreter** | Executes Python in `.venv`, data analysis, manages the Python skill library | python_interpreter, save_skill, list_skills, load_markdown_skill |
| **general_purpose** | Workspace file operations | read_file, write_file, patch, search_files |

- Sub-agents are invoked via **`delegate_task`** (recommended: task tracking + isolated session; schema takes `goal` required, `context` optional - there is no `prompt`/`task` field) or **`transfer_to_agent`** (hands control directly). Sub-agents cannot call `delegate_task`, `clarify`, `memory`, `send_message`, or `cronjob`.
- Sub-agent delegation has a watchdog timeout (default 300s; `delegate_timeout_seconds` in config).

## 3. Capability Inventory

- **Web research & browsing** - MCP-connected browser tools (requires the Lightpanda MCP server at `http://localhost:9223/mcp`, configured via `mcp_server_url`).
- **File download** - any HTTP/HTTPS URL to `./downloads/`; filename basename-sanitized, sandbox-confined.
- **Python code interpreter** - isolated `.venv`; auto-resolves missing pip dependencies (`ModuleNotFoundError` -> install -> retry); `PYTHONPATH` includes `./skills`; own process group with parent-death signal; temp/work dirs pinned to workspace under sandbox.
- **File operations** - `read_file` (offset/limit ranges), `write_file` (overwrite flag), `patch` (byte-exact string replacement), `search_files` (recursive regex; head_limit default 100, max 50k entries walked, 30s deadline).
- **System command execution** - `system_exec` toolset: shell routing (`sh -c` when no args), process hardening, 120s default timeout (use `system_exec_start` for background), path-confined under sandbox.
- **Data analysis & visualization** - pandas/matplotlib in `.venv`; artifacts saved to `./outputs/`.
- **Task board** - persisted `tasks.json`: `create_task`, `list_tasks`, `get_task`, `update_task`, `archive_task`, `delete_task`.
- **Session persistence** - conversation sessions stored under `./sessions` (`session` CLI + `hakase session`); stale sessions cleaned after 30 days.
- **Context management** - history building with budget math and optional cheap-model summarization (`summary_model`); manual `/compact [focus]` triggers the same compaction cascade on demand.
- **Message attachments** - `@` file mention menu and `Ctrl+V` image paste; text files embed as text parts, images as inline data parts; attachments persist by path+MIME and re-read on resume.
- **Mid-run messaging & interrupt** - messages typed while the agent is busy are queued and steered into the running session as a `USER INTERJECTION`; `Esc Esc` (within 2s) interrupts the running agent.
- **Slash commands** - local command menu (`/board`, `/compact`, `/new`, `/sessions`, `/help`, `/exit`) that never reaches the model.

## 4. Configuration

### Config file lookup (project wins, then user home)
1. `./config.json` in the working directory (project config).
2. `~/.hakase/config.json` - user-level fallback (`$HAKASE_HOME/config.json` when `HAKASE_HOME` is set).
3. If neither exists, config can be built entirely from `HAKASE_*` environment variables.

### Key fields
- `provider` - `gemini` (default), `openai`, `openai-compatible`. `base_url` for compatible endpoints (e.g. Ollama `http://localhost:11434/v1`).
- `model_name` - empty uses provider default (`gemini-2.5-flash`, `gpt-4o-mini`).
- `api_key` - required for gemini/openai.
- `instruction` - optional, additional customization rendered into the agent instructions as a `USER CONFIG INSTRUCTION` section (alongside discovered AGENTS.md context). It is NOT a replacement for the built-in system prompts (system prompts come from agent.go constants); it only adds.
- `instruction_files` - extra context files (local paths or http(s) URLs) merged into the project context after project and user-level AGENTS.md files.
- `context_files` - tunes project-context loading: `max_chars` (per-file cap, default 20000), `apply_to` (which agents receive the block; empty = all).
- `mcp_server_url` - Lightpanda browser MCP endpoint.
- `knowledge_dir` - knowledge base directory (default `./knowledge`; `~` expands to home, e.g. `~/.hakase/knowledge` for a user-global base).
- `skill_dirs` - extra markdown skill directories, resolved against project root when relative.
- `summary_model` - cheaper model for context-compaction; `HAKASE_SUMMARY_MODEL` env override.
- `sandbox` - confinement strategy (`paths` default, `bubblewrap`, `landlock` reserved, `off`).
- `loop_guard` - anti-degeneration guardrails: `max_output_tokens` (default 8192), `repetition_limit` (8), `max_text_without_tool` (20000).
- `approval` - interactive approval gate: `mode` (`interactive` default, `deny`, `allow`), `expiry_seconds` (60).
- `thinking_level` - passed to provider (`off`, `low`, `medium`, `high`, `maximum`, `xhigh`).

### Environment variables (override config file)
`HAKASE_API_KEY`, `HAKASE_PROVIDER`, `HAKASE_MODEL`, `HAKASE_BASE_URL`, `HAKASE_SUMMARY_MODEL`, `HAKASE_DEBUG`, `HAKASE_MAX_OUTPUT_TOKENS`, `HAKASE_HOME`.

**Important:** `HAKASE_*` variables are scrubbed from subprocess environments so the API key never leaks into shell commands or sandboxed Python.

### User home: `~/.hakase/` (Claude-style)
All user-level agent state lives under `~/.hakase/` (override with `$HAKASE_HOME`):
- `~/.hakase/config.json` - user-level config fallback.
- `~/.hakase/skills/` - user-level markdown skills, discovered automatically.
- `~/.hakase/knowledge/` - optional user-global knowledge base (set `knowledge_dir: "~/.hakase/knowledge"`).

## 5. Skills System

Two kinds of skills:

### Markdown skills (SKILL.md) - portable, agent-agnostic
- Format: one directory per skill containing `SKILL.md` with YAML frontmatter (`name` required, lowercase kebab-case `^[a-z0-9]+(-[a-z0-9]+)*$`, <=64 chars; `description` required, <=1024 chars; optional `license`, `compatibility`, `metadata`, `allowed-tools`) and a progressive-disclosure body. Optional `scripts/` and `references/` subdirs. Directory name MUST equal frontmatter `name`.
- Interoperable with Claude Code, Codex CLI, Gemini CLI, OpenCode (agentskills.io spec) - drop their skills into `.agents/skills/` and they work.
- Discovery order (first match by name wins): project level `.agents/skills` -> `.claude/skills` -> `.opencode/skills` -> `.gemini/skills` (walked from cwd up to git root), then `<root>/skills`, then `skill_dirs` from config, then user level `~/.hakase/skills` -> `~/.agents/skills` -> `~/.claude/skills` -> `~/.gemini/skills` -> `~/.config/opencode/skills`.
- Skills are indexed by name+description in the agent prompt; bodies load on demand via `load_markdown_skill`.
- Skills added mid-session require a restart to be discovered.

### Python skills (legacy) - self-evolved
- Saved via `save_skill` after verified execution; stored as `./skills/<name>.py` + registered in `skills/skills.json`; importable as `from skills.<name> import ...`.
- On name collision with a markdown skill, the markdown skill wins in the prompt (the `.py` remains importable).

### CLI
- `hakase skill create <name> [--dir <path>] [--description <text>] [--template python] [--force]` - scaffolds `<dir>/<name>/SKILL.md` (default dir: `<projectRoot>/.agents/skills/`).
- `hakase skill list` - lists discovered skills with source paths.
- `hakase skill validate <dir>` - validates a skill; exits non-zero on failure (CI-friendly).

## 6. Knowledge Base

Persistent wiki-style notes with YAML frontmatter and `[[wikilinks]]`, stored in the knowledge dir (default `./knowledge`):

```
knowledge/
├── index.md    # auto-maintained catalog (regenerated on every change)
├── log.md      # append-only operation log
├── notes/      # optional subdirectory (preferred for slotted notes)
└── raw/        # immutable raw sources (excluded from index)
```

Eight tools: `save_knowledge`, `recall_knowledge`, `search_knowledge`, `update_knowledge`, `link_knowledge`, `cite_knowledge`, `list_knowledge`, `lint_knowledge`.

- Note frontmatter: `title`, `aliases`, `tags`, `created`, `updated`, `status` (draft/permanent/archived), `confidence` (high/medium/low), `sources`, `summary`, `related`.
- Wikilinks: `[[target]]`, `[[target|label]]`, `[[target#heading]]`; resolution is case-insensitive (slug -> unique basename -> alias).
- **Dangling links rule:** when save/recall/update/link return dangling wikilinks, surface them to the user, list missing notes, and offer to create them - create only after user confirmation.
- Retrieval is keyword/tag/grep only - no embeddings, no vector DB.
- CLI: `hakase knowledge list|read|search|lint|create|link [--dir <path>]`.

## 7. Safety Model

- **Sandbox on by default**: absent `sandbox` block yields `paths` mode. Modes: `paths` (pure path confinement - file ops, downloads, python resolve against workspace roots; `system_exec` absolute path args audited against read roots + trusted system dirs `/usr /lib /bin /etc /proc /dev /sys /tmp /run`), `bubblewrap` (kernel-level `bwrap` isolation, network unshare, env scrubbing), `landlock` (reserved), `off` (opt-in disable).
- Roots: `workspace_roots` (writable, default `["."]`), `read_roots`, `deny_roots` (highest precedence); symlink escapes prevented via securejoin + EvalSymlinks.
- **Approval gate**: harmful commands require interactive user approval (`approval.mode`; interactive default with 60s expiry).
- **Loop guard**: aborts runs stuck in repetition loops or text-only bloat.
- **Process hardening**: spawned processes get parent-death signals; children reaped if the agent dies.
- **system_exec default timeout**: 120s (use `system_exec_start` for long-running or set `timeout_seconds`).

## 8. TUI, Slash Commands & Attachments

Split-pane TUI: left chat viewport, right status/log pane, bottom multi-line input. Panes are clickable to focus; the hint bar under the input surfaces the most-used shortcuts and, while the agent is busy, an `N queued` counter.

### Keyboard shortcuts

| Shortcut | Action |
|---|---|
| `Ctrl+C` | Quit (also cancels a running agent) |
| `Esc` `Esc` | Interrupt the running agent (double-press within 2s; a single `Esc` only arms it) |
| `Esc` | Close the help overlay (never quits) |
| `Ctrl+/` or `?` | Toggle help overlay |
| `Tab` / `Shift+Tab` | Cycle focus (input -> chat -> log -> task) |
| `Ctrl+T` | Toggle thinking display |
| `Enter` | Send message (queued while the agent is busy) |
| `Shift+Enter` / `Ctrl+J` | Newline in input |
| `Ctrl+V` | Paste text, or attach a clipboard image as a chip |
| `@name` | Attach a file via the `@` mention menu (arrow keys navigate, `Enter`/`Tab` selects) |
| `Ctrl+Shift+C` | Copy the focused pane's content (ANSI-stripped) to the clipboard |
| `↑`/`k`, `↓`/`j`, `PgUp`/`b`, `PgDn`/`f`, `u`/`d`, `Home`/`g`, `End`/`G` | Scroll focused pane |
| `Ctrl+A` / `Ctrl+E` | Line start / end |
| `Ctrl+U` | Clear input |

### Slash commands

Typing `/` in the input opens a filtered command menu (arrow keys navigate, `Tab` completes, `Enter` runs); commands are handled locally and never reach the model. Built-ins:

| Command | Action |
|---|---|
| `/board <sub>` | Task board: `summary`, `list`, `new <title>`, `get <id>`, `update <id>`, `done <id>`, `fail <id>`, `cancel <id>`, `delete <id>`, `archive <id>`, `claim <id>` (aliases `/tasks`, `/task`; mutating subcommands blocked while the agent works) |
| `/compact [focus]` | Manually trigger the compaction cascade: deterministic history snip immediately, async LLM summary (optional focus steers it) |
| `/new` | Start a fresh session (previous sessions stay resumable) |
| `/sessions` | Open the session chooser to switch or resume a session (alias `/resume`) |
| `/help` | Show the keyboard/slash command reference (alias `/?`) |
| `/exit` / `/quit` | Exit hakase |

`/compact`, `/new`, and `/sessions` are blocked while the agent is processing.

### Mid-run messaging

Messages typed and sent while the agent is working are **queued** (the hint bar shows `N queued`). They are steered into the running session at the next model-call boundary as a `USER INTERJECTION (while you were working):` user turn, then drained as their own full turn when the current run completes. Queued prompts can carry attachments. Interrupting with `Esc Esc` merges all pending queued prompts into a single turn (Codex semantics).

### Attachments

Files and images can be attached without the agent having to find them:

- **`@file`** - type `@` to open a workspace file picker (bounded walk, hidden/heavy dirs skipped); text files embed their content (cap 200 KB), images embed as multimodal input (cap 10 MB). `@` paths resolve through the sandbox read roots; out-of-workspace paths are rejected.
- **Image paste** - copy an image and press `Ctrl+V`; it is read from the clipboard (wl-paste/xclip/xsel) and attached as a `[image N]` chip.
- Chips render above the input; `Backspace` on an empty input removes the last chip. Only the path + MIME are persisted in the session; content is re-read on resume so session files stay small.

## 9. Common Operations

- **Answering "who are you" / "what can you do"**: ground in the identity (section 1), architecture (section 2), and capability inventory (section 3) above. Keep it factual and specific to hakase.
- **Changing the provider/model**: edit `config.json` (`provider`, `model_name`, `api_key`, optional `base_url`) or set `HAKASE_*` env vars. Restart the TUI to apply.
- **Adding a skill**: prefer project `.agents/skills/<name>/SKILL.md` (always-scanned, portable, default `hakase skill create` target); fall back to `.claude/skills`, `.opencode/skills`, `.gemini/skills`, then user `~/.hakase/skills`, `~/.agents/skills`, etc. Never create skills outside discovery paths. Directory name must match frontmatter `name`. Restart to load.
- **Installing a skill from GitHub**: the standard cross-tool path is `gh skill install <owner/repo>` which writes to `.agents/skills/` (project scope). hakase's skill CLI currently has create/list/validate only; a fetched skill placed in any discovery dir is picked up after restart.
- **User-global knowledge**: set `knowledge_dir: "~/.hakase/knowledge"` (tilde expands) so durable facts persist across projects.
- **Steering a running agent**: type a message while the agent is busy and press `Enter` - it is queued and injected into the running session as a `USER INTERJECTION`; press `Esc` twice (within 2s) to interrupt the run. Queued messages drain as their own turn when the run completes.
- **Attaching files/images**: type `@` in the input to pick a workspace file, or copy an image and press `Ctrl+V`. `@` paths must resolve under the sandbox read roots.
- **Troubleshooting**:
  - `unsupported provider: <name>` - provider field is wrong; use gemini/openai/openai-compatible or empty.
  - `gemini provider requires an api_key` / `openai provider requires an api_key` - missing key in config.
  - OpenAI-compatible unreachable - confirm `base_url` serves an OpenAI-compatible API (e.g. Ollama at `/v1`).
  - `bwrap` not installed - bubblewrap mode falls back to path confinement with a logged warning.
  - New skill not showing up - restart; discovery happens at startup.

## 10. Authoritative Sources

- `README.md` - primary maintained documentation (features, config, quick start).
- `agent.go` - system-prompt constants that define the agent's actual self-description and operational rules.
- `REFERENCE_SKILL_TOOLS.md` - reference inventory of Hermes Agent skills/tools (candidates for future implementation; hub sync is NOT implemented).
- Deep dives: see `references/` in this skill directory (architecture.md, configuration.md, skills.md, knowledge-base.md, troubleshooting.md).
