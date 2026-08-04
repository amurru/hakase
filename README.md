# Hakase

A high-autonomy, general-purpose AI research and navigation agent built in Go, featuring a rich terminal TUI, Google ADK orchestration with Gemini, MCP server integration, a Python code interpreter, and a self-evolving skill library.

![Go](https://img.shields.io/badge/Go-1.26-blue?logo=go)
![License](https://img.shields.io/badge/License-MIT-green)

---

## Overview

**hakase** is a terminal-based AI agent harness inspired by the Hermes Agent framework. It orchestrates multiple specialized sub-agents — a **Web Researcher** and a **Code Interpreter** — through a Google ADK root orchestrator, powered by Gemini models. The entire interaction happens inside a simple split-pane TUI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

The agent can:

- 🔍 **Browse & research** the web using MCP-connected browser tools
- 📥 **Download** files, PDFs, and images from the internet
- 🐍 **Execute Python** code in an isolated virtual environment with auto-dependency resolution
- 📊 **Analyze data**, generate charts, and produce visual artifacts
- 🧠 **Learn & persist skills** — novel Python workflows are automatically saved to a local skill library for future reuse
- 📚 **Manage a persistent knowledge base** — wiki-style markdown notes with YAML frontmatter and [[wikilinks]] for durable facts the agent learns, with tools to save, recall, search, update, link, cite, and lint
- 🛡️ **Sandboxed execution** — subprocesses and file operations are confined to an approved workspace by default (path-confinement), with optional kernel-level bubblewrap isolation
- 💻 **Run system commands** — execute shell commands, scripts, and executables directly on the host via a `system_exec` toolset
- 📂 **Manage outputs** — generated HTML files, data artifacts, and more are saved to `./outputs/`

---

## Project Structure

```
hakase/
├── main.go                  # Entry point — loads config, boots the TUI and agent runner
├── agent.go                 # Core agent logic: ADK setup, sub-agents, tools (Python interpreter, downloader, skill manager)
├── delegate.go              # Sub-agent delegation — execute_task, progress reporting, dedup cache, watchdog
├── toolcall.go              # Malformed tool-call JSON repair and retry
├── sandbox.go               # Workspace path confinement (root normalization, secure join, containment checks)
├── sandboxexec.go           # bubblewrap (bwrap) subprocess isolation for sandboxed exec
├── systemexec.go            # system_exec toolset — shell routing, process hardening, env scrubbing
├── fileops.go               # File operation tools (read/write/patch/search) with sandbox-aware resolution
├── debug_log.go             # Structured JSON debug logging (info/warn/error levels)
├── skill_discovery.go       # Markdown & Python skill discovery/loading
├── ui.go                    # Bubble Tea TUI — split-pane layout with chat, log, and input views
├── config.go                # Config loader (reads config.json)
├── config.json              # Runtime configuration (API key, model, MCP server URL)
├── config.json.example      # Example config template
├── go.mod / go.sum          # Go module dependencies
├── skills/                  # Persisted Python skill library
├── knowledge/               # Persistent knowledge base (markdown notes with YAML frontmatter + [[wikilinks]])
├── .agents/skills/          # Portable markdown skills (SKILL.md)
├── skill_cli.go             # hakase skill CLI (create/list/validate)
├── downloads/               # Downloaded files (PDFs, images, datasets)
├── outputs/                 # Generated artifacts (HTML files, charts, reports)
└── .venv/                   # Python virtual environment (auto-created)
```

---

## Features

### 🖥️ Terminal TUI

A split-pane terminal interface built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [Lip Gloss](https://github.com/charmbracelet/lipgloss):

- **Left panel** — Chat viewport displaying agent responses and tool call logs
- **Right panel** — Real-time status and execution logs
- **Bottom** — Multi-line text input (auto-grows up to 3 lines) with focus cycling (`Tab` to switch panes) and a hint bar showing the most used shortcuts
- **Help overlay** — Press `Ctrl+/` (`?` when not typing) for a full keyboard shortcut reference

### Keyboard Shortcuts

| Shortcut               | Action                                          |
| ---------------------- | ----------------------------------------------- |
| `Ctrl+C`               | Quit the application                            |
| `Esc`                  | Close the help overlay (never quits)            |
| `Ctrl+/` or `?`        | Toggle the help overlay                         |
| `Tab` / `Shift+Tab`    | Cycle focus: input → chat → log → task          |
| `Ctrl+T`               | Toggle the thinking display                     |
| `Enter`                | Send the message                                |
| `Shift+Enter` / `Ctrl+J` | Insert a newline in the input                 |
| `↑`/`k`, `↓`/`j`       | Scroll the focused pane (older/newer content)   |
| `PgUp`/`b`, `PgDn`/`f` | Page up / down in the focused pane              |
| `u` / `d`              | Half page up / down in the focused pane         |
| `Home`/`g`, `End`/`G`  | Jump to top / bottom of the focused pane        |
| `Ctrl+A` / `Ctrl+E`    | Jump to line start / end in the input           |
| `Ctrl+U`               | Clear the input                                 |

Mouse wheel scrolling works on whichever pane is focused. The log pane stays
pinned to the bottom unless you scroll up to read history.

### 🤖 Multi-Agent Orchestration

Powered by [Google ADK](https://github.com/google/adk):

| Agent                | Role                                                                 |
| -------------------- | -------------------------------------------------------------------- |
| **orchestrator**     | Root agent that delegates tasks to sub-agents based on intent        |
| **web_researcher**   | Searches the web, navigates pages, downloads files, extracts content |
| **code_interpreter** | Executes Python, performs data analysis, manages the skill library   |
| **general_purpose**  | Reads, writes, edits, and searches files in the workspace            |

### 🐍 Python Code Interpreter

- Runs Python code in an isolated `.venv` virtual environment
- **Auto-resolves missing dependencies** — detects `ModuleNotFoundError`, installs the package via pip, and retries
- Sets `PYTHONPATH` to include `./skills` so persisted skills are importable
- **Sandbox-aware** — when the sandbox is active, the script temp dir and working directory are pinned to the workspace root (`.hakase-tmp/`) so script writes stay inside the approved workspace
- **Process hardening** — the interpreter runs in its own process group with a parent-death signal, so children (and grandchildren) are reaped if the agent crashes

### 🧠 Self-Evolving Skill Library

The agent can save tested Python scripts as reusable skills:

1. Code is executed and verified via `python_interpreter`
2. The agent calls `save_skill` to persist the script to `./skills/`
3. Skills are registered in `skills/skills.json` with name, description, and import usage
4. On subsequent runs, the agent loads all saved skills and can reuse them via `from skills.<name> import ...`

### 📚 Knowledge Base

The agent maintains a persistent, wiki-style knowledge base for durable facts it learns. Notes are markdown files with YAML frontmatter and `[[wikilinks]]`, stored in a workspace folder (default `./knowledge/`, configurable via `knowledge_dir` in `config.json`):

```
knowledge/
├── index.md    # auto-maintained catalog of all notes (regenerated on every change)
├── log.md      # append-only operation log ("## [date] action | Title")
├── notes/      # optional subdirectory (preferred when a slug exists in both places)
└── raw/        # optional immutable raw sources (excluded from the index)
```

Each note is markdown with YAML frontmatter: `title`, `aliases`, `tags`, `created`, `updated`, `status` (`draft` / `permanent` / `archived`), `confidence` (`high` / `medium` / `low`), `sources` (URLs or `raw/` paths), `summary`, and `related`. The body contains `[[wikilinks]]` to related notes.

The orchestrator agent exposes eight knowledge tools:

- **`save_knowledge`** - save a new note; unresolved `[[wikilinks]]` are reported as dangling
- **`recall_knowledge`** - load a note by slug, basename, or alias, with backlinks
- **`search_knowledge`** - keyword/tag search across notes
- **`update_knowledge`** - correct or extend an existing note
- **`link_knowledge`** - create `[[wikilinks]]` between notes
- **`cite_knowledge`** - footnote-style citation of a note with its source URL
- **`list_knowledge`** - list all notes
- **`lint_knowledge`** - health check: orphans, dangling links, broken index

Wiki links support `[[target]]`, `[[target|label]]`, and `[[target#heading]]`. Resolution is case-insensitive (slug -> unique basename -> alias). Links to notes that do not exist yet are reported as dangling links; the agent surfaces them to the user and offers to create them, creating only after user confirmation.

Retrieval is keyword/tag/grep only - no embeddings, no vector database, no extra dependencies.

The `hakase knowledge` CLI manages the knowledge base:

- `hakase knowledge list|read|search|lint|create|link` - with a `--dir` flag to override the knowledge directory

```bash
hakase knowledge create "Quantum Computing" --tags physics --content "See [[Superposition]]."
hakase knowledge read quantum-computing
hakase knowledge lint
```

### 📥 File Download

- Downloads files from any HTTP/HTTPS URL
- Saves to `./downloads/` with automatic filename resolution
- Supports PDFs, images, datasets, and binary blobs
- **Filename sanitized** — a supplied filename is stripped to its base component (`filepath.Base`) so `../` traversal attempts are neutralized
- **Sandbox-aware** — when the sandbox is active, the download target is resolved through workspace confinement and rejected if it would land outside the approved workspace

### 📁 File Operations

The `general_purpose` agent provides workspace file tools:

- **`read_file`** — read file contents, optionally restricted to a line range (`offset`/`limit`)
- **`write_file`** — create new files (or overwrite existing ones with `overwrite=true`)
- **`patch`** — targeted string replacement inside an existing file
- **`search_files`** — recursive regex search over file contents with `content` / `files_with_matches` / `count` output modes

Search is hardened against pathological trees: `head_limit` defaults to `100` when unset, the walk visits at most `50,000` entries, and a per-call `30s` deadline gracefully returns partial matches (marked `truncated`) instead of hanging. When the sandbox is active, all four tools resolve paths through workspace confinement (reads confined to read roots, writes to workspace roots).

### 💻 System Command Execution

A `system_exec` toolset runs shell commands, scripts, and executables directly on the host machine, with several safety guarantees:

- **Shell routing** — when no `args` are provided the whole command line is passed to `sh -c`, so pipes, redirects, globs, `&&`/`||`, and compound commands work naturally; explicit `(command, args...)` calls keep full control
- **Process hardening** — spawned processes are placed in their own process group with a parent-death signal, so they and their children are reaped if the agent dies
- **Sandbox integration** — under a `bubblewrap` sandbox the command is wrapped in `bwrap` with filesystem + network isolation; sensitive env vars (`HAKASE_*`, `AWS_*`, `GITHUB_*`, `OPENAI_*`) are scrubbed so they never leak into sandboxed subprocesses; the working directory is pinned to the workspace root

### 🛡️ Sandboxing & Workspace Confinement

hakase confines subprocesses and file operations to approved workspaces out of the box. The `sandbox` block in `config.json` selects a strategy:

| Mode | Description |
| ---- | ----------- |
| `paths` (default) | Pure path confinement — all file ops (`read_file`/`write_file`/`patch`/`search_files`), downloads, and the Python interpreter resolve paths against approved read/work/deny roots. Symlink escapes are prevented via `securejoin` + `EvalSymlinks` re-verification. |
| `bubblewrap` | Adds kernel-level subprocess isolation — `system_exec` and Python runs are wrapped in [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`) with separate PID/IPC/UTS/user namespaces, dropped capabilities, minimal filesystems, read-only system dirs, and optional network unshare. |
| `landlock` | Reserved for future in-process Landlock + seccomp confinement (Phase 3). |
| `off` | Explicitly disables confinement (opt-in only). |

Key properties:

- **On by default** — an absent or unset `sandbox` block yields `paths` mode, so the agent cannot write outside approved workspaces without explicit configuration
- **Roots** — `workspace_roots` (writable, default `["."]`), `read_roots` (readable, default = workspace roots), and `deny_roots` (always rejected, highest precedence); all are symlink-evaluated and de-duplicated
- **Downloads** — the filename is basename-sanitized and the output path is confined to the workspace
- **Current sandbox** — if `bwrap` is not installed, bubblewrap mode falls back to the safe path-confinement exec path and logs a warning

### 🔌 MCP Integration

Connects to an MCP (Model Context Protocol) server for browser automation and web navigation tools. Configured via `config.json` → `mcp_server_url`.

---

## Quick Start

### Prerequisites

- **Go** 1.26+
- **Python 3** — required for the code interpreter (`.venv` execution, auto-dependency resolution) and the self-evolving skill library (`./skills/`)
- A **Google Gemini API key**
- **Lightpanda** — the MCP browser automation server that provides web navigation tools. Install it from [lightpanda.ai](https://lightpanda.ai) and start it before running the agent (it serves the MCP endpoint on `localhost:9223` by default)

### Setup

1. **Clone and configure:**

```bash
cp config.json.example config.json
# Edit config.json with your API key and MCP server URL
```

2. **Install Go dependencies:**

```bash
go mod download
```

3. **Run the agent:**

```bash
go run .
```

The TUI will launch. Type your question and press `Enter`. The agent will research, analyze, and respond — all from your terminal.

### Development Platform

hakase is currently being developed and tested on **Linux**.

### Configuration

Edit `config.json`:

```json
{
  "provider": "gemini",
  "model_name": "gemini-3.5-flash-lite",
  "api_key": "your-gemini-api-key",
  "instruction": "You are a web automation agent harness.",
  "mcp_server_url": "http://localhost:9223/mcp",
  "knowledge_dir": "",
  "sandbox": {
    "mode": "paths"
  }
}
```

### Providers

hakase supports multiple LLM providers, selected via the `provider` field in `config.json`. An empty or missing `provider` value defaults to `gemini`, preserving previous behavior.

| Provider            | Description                                      | Default Model      |
| ------------------- | ------------------------------------------------ | ------------------ |
| `gemini`            | Google Gemini                                    | `gemini-2.5-flash` |
| `openai`            | OpenAI API                                       | `gpt-4o-mini`      |
| `openai-compatible` | OpenAI-compatible endpoints (Ollama, vLLM, etc.) | `gpt-4o-mini`      |

When `model_name` is empty, the provider's default model is used.

**Gemini** (default):

```json
{
  "provider": "gemini",
  "model_name": "gemini-2.5-flash",
  "api_key": "your_gemini_api_key",
  "instruction": "You are a web automation agent harness.",
  "mcp_server_url": "http://localhost:9223/mcp",
  "fallback_providers": ["openai"],
  "base_url": "",
  "provider_options": {}
}
```

**OpenAI**:

```json
{
  "provider": "openai",
  "model_name": "gpt-4o-mini",
  "api_key": "your_openai_api_key",
  "instruction": "You are a web automation agent harness.",
  "mcp_server_url": "http://localhost:9223/mcp"
}
```

**OpenAI-compatible endpoint (e.g. Ollama)**:

```json
{
  "provider": "openai-compatible",
  "model_name": "llama-3.3-70b",
  "api_key": "optional_key",
  "base_url": "http://localhost:11434/v1",
  "instruction": "You are a web automation agent harness.",
  "mcp_server_url": "http://localhost:9223/mcp"
}
```

#### Provider configuration fields

- `base_url` — Base URL for OpenAI-compatible endpoints (e.g. `http://localhost:11434/v1` for Ollama). Ignored when empty; used only by the `openai` / `openai-compatible` providers.
- `fallback_providers` — Optional ordered list of provider names to try if the primary provider fails (e.g. `["openai"]`). Empty by default.
- `provider_options` — Optional map of provider-specific settings. Reserved for future use.
- `knowledge_dir` - Directory for the persistent knowledge base (default `./knowledge`).
- `summary_model` — Optional cheaper/weaker model used for context-compaction summarization (e.g. `gemini-2.5-flash-lite`). When empty, the primary model handles summaries. Set `HAKASE_SUMMARY_MODEL` to override via environment.
- `sandbox` — Optional confinement block (see [Sandboxing & Workspace Confinement](#-sandboxing--workspace-confinement)). Absent → `paths` mode. Fields: `mode` (`paths` | `bubblewrap` | `landlock` | `off`), `workspace_roots`, `read_roots`, `deny_roots`, `allow_network`, `allow_pip_install`, `permissions`.

#### Environment variables

Environment variables override the matching `config.json` fields, with environment variables taking precedence over the file. If `config.json` is missing but at least one of these is set, the config is built entirely from the environment:

| Variable          | Overrides    |
| ----------------- | ------------ |
| `HAKASE_API_KEY`  | `api_key`    |
| `HAKASE_PROVIDER` | `provider`   |
| `HAKASE_MODEL`    | `model_name` |
| `HAKASE_BASE_URL` | `base_url`   |
| `HAKASE_SUMMARY_MODEL` | `summary_model` |
| `HAKASE_DEBUG`    | `debug`      |

Note: `HAKASE_*` variables are scrubbed from the environment of subprocesses spawned by the agent (see `system_exec`), so the API key used for providers never leaks into shell commands or sandboxed Python runs.

#### Migration note

hakase migrated from the ADK v1 stack to the ADK v2 stack (`google.golang.org/adk/v2`). The configuration format is unchanged, so existing `config.json` files continue to work without modification (backward compatible). An empty `provider` field still selects Gemini, matching the previous single-provider behavior.

#### Troubleshooting

- **Unsupported provider error** — `unsupported provider: <name>` means the `provider` field is set to a value other than `gemini`, `openai`, or `openai-compatible`. Correct the value or leave it empty to use the default.
- **Empty API key error** — `gemini provider requires an api_key` or `openai provider requires an api_key` means the `api_key` field is missing for the selected provider. Set a valid key in `config.json`.
- **OpenAI-compatible endpoint unreachable** — when using `openai-compatible`, confirm the server at `base_url` is running and serves an OpenAI-compatible API (e.g. Ollama at `http://localhost:11434/v1`), and that it is reachable from the machine running the agent.

---

## Example Workflows

### Research a Topic

> _"Summarize the latest developments in quantum computing and provide key citations."_

The orchestrator delegates to `web_researcher`, which navigates sources and returns a synthesized Markdown answer.

### Generate an HTML Game

> _"Create a fully playable browser game as a single HTML file."_

The `code_interpreter` writes a self-contained HTML+JS game, saves it to `./outputs/`, and persists the script as a reusable skill in `./skills/`.

### Data Analysis

> _"Download this CSV, compute summary statistics, and generate a chart."_

The agent downloads the file, runs Python with pandas/matplotlib in `.venv`, and saves the output artifact.

---

## Skills

### Markdown Skills

hakase supports markdown-based skills in addition to Python skills. Each skill is a directory containing a `SKILL.md` file with YAML frontmatter and a progressive-disclosure body.

#### The `hakase skill` CLI

The `hakase skill` command manages markdown skills:

- `hakase skill create <name> [--dir <path>] [--description <text>] [--template python] [--force]` - Scaffolds `<dir>/<name>/SKILL.md` with valid frontmatter (`name`, `description`, `license: MIT`, `metadata: author/version`) plus `scripts/` and `references/` subdirectories. The `<name>` must match `^[a-z0-9]+(-[a-z0-9]+)*$`. The default directory is the git project root's `.agents/skills/`. The description falls back to a non-empty placeholder so the skill passes validation immediately. The `--template python` flag also writes `scripts/<name>.py`. Fails on an existing directory unless `--force` is used.
- `hakase skill list` - Prints discovered skills (Python from `./skills/skills.json` plus markdown from project and user directories) with source paths.
- `hakase skill validate <dir>` - Parses and validates a single skill; exits non-zero on failure (CI-friendly).

#### Markdown Skill Format

Each skill directory contains:

- `SKILL.md` - Required. YAML frontmatter (`name` and `description` are required; `license`, `compatibility`, `metadata`, and `allowed-tools` are optional) followed by a progressive-disclosure body.
- `scripts/` - Optional executable code files.
- `references/` - Optional deeper documentation (loaded on demand).

#### Discovery Locations

Skills are discovered from these locations, in priority order (project first, deduped by name, first match wins):

- **Project level** (walk from cwd up to the git root): `.agents/skills/`, `.claude/skills/`, `.opencode/skills/`, `.gemini/skills/`
- **Project library**: `./skills` (existing Python skill library dir; `SKILL.md` files are also scanned here)
- **Custom dirs**: `skill_dirs` from `config.json` (resolved against the project root when relative)
- **User level**: `~/.agents/skills/`, `~/.claude/skills/`, `~/.gemini/skills/`, `~/.config/opencode/skills/` (honoring `XDG_CONFIG_HOME`)

Skills are indexed by name and description in the agent prompt. The full body is loaded on demand via the `load_markdown_skill` tool. Invalid skills are skipped with a warning.

#### Interoperability

Skills authored to this format (e.g. from Claude Code, Codex CLI, Gemini CLI, or OpenCode - the agentskills.io spec) work in hakase by dropping them into `.agents/skills/`.

#### Coexistence with Python Skills

Python skills (`skills.json` + `.py` files) are unchanged. On a name collision, the markdown skill wins in the prompt and the Python entry is omitted with a logged warning (the `.py` file remains importable).

#### Operational Notes

- Skills added mid-session require a restart to be discovered.
- `.agents/skills/` is meant to be committed to the repository.

---

## Dependencies

| Package                                  | Purpose                                   |
| ---------------------------------------- | ----------------------------------------- |
| `charm.land/bubbletea/v2`                | TUI framework                             |
| `charm.land/bubbles/v2`                  | TUI components (text input, viewport)     |
| `charm.land/lipgloss/v2`                 | Terminal styling                          |
| `google.golang.org/adk/v2`               | Google Agent Development Kit (v2; was v1) |
| `google.golang.org/genai`                | Gemini AI client                          |
| `github.com/openai/openai-go/v3`         | OpenAI API client                         |
| `github.com/modelcontextprotocol/go-sdk` | MCP client for browser automation         |
| `github.com/cyphar/filepath-securejoin` | Symlink-safe secure path joining for sandbox confinement |

---

## License

MIT
