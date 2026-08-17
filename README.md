# Hakase

A high-autonomy, general-purpose AI research and navigation agent built in Go, featuring a rich terminal TUI, Google ADK orchestration across multiple model providers (Gemini, OpenAI, and OpenAI-compatible endpoints), MCP server integration, a Python code interpreter, and a self-evolving skill library.

![Go](https://img.shields.io/badge/Go-1.26-blue?logo=go)
![License](https://img.shields.io/badge/License-MIT-green)

---

## Overview

**hakase** is a terminal-based AI agent harness inspired by the Hermes Agent framework. It orchestrates multiple specialized sub-agents — a **Web Researcher** and a **Code Interpreter** — through a Google ADK root orchestrator, powered by configurable LLM providers (Gemini, OpenAI, or any OpenAI-compatible endpoint). The entire interaction happens inside a simple split-pane TUI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

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
- ⏰ **Schedule recurring tasks** — a `cronjob` tool for one-shot and recurring agent tasks (research digests, monitoring, periodic reports) with cron/interval/ISO schedules, persisted to `~/.hakase/cronjobs.json` and fired by a background scheduler

---

## Project Structure

```
hakase/
├── main.go                  # Entry point — loads config, boots the TUI and agent runner
├── agent.go                 # Core agent logic: ADK setup, sub-agents, tools (Python interpreter, downloader, skill manager)
├── cronjob.go               # Scheduled tasks - cronjob tool, scheduler, ~/.hakase/cronjobs.json registry
├── delegate.go              # Sub-agent delegation — execute_task, progress reporting, dedup cache, watchdog
├── toolcall.go              # Malformed tool-call JSON repair and retry
├── sandbox.go               # Workspace path confinement (root normalization, secure join, containment checks)
├── sandboxexec.go           # bubblewrap (bwrap) subprocess isolation for sandboxed exec
├── systemexec.go            # system_exec toolset — shell routing, process hardening, env scrubbing
├── fileops.go               # File operation tools (read/write/patch/search) with sandbox-aware resolution
├── debug_log.go             # Structured JSON debug logging (info/warn/error levels)
├── skill_discovery.go       # Markdown & Python skill discovery/loading
├── instruction_context.go   # Project context files (AGENTS.md) discovery & rendering
├── env.go                   # Runtime environment detection (OS/distro/arch, package manager, toolchains)
├── env_cli.go               # hakase env CLI (print the detected environment block)
├── rule_cli.go              # hakase rules CLI (list/show project context files)
├── ui.go                    # Bubble Tea TUI — split-pane layout with chat, log, and input views
├── config.go                # Config loader (reads config.json)
├── config.json              # Runtime configuration (API key, model, MCP server URL)
├── config.json.example      # Example config template
├── go.mod / go.sum          # Go module dependencies
├── skills/                  # Persisted Python skill library
├── knowledge/               # Persistent knowledge base (markdown notes with YAML frontmatter + [[wikilinks]])
├── .agents/skills/          # Portable markdown skills (SKILL.md) - includes the hakase self-skill
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
- **Mid-run messaging** — Type and send while the agent is working: your message is queued (shown as `N queued` in the hint bar), steered into the running session at the next model-call boundary as a `USER INTERJECTION`, then processed as its own turn when the current run completes
- **Mid-run questions** — The agent can pause and ask you a question mid-task via the `clarify` tool; choose from up to 4 options or type a free-text answer, [esc] to dismiss
- **Help overlay** — Press `Ctrl+/` (`?` when not typing) for a full keyboard shortcut reference
- **Inline math rendering** — LaTeX math in agent responses renders natively in the chat pane: display math (`$$...$$`) compiles to a transparent PNG via tectonic + poppler and displays through the kitty graphics protocol in kitty/WezTerm/ghostty terminals; everywhere else it degrades to a Unicode character grid (stacked fractions, `∑`/`∫` limits, matrix delimiters) via the pure-Go termtex parser. Inline math (`$...$`) always uses Unicode. Streaming shows Unicode math that upgrades to images when the message completes; no terminal or toolchain is required for the fallback to work.

### Keyboard Shortcuts

| Shortcut               | Action                                          |
| ---------------------- | ----------------------------------------------- |
| `Ctrl+C`               | Quit the application (also cancels a running agent) |
| `Esc` `Esc`            | Interrupt the running agent (double-press within 2s) |
| `Esc`                  | Close the help overlay (never quits)            |
| `Ctrl+/` or `?`        | Toggle the help overlay                         |
| `Tab` / `Shift+Tab`    | Cycle focus: input → chat → log → task          |
| `Ctrl+T`               | Toggle the thinking display                     |
| `Enter`                | Send the message (queued while the agent is busy) |
| `Shift+Enter` / `Ctrl+J` | Insert a newline in the input                 |
| `↑`/`k`, `↓`/`j`       | Scroll the focused pane (older/newer content)   |
| `PgUp`/`b`, `PgDn`/`f` | Page up / down in the focused pane              |
| `u` / `d`              | Half page up / down in the focused pane         |
| `Home`/`g`, `End`/`G`  | Jump to top / bottom of the focused pane        |
| `Ctrl+A` / `Ctrl+E`    | Jump to line start / end in the input           |
| `Ctrl+U`               | Clear the input                                 |

Mouse wheel scrolling works on whichever pane is focused. The log pane stays
pinned to the bottom unless you scroll up to read history.

### Slash Commands

Type `/` in the input to see a filtered command menu (arrow keys navigate,
`Tab` completes, `Enter` runs). Built-in commands:

| Command                | Action                                                        |
| ---------------------- | ------------------------------------------------------------- |
| `/board`               | Task board: `summary`, `list`, `new <title>`, `get <id>`, `update <id>`, `done <id>`, `fail <id>`, `cancel <id>`, `delete <id>`, `archive <id>`, `claim <id>` |
| `/mcp`                 | Manage MCP servers: open the interactive server panel, or `list` / `enable <name>` / `disable <name>` / `reconnect <name>` |
| `/compact [focus]`     | Summarize the conversation to free context, continuing the same session; optional focus instructions steer the summary |
| `/new`                 | Start a fresh session (previous sessions stay resumable)      |
| `/sessions`            | Open the session chooser to switch or resume old sessions     |
| `/help`                | Show the keyboard shortcut and slash command reference        |
| `/exit` / `/quit`      | Exit hakase                                                   |

`/compact` runs the deterministic history snip immediately (keeps the last
two turns) and schedules an async LLM summary - the same compaction cascade
used by automatic context management (`summary_model`), exposed manually.

### File Attachments

Attach files and images to a message without the agent having to find them:

- **`@file`** - type `@` to open a workspace file picker; arrow keys
  navigate, `Enter` attaches the highlighted file as a chip (`@name.go`).
  Text files embed their content; images embed as multimodal input.
- **Image paste** - copy an image (screenshot) and press `Ctrl+V`; it is
  read from the clipboard and attached as a `[image 1]` chip. Text pastes
  still work normally.
- Chips render in a row above the input; `Backspace` on an empty input
  removes the last chip. Attachments are sent alongside the prompt text and
  are persisted with the session (re-attached on resume).

Sandbox note: `@` paths resolve through the sandbox read roots - files
outside the approved workspace are rejected with a hint.

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

#### Skill evolution (darwinian-evolver style)

hakase ships a native, cron-driven evolution loop over the Python skill
library (`evolver.go`), inspired by the darwinian-evolver contract (no AGPL
import - the upstream `imbue-ai/darwinian_evolver` is never wrapped):

- **Evaluator** - each skill's `skills/<name>.eval.json` defines input /
  expected cases (trainable + holdout split). Skills without an eval set, or
  whose module fails to load, are skipped.
- **Mutator** - failing skills are fed to the configured model (current
  source + failure cases) which proposes a fixed implementation.
- **A/B gate** - a mutation is promoted only when it beats the incumbent by
  >=5% on the trainable score with zero holdout regressions. The incumbent
  is preserved as `<name>.py.bak`. Skills with an eval hit rate below 30%
  are auto-deprecated in `skills/skills.json`.
- **Driver** - run `hakase skill evolve [--mutate]` for a manual pass, or
  schedule the nightly pass as a native cron job (`native: "evolve"` via the
  `cronjob` tool, or `hakase cron`). Every pass writes an auditable report
  to `outputs/cron/evolve-*.md` for human review. No live self-modification.

The `darwinian-evolver` markdown skill (`.agents/skills/darwinian-evolver/`)
documents the loop, the eval-set format, and the cron wiring.

#### Reflexion (lessons learned)

The orchestrator writes "lessons learned" knowledge notes after failed or
complex tasks (tagged `lessons-learned`) and recalls them at session start,
so hard-won solutions and dead-ends are not re-learned.

### 📚 Knowledge Base

The agent maintains a persistent, wiki-style knowledge base for durable facts it learns. Notes are markdown files with YAML frontmatter and `[[wikilinks]]`, stored in a workspace folder (default `./knowledge/`, configurable via `knowledge_dir` in `config.json`):

```
knowledge/
├── index.md    # auto-maintained catalog of all notes (regenerated on every change)
├── log.md      # append-only operation log ("## [date] action | Title")
├── notes/      # optional subdirectory (preferred when a slug exists in both places)
└── raw/        # optional immutable raw sources (excluded from the index)
```

Each note is markdown with YAML frontmatter: `title`, `aliases`, `tags`, `created`, `updated`, `status` (`draft` / `permanent` / `archived`), `confidence` (`high` / `medium` / `low`), `sources` (URLs or `raw/` paths), `summary`, `related`, and `metadata` (structured key/value facts extracted at save time, e.g. GitHub project fields). The body contains `[[wikilinks]]` to related notes.

The orchestrator agent exposes eight knowledge tools:

- **`save_knowledge`** - save a new note; the note is auto-enriched with the configured summarization model (summary, excerpt, tags, aliases, related notes, and structured metadata), deterministic extraction is the fallback, existing notes that are related are auto-linked under a Related section, GitHub project metadata (owner, maintainers, stars, language, license) is captured when a repository is referenced, and unresolved `[[wikilinks]]` are reported as dangling
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
- `hakase knowledge bench [--dir <path>] [--k 5] [--eval <file>]` - runs the search-quality benchmark (qmd `qmd bench` analog): reads query -> expected-slug pairs from `<knowledge_dir>/bench.json` (or `--eval`), runs each query through the relevance-ranked search, and reports recall@k / MRR. The eval-set format is shared with the skill evolver.

```bash
hakase knowledge create "Quantum Computing" --tags physics --content "See [[Superposition]]."
hakase knowledge read quantum-computing
hakase knowledge lint
hakase knowledge bench
```

Search results are relevance-ranked (BM25-style: title/alias/tag matches
outrank summary/metadata/body, with an alphabetical tiebreak) while keeping
the exact same result set as the old substring search. Optional HyDE-lite
LLM query expansion is available via the `search_expansion` config field
(default off).

#### The `hakase cron` CLI

The `hakase cron` command manages scheduled tasks:

- `hakase cron list|status|pause <id>|resume <id>|run <id>|tick` - list all jobs, show the registry path and state counts, pause/resume a job by ID or name, trigger a job immediately, or run all due jobs once

The in-process scheduler runs while the TUI is open (a 30-second tick fires due jobs headless); `hakase cron tick` runs all due jobs once from the CLI. `run` and `tick` bootstrap the model for headless execution; the other subcommands are pure file operations.

### 📄 Project Context Files (AGENTS.md)

hakase loads project context files - `AGENTS.md` - into every agent's system instruction, so repository conventions, architecture notes, and coding rules are followed without being repeated in every prompt. The semantics match the conventions used by OpenCode and Hermes Agent, so context files authored for those agents work unchanged:

- **Project scope** - `AGENTS.md` files are collected from the current directory up to the git root (closest first; nested files stack). Only when no `AGENTS.md` exists anywhere in the walk is a **project-scoped** `CLAUDE.md` used as a fallback.
- **User scope** - `~/.hakase/AGENTS.md` (or `$HAKASE_HOME/AGENTS.md`) when present. The Claude Code global `~/.claude/CLAUDE.md` is deliberately **never** loaded.
- **Custom files** - `instruction_files` in `config.json` adds more context: absolute paths, `~/`-prefixed paths, project-relative paths, or `http(s)://` URLs (fetched at startup with a short timeout; failures are skipped, never fatal).

Each loaded file is rendered as `Instructions from: <path>` followed by its content under a `### PROJECT CONTEXT FILES:` header. Content is **prompt-injection scanned** (matching files are blocked and replaced with a warning) and **truncated per file** (Hermes-style 70% head / 20% tail split, default 20,000 characters, configurable via `context_files.max_chars`). The rendered block is accounted for in the context-compaction token budget, so large files cannot silently blow the model window.

The block is injected into the **orchestrator** and all sub-agents by default; `context_files.apply_to` restricts it to a named subset (`orchestrator`, `web_researcher`, `code_interpreter`, `general_purpose`).

```json
{
  "instruction_files": ["docs/rules.md", "https://example.com/team-agents.md"],
  "context_files": {
    "max_chars": 20000,
    "apply_to": ["orchestrator", "general_purpose"]
  }
}
```

#### Progressive subdirectory context

Beyond the startup block, reading a file (`read_file`) or searching a directory (`search_files`) below the workspace root attaches any `AGENTS.md` in that directory tree - not already in the system prompt - to the tool result under a `SUBDIRECTORY CONTEXT` header. Each file is attached **once per session**, injection-scanned, and capped at 8,000 characters per file. This keeps deep-nested conventions in the model's view without bloating the system prompt.

#### Live reconcile

If a loaded context file changes mid-session, hakase detects it (cheap path/size/mtime fingerprint, checked before every model call) and injects a one-shot `PROJECT CONTEXT UPDATE` notice so the model follows the updated instructions.

#### The `hakase rules` CLI

Preview the active context without running the agent:

```bash
hakase rules list    # list the context files that would be loaded (render order + scope)
hakase rules show AGENTS.md   # show one file's content (path or basename)
```

### 📐 Preferred Measurement Units

hakase can report physical quantities (distance, mass, volume, temperature, speed, area) in your preferred measurement system. Set `units.system` in `config.json` (or in the web UI's **Settings → Preferred Measurement Units**):

```json
{
  "units": { "system": "metric" }
}
```

- `metric` (default) — meters/km, kilograms, liters, degrees Celsius, km/h, m²/ha (SI / ISO).
- `imperial` — miles, pounds, US gallons, degrees Fahrenheit, mph, sq ft/acres.

When unset, metric (SI / ISO) is used for every agent. The preference is injected as a system reminder into every agent's instruction, so the agent converts and presents values in your chosen system whenever a task involves units (the original value may be shown in parentheses on first mention). The user can always override per-task by asking for a specific system.

### 🖥️ Runtime Environment Awareness

At session start hakase detects the environment it runs on and injects a compact
block into every agent's system instruction, so the model acts on facts instead
of assumptions - e.g. it knows `pacman` is the package manager on Arch rather
than guessing `apt`, and which compilers/interpreters actually exist before
choosing an execution strategy.

The block reports (detected once at startup, omitted line-by-line when
undetectable):

- **OS / architecture / distro** - e.g. `linux/amd64 (Arch Linux)`, plus the
  kernel release on Linux
- **Package manager** - resolved by PATH availability first (handles hybrids
  like Nix-on-Ubuntu), with a distro-ID map fallback (`pacman`, `apt`, `dnf`,
  `zypper`, `apk`, `brew`, ...)
- **Shell, locale, timezone** - `$SHELL`, `$LC_ALL`/`$LANG`, and the current
  zone + UTC offset
- **User, home, workspace** - identity and the workspace root
- **Disk & memory** - free space on the workspace filesystem and total memory
- **Toolchains** - available compilers/interpreters/VCS with their versions
  (`go`, `gcc`, `clang`, `rustc`, `python3`, `node`, `git`, `docker`), probed
  with a 1s timeout and never fatal

The block is labeled a startup snapshot; the model is directed to use
`system_exec` for live system state (processes, current disk/memory, network).
Two safeguards keep the snapshot honest:

- **Staleness refresh** - if disk free or available memory drifts materially
  (>= 1 GiB or >= 5%) since the startup snapshot, hakase injects a one-shot
  `ENVIRONMENT UPDATE` notice into the running session (checked before each
  model call, same mechanism as the context-file live reconcile) so the model
  re-checks live state instead of trusting stale values.
- **Sandbox note** - when the sandbox is `bubblewrap` (subprocess isolation),
  the block carries a note that host-detected toolchains may be unreachable
  inside the sandbox, steering code execution to `python_interpreter`.

The `hakase env` CLI prints the exact block without running the agent:

```bash
hakase env
```

Tuning via `config.json` (defaults shown; the block is enabled by default):

```json
{
  "system_env": {
    "enabled": true,
    "max_chars": 800,
    "apply_to": []
  }
}
```

- `enabled` - `false` disables the block entirely.
- `max_chars` - caps the rendered block (0 uses the 800-char default). The
  block's token size is accounted for in the context-compaction budget.
- `apply_to` - restricts which agents receive the block; empty means all four
  agents (`orchestrator`, `web_researcher`, `code_interpreter`,
  `general_purpose`), mirroring `context_files.apply_to`.

Linux is the primary platform: on other OSes the portable fields (OS, arch,
shell, locale, timezone, user, toolchains) are still reported and the
Linux-specific ones (kernel, distro, disk, memory) degrade gracefully.

### 📥 File Download

- Downloads files from any HTTP/HTTPS URL
- Saves to `./downloads/` with automatic filename resolution
- Supports PDFs, images, datasets, and binary blobs
- **Filename sanitized** — a supplied filename is stripped to its base component (`filepath.Base`) so `../` traversal attempts are neutralized
- **Sandbox-aware** — when the sandbox is active, the download target is resolved through workspace confinement and rejected if it would land outside the approved workspace

### 👁️ Vision

The `vision` tool loads an image (URL, local file path, or `data:` URL) so the model can see it. When the main model is vision-capable, the image is attached directly to the model context; otherwise a configured `vision_model` describes the image as text. Images are SSRF-guarded, size-capped, and auto-converted or resized to fit provider limits. Configure via `vision_model`, `vision_provider`, `vision_base_url`, `vision_api_key`, and `model_vision`.

Attached images (`@file` or pasted screenshots) are handled the same way: on a non-vision main model they are described by `vision_model` before reaching the model (required on OpenAI-compatible providers, whose adapter rejects raw image parts); on a vision-capable model they pass through as inline input.

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
- **Path confinement (all sandbox modes)** — when the sandbox is active, absolute path arguments in the command line are audited against the sandbox read roots and trusted system dirs (`/usr`, `/lib`, `/bin`, `/etc`, `/proc`, `/dev`, `/sys`, `/tmp`, `/run`); anything else is rejected with an actionable error. This stops whole-filesystem scans like `find / -type d -name skills` from escaping the workspace. Add directories to `sandbox.read_roots` in `config.json` to permit them.
- **Default timeout** — synchronous `system_exec` kills the command after 120s when `timeout_seconds` is omitted, so a hung command can never block the agent indefinitely. Long-running work should use `system_exec_start` (background) or an explicit `timeout_seconds`.
- **Sandbox integration** — under a `bubblewrap` sandbox the command is wrapped in `bwrap` with filesystem + network isolation; sensitive env vars (`HAKASE_*`, `AWS_*`, `GITHUB_*`, `OPENAI_*`) are scrubbed so they never leak into sandboxed subprocesses; the working directory is pinned to the workspace root

### 🛡️ Sandboxing & Workspace Confinement

hakase confines subprocesses and file operations to approved workspaces out of the box. The `sandbox` block in `config.json` selects a strategy:

| Mode | Description |
| ---- | ----------- |
| `paths` (default) | Pure path confinement — all file ops (`read_file`/`write_file`/`patch`/`search_files`), downloads, and the Python interpreter resolve paths against approved read/work/deny roots. `system_exec` commands are audited so absolute path arguments must stay under the read roots or trusted system dirs. Symlink escapes are prevented via `securejoin` + `EvalSymlinks` re-verification. |
| `bubblewrap` | Adds kernel-level subprocess isolation — `system_exec` and Python runs are wrapped in [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`) with separate PID/IPC/UTS/user namespaces, dropped capabilities, minimal filesystems, read-only system dirs, and optional network unshare. |
| `landlock` | Reserved for future in-process Landlock + seccomp confinement (Phase 3). |
| `off` | Explicitly disables confinement (opt-in only). |

Key properties:

- **On by default** — an absent or unset `sandbox` block yields `paths` mode, so the agent cannot write outside approved workspaces without explicit configuration
- **Roots** — `workspace_roots` (writable, default `["."]`), `read_roots` (readable, default = workspace roots), and `deny_roots` (always rejected, highest precedence); all are symlink-evaluated and de-duplicated
- **Downloads** — the filename is basename-sanitized and the output path is confined to the workspace
- **Current sandbox** — if `bwrap` is not installed, bubblewrap mode falls back to the safe path-confinement exec path and logs a warning

### 🔌 MCP Integration

hakase is an MCP (Model Context Protocol) **client**. Any number of stdio or
streamable-HTTP MCP servers can be configured, and their tools are exposed to
the orchestrator (and delegated sub-agents) dynamically as `mcp_<server>_<tool>`
tools. Manage servers at runtime from the TUI with `/mcp` - the interactive
panel shows per-server status and supports enable / disable / reconnect without
a restart (toggles apply on the next message).

Configuration lives in the `mcp` block of `config.json` (project scope, never
written by the app) merged with the user registry at `~/.hakase/mcp.json`
(written by the TUI):

```json
{
  "mcp": {
    "servers": {
      "lightpanda": { "type": "http", "url": "http://localhost:9223/mcp" },
      "github": {
        "type": "stdio",
        "command": ["npx", "-y", "@github/mcp-server"],
        "env": { "GITHUB_PAT": "${GITHUB_PAT}" },
        "tools": { "exclude": ["mcp_github_delete_repo"] }
      }
    }
  }
}
```

- `type`: `stdio` (default when `command` is set) or `http`.
- `command` + `env`: the stdio child process. `HAKASE_*`/`AWS_*`/`GITHUB_*`/
  `OPENAI_*` are scrubbed from its environment (same rule as `system_exec`);
  the server's own `env` block is applied on top, with `${VAR}` /
  `${VAR:-default}` expansion (also applied to `url`, `headers`, and command
  args).
- `url` + `headers`: a streamable-HTTP endpoint. `headers` values are
  env-expanded, so keep tokens out of committed config.
- `disabled: true`: explicit opt-out (new servers default to enabled).
- `tools.include` / `tools.exclude`: per-server allow/deny lists on the
  namespaced tool names (`mcp_<server>_<tool>`); exclude wins.
- `timeout_ms` and `oauth` are reserved for the auth phase (remote OAuth).

The legacy single-server `mcp_server_url` field still works and is
auto-migrated to a server named `lightpanda`.

---

## Quick Start

### Prerequisites

- **Go** 1.26+
- **Python 3** — required for the code interpreter (`.venv` execution, auto-dependency resolution) and the self-evolving skill library (`./skills/`)
- An **API key for your chosen provider** — Google Gemini, OpenAI, or an OpenAI-compatible endpoint (e.g. Ollama, vLLM). The key requirement depends on the `provider` field in `config.json`.
- **Lightpanda** — the MCP browser automation server that provides web navigation tools. Install it from [lightpanda.ai](https://lightpanda.ai) and start it before running the agent (it serves the MCP endpoint on `localhost:9223` by default)

### Setup

1. **Clone and configure:**

```bash
cp config.json.example config.json
# Edit config.json with your API key (matching your provider) and MCP server URL
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
  "model_name": "gemini-2.5-flash",
  "api_key": "your-gemini-api-key",
  "instruction": "You are a web automation agent harness.",
  "mcp_server_url": "http://localhost:9223/mcp",
  "mcp": {
    "servers": {
      "lightpanda": { "type": "http", "url": "http://localhost:9223/mcp" }
    }
  },
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
| `gemini`            | Google Gemini                                    | `gemini-3.7-flash` |
| `openai`            | OpenAI API                                       | `gpt-5.6-terra`    |
| `openai-compatible` | OpenAI-compatible endpoints (Ollama, vLLM, etc.) | `gpt-5.6-terra`    |

When `model_name` is empty, the provider's default model is used.

**Gemini** (default):

```json
{
  "provider": "gemini",
  "model_name": "gemini-3.7-flash",
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
  "model_name": "gpt-5.6-terra",
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
- `instruction` - Optional, additional customization rendered into the agent instructions as a `USER CONFIG INSTRUCTION` section (alongside the discovered `AGENTS.md` context). It is not a replacement for the built-in system prompts - it only adds.
- `instruction_files` - Optional list of extra context files merged into the project context (see [Project Context Files](#-project-context-files-agentsmd)): absolute paths, `~/`-prefixed paths, project-relative paths, or `http(s)://` URLs.
- `context_files` - Optional tuning for the project context files: `max_chars` (per-file truncation cap, default `20000`) and `apply_to` (restrict which agents receive the block; empty = all).
- `system_env` - Optional tuning for the runtime-environment block (see [Runtime Environment Awareness](#-runtime-environment-awareness)): `enabled` (default `true`), `max_chars` (block cap, default `800`), and `apply_to` (restrict which agents receive the block; empty = all).
- `mcp_server_url` - Legacy single MCP server URL (Lightpanda browser automation). Auto-migrated to a server named `lightpanda` in the `mcp` block.
- `mcp` - Optional MCP server configuration (see [MCP Integration](#-mcp-integration)): `servers` map of name -> `{type, command, env, url, headers, disabled, tools, timeout_ms, oauth}`.
- `knowledge_dir` - Directory for the persistent knowledge base (default `./knowledge`; a leading `~` expands to the user home, e.g. `~/.hakase/knowledge` for a user-global base).
- `search_expansion` - Optional HyDE-lite LLM query expansion for `search_knowledge` (default `false`). When off, search behavior is byte-identical to plain substring search (just relevance-ordered). When on, the summarization model rephrases the query into 2-3 phrasings which are OR-matched and fused with Reciprocal Rank Fusion; on failure or timeout it falls back silently to plain substring search. Set `HAKASE_SEARCH_EXPANSION` to override via environment.
- `summary_model` — Optional cheaper/weaker model used for context-compaction summarization (e.g. `gemini-3.5-flash-lite`). When empty, the primary model handles summaries. Set `HAKASE_SUMMARY_MODEL` to override via environment.
- `vision_model` - Optional multimodal model used to describe images as text when the main model lacks vision (legacy mode). Empty = disabled.
- `vision_base_url` - Optional separate endpoint for the vision model; empty = primary `base_url`.
- `vision_api_key` - Optional separate key for the vision model; empty = primary `api_key`.
- `vision_provider` - Optional provider for the vision model: `gemini`, `openai`, or `openai-compatible`. Empty = primary provider (a `vision_base_url` alone still forces an OpenAI-compatible endpoint). Use this when the vision model lives on a different backend than the main model - e.g. a Gemini vision model while the primary provider is OpenAI-compatible.
- `model_vision` - Override multimodal detection for the main model: `auto` | `yes` | `no` (default `auto`).
 - `sandbox` — Optional confinement block (see [Sandboxing & Workspace Confinement](#-sandboxing--workspace-confinement)). Absent → `paths` mode. Fields: `mode` (`paths` | `bubblewrap` | `landlock` | `off`), `workspace_roots`, `read_roots`, `deny_roots`, `allow_network`, `allow_pip_install`, `permissions`.
 - `loop_guard` — Optional anti-degeneration guardrails that abort a run stuck in a repetition loop or text-only bloat instead of burning the whole context/output window. Zero values use the defaults. Fields: `max_output_tokens` (cap on provider `maxOutputTokens`, default `8192`), `repetition_limit` (abort after this many consecutive identical non-thought chunks, default `8`), `max_text_without_tool` (abort after this many runes of text with zero tool calls, default `20000`). Set `HAKASE_MAX_OUTPUT_TOKENS` to override the cap via environment.

#### Environment variables

Environment variables override the matching `config.json` fields, with environment variables taking precedence over the file. If `config.json` is missing but at least one of these is set, the config is built entirely from the environment:

| Variable          | Overrides    |
| ----------------- | ------------ |
| `HAKASE_API_KEY`  | `api_key`    |
| `HAKASE_PROVIDER` | `provider`   |
| `HAKASE_MODEL`    | `model_name` |
| `HAKASE_BASE_URL` | `base_url`   |
| `HAKASE_SUMMARY_MODEL` | `summary_model` |
| `HAKASE_VISION_MODEL` | `vision_model` |
| `HAKASE_VISION_BASE_URL` | `vision_base_url` |
| `HAKASE_VISION_API_KEY` | `vision_api_key` |
| `HAKASE_VISION_PROVIDER` | `vision_provider` |
| `HAKASE_MODEL_VISION` | `model_vision` |
| `HAKASE_DEBUG`    | `debug`      |
| `HAKASE_MAX_OUTPUT_TOKENS` | `loop_guard.max_output_tokens` |
| `HAKASE_HOME`     | user home directory (default `~/.hakase`) |

Note: `HAKASE_*` variables are scrubbed from the environment of subprocesses spawned by the agent (see `system_exec`), so the API key used for providers never leaks into shell commands or sandboxed Python runs.

#### User home (`~/.hakase/`)

User-level agent state lives under `~/.hakase/` (Claude-style; override with `$HAKASE_HOME`):

- `~/.hakase/config.json` - user-level config fallback, used when no project `config.json` exists
- `~/.hakase/skills/` - user-level markdown skills, discovered automatically
- `~/.hakase/knowledge/` - optional user-global knowledge base (set `knowledge_dir: "~/.hakase/knowledge"`)

#### Migration note

hakase migrated from the ADK v1 stack to the ADK v2 stack (`google.golang.org/adk/v2`). The configuration format is unchanged, so existing `config.json` files continue to work without modification (backward compatible). An empty `provider` field still selects Gemini, matching the previous single-provider behavior.

#### Troubleshooting

- **Unsupported provider error** — `unsupported provider: <name>` means the `provider` field is set to a value other than `gemini`, `openai`, or `openai-compatible`. Correct the value or leave it empty to use the default.
- **Empty API key error** — `gemini provider requires an api_key` or `openai provider requires an api_key` means the `api_key` field is missing for the selected provider. Set a valid key in `config.json`.
- **OpenAI-compatible endpoint unreachable** — when using `openai-compatible`, confirm the server at `base_url` is running and serves an OpenAI-compatible API (e.g. Ollama at `http://localhost:11434/v1`), and that it is reachable from the machine running the agent.

---

## Web Interface

hakase ships two HTTP server modes for browser access:

- **`hakase web`** - serves the full web UI (single-page app + API) at `http://127.0.0.1:8080` by default. Open the printed URL (`Hakase web UI: http://...`) in a browser and log in.
- **`hakase serve`** - API-only mode at `http://127.0.0.1:8081` by default. No SPA is served; useful when you run your own frontend or script against the API directly.

Both modes share the same flags:

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--port <n>` | `8080` (web) / `8081` (serve) | Port to listen on |
| `--host <addr>` | `127.0.0.1` | Host address to bind to |

```bash
hakase web                 # SPA + API on http://127.0.0.1:8080
hakase serve               # API-only on http://127.0.0.1:8081
hakase web --port 9000     # custom port
hakase web --host 0.0.0.0  # bind all interfaces (see warning)
```

`--host 0.0.0.0` exposes the server on every network interface. hakase prints a warning and recommends putting a reverse proxy in front for TLS termination instead (see [Reverse Proxy (Caddy)](#reverse-proxy-caddy)).

## Authentication

Before the server will start, you must create the admin credentials:

```bash
hakase auth set-password
```

The command prompts for a username and a password, hashes the password with **argon2id**, and writes the credentials to `~/.hakase/credentials.json` (mode `0600`, never stored in cleartext). If the file already exists, the current password must be entered before it can be changed.

- **Web UI** - log in through the browser; the server issues a JWT stored in an HttpOnly cookie.
- **API** - authenticate with a bearer token (the same JWT) on each request.

The JWT signing secret lives at `~/.hakase/jwt-secret` (generated on first run, mode `0600`).

## Reverse Proxy (Caddy)

The hakase HTTP server does **not** handle TLS. Terminate HTTPS at a reverse proxy. [Caddy](https://caddyserver.com) is the easiest option because it obtains and renews Let's Encrypt certificates automatically:

```
hakase.example.com {
    reverse_proxy localhost:8080
}
```

Point your domain's DNS at the machine, run Caddy, and the site is served over HTTPS automatically. Caddy also handles the domain name, and you can layer on optional protection:

```
hakase.example.com {
    reverse_proxy localhost:8080
    basic_auth {
        user hashed-password
    }
}
```

Generate the hashed password with `caddy hash-password`. An IP allowlist works too (for example `@blocked not remote_ip 192.0.2.0/24` with `respond @blocked 403`) - see the [Caddy docs](https://caddyserver.com/docs). nginx or any other reverse proxy works the same way.

## Production Deployment

- **Use a strong password** for the admin account set with `hakase auth set-password`.
- **Bind to localhost and proxy** - keep the default `--host 127.0.0.1` and let the reverse proxy forward to it. Never expose the Go server directly to the internet.
- **Protect `~/.hakase/credentials.json`** - the server enforces mode `0600`; keep the file owned by the hakase user and out of backups or repositories.
- **Rotate the JWT secret** - `~/.hakase/jwt-secret` signs every session. Regenerate it periodically to invalidate outstanding tokens (existing sessions will need to log in again).
- **TLS is the proxy's job** - hakase serves plain HTTP only. Always terminate HTTPS at the reverse proxy, and never run `--host 0.0.0.0` without one.

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
- `hakase skill evolve [--dir ./skills] [--mutate] [--report <path>]` - Runs one darwinian-evolver-style skill-evolution pass over the Python skill library (see [Self-Evolving Skill Library](#-self-evolving-skill-library)). Default is evaluation-only: every skill with a `skills/<name>.eval.json` eval set is scored, skills below the 30% hit-rate threshold are auto-deprecated, and an auditable report is written to `outputs/cron/evolve-<timestamp>.md`. `--mutate` enables the mutator step (requires a configured model): failing skills are mutated via the model, and only mutations that beat the incumbent by >=5% with zero holdout regressions are promoted (the incumbent is preserved as `<name>.py.bak`).

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
- **User level**: `~/.hakase/skills/` (or `$HAKASE_HOME/skills/`), `~/.agents/skills/`, `~/.claude/skills/`, `~/.gemini/skills/`, `~/.config/opencode/skills/` (honoring `XDG_CONFIG_HOME`)

Skills are indexed by name and description in the agent prompt. The full body is loaded on demand via the `load_markdown_skill` tool. Invalid skills are skipped with a warning. Each markdown skill listing in the prompt includes its discovery source directory (e.g. `Location: <root>/.agents/skills`), so the agent knows where existing skills actually live.

When the agent creates a new markdown skill, the prompt instructs it to prefer the project root's `.agents/skills/` (the portable, always-scanned location, and the default target of `hakase skill create`). If writing there fails, it may fall back to any other valid discovery location in priority order - the project's `.claude/skills/`, `.opencode/skills/`, or `.gemini/skills/`, then the user-level `~/.hakase/skills/`, `~/.agents/skills/`, `~/.claude/skills/`, `~/.gemini/skills/`, or `~/.config/opencode/skills/`. Skills placed outside these discovery paths are never loaded, and the skill directory name must match the `name` in its SKILL.md frontmatter.

### The `hakase` Self-Skill

The repository ships a self-knowledge skill at `.agents/skills/hakase/SKILL.md` that documents the agent itself: identity, architecture, sub-agents, tools, configuration, skills system, knowledge base, sandbox/safety model, user home (`~/.hakase`), CLI commands, and troubleshooting. The agent loads it whenever the user asks about hakase itself ("who are you", "what can you do", "how do I configure/extend hakase"). Deeper reference material lives in `.agents/skills/hakase/references/` (architecture, configuration, skills, knowledge-base, troubleshooting). Being committed to the repository, it ships with the agent and is versioned with it; for binary-only installs it can be fetched into any discovery location (e.g. project `.agents/skills/` or user `~/.hakase/skills/`) from the GitHub repo, following the cross-tool `gh skill install` convention.

#### Interoperability

Skills authored to this format (e.g. from Claude Code, Codex CLI, Gemini CLI, or OpenCode - the agentskills.io spec) work in hakase by dropping them into `.agents/skills/`.

#### Research skills (ported from Hermes Agent)

The repository ships a research skill category ported from Hermes Agent's
`optional-skills/research/` (MIT), alongside the creative port: `domain-intel`
(passive recon: crt.sh subdomains, WHOIS, DNS-over-HTTPS, SSL, bulk - pure
stdlib), `osint-investigation` (follow-the-money public-records framework:
SEC EDGAR, USAspending, OFAC SDN, OpenCorporates, GDELT, Wayback, etc. - 16
stdlib scripts + 11 source references), `drug-discovery` (ChEMBL, PubChem,
OpenFDA, OpenTargets doctrine), `bioinformatics` (gateway to bioSkills +
ClawBio pipelines, cloned on demand), `scrapling` (HTTP/Dynamic/Stealthy
fetchers + spider, `pip install scrapling[all]` prerequisite), and
`darwinian-evolver` (documents the native skill-evolution layer above).
The porting manifest and conventions live in `.agents/skills/research/`.
Dropped upstream skills (duckduckgo-search, searxng-search, parallel-cli,
qmd, gitnexus-explorer) are documented there with rationale - generic web
search is covered by the `web_researcher` sub-agent, and qmd's retrieval
ideas were folded into the knowledge-search quality work (relevance ranking,
query expansion, `hakase knowledge bench`).

#### LaTeX / math skill (original)

`latex-math` (`.agents/skills/latex-math/`) is an original doctrine skill for
LaTeX typesetting and mathematical documents: mode classification
(document/snippet/beamer), a verbatim preamble catalog (`references/`),
notation conventions, a compile-verify-fix loop with a 40-entry error
playbook, quality checklists, no-fabrication rules, and `scripts/compile.sh`
(.tex -> PDF + transparent PNG via tectonic + poppler) + `scripts/lint.sh`
(structural and `.bib` lint). It pairs with the TUI's built-in math
rendering (see [Terminal TUI](#-terminal-tui)).

#### Coexistence with Python Skills

Python skills (`skills.json` + `.py` files) are unchanged. On a name collision, the markdown skill wins in the prompt and the Python entry is omitted with a logged warning (the `.py` file remains importable).

#### Operational Notes

- Skills added mid-session require a restart to be discovered.
- `.agents/skills/` is meant to be committed to the repository.

---

## TODO

### Creative skills - deferred items

The creative skills port (see `.omo/plans/creative-skills-port.md`) deliberately
deferred a subset of the Hermes creative skills that depend on capabilities
hakase did not have at the time. Native **MCP client support on the
orchestrator** (see [MCP Integration](#-mcp-integration)) is now implemented -
connect any MCP server (stdio or HTTP) via the `mcp` config block and manage it
from the TUI with `/mcp`. Still deferred for the remaining skills: an
**`image_gen` tool** and a **`video_gen` tool**:

- `touchdesigner-mcp` - requires a TouchDesigner MCP server; can now be added
  via the generic `mcp` config once a server endpoint is available.
- `baoyu-infographic` - currently adapted to HTML/SVG output; revisit to use a
  native `image_gen` tool when available.
- `comfyui` - ported as doctrine + gated on user infra (ComfyUI/comfy-cli,
  GPU or cloud); revisit to integrate with native image/video generation.
- `songwriting-and-ai-music` - doctrine ported; generation is external (Suno);
  revisit for native TTS/audio when relevant tools exist.

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
| `github.com/robfig/cron/v3`               | 5-field cron parsing for scheduled tasks |

---

## License

MIT
