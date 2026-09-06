# Development Guide

This guide collects the developer-facing and advanced reference material for hakase. The user-facing [README.md](../README.md) is the quick-start; this document is the deep dive.

- [Project Structure](#project-structure)
- [Build System & Tags](#build-system--tags)
- [Release Engineering](#release-engineering)
- [Release Pipeline (GitHub Actions)](#release-pipeline-github-actions)
- [Verifying Release Binaries](#verifying-release-binaries)
- [Web UI Development Flow](#web-ui-development-flow)
- [Development Platform](#development-platform)
- [Windows Notes](#windows-notes)
- [Testing](#testing)
- [Architecture & Agent Orchestration](#architecture--agent-orchestration)
- [TUI Deep Dive](#tui-deep-dive)
- [Knowledge Base](#knowledge-base)
- [Skills System](#skills-system)
- [Project Context Files (AGENTS.md)](#project-context-files-agentsmd)
- [Runtime Environment Awareness](#runtime-environment-awareness)
- [Sandboxing & Workspace Confinement](#sandboxing--workspace-confinement)
- [MCP Integration](#mcp-integration)
- [Media Generation](#media-generation)
- [Sidekick (Second Model)](#sidekick-second-model)
- [File Operations & System Execution](#file-operations--system-execution)
- [Configuration Reference](#configuration-reference)
- [Dependencies](#dependencies)
- [TODO -- Deferred Items](#todo----deferred-items)

---

## Project Structure

Standard Go layout - a single entry point under `cmd/`, all packages under `internal/`, and the web SPA under `webui/`:

```
hakase/
├── cmd/
│   └── hakase/                 # Entry point. main.go - TUI bootstrap, dependency wiring, web/serve
│                               #   interception before CLI dispatch; web.go - web/serve server
│                               #   bootstrap; slash_commands.go - /board and /mcp handlers
├── internal/
│   ├── agent/                  # Core agent logic: ADK runner setup, sub-agents, providers
│   │                           #   (Gemini / OpenAI / OpenAI-compatible + fallback), delegation,
│   │                           #   approval & clarify gates, loop guard, task registry,
│   │                           #   malformed tool-call repair, audit logging
│   ├── agentrun/               # Transport-neutral single-turn driver (run loop, project
│   │                           #   binding, tool-call repair, persistence) shared by web
│   │                           #   chat and channels
│   ├── auth/                   # Argon2id credential hashing + JWT issue/verify
│   ├── channel/                # Communication-channel subsystem: pairing/auth, per-chat run
│   │                           #   state, bridge event router, formatting; channel/state is
│   │                           #   the ~/.hakase/channels.json leaf store, channel/telegram
│   │                           #   the Telegram transport
│   ├── cli/                    # CLI dispatcher and subcommands (skill, task, knowledge, session,
│   │                           #   rules, env, cron, channels, auth) + the cronjob tool & scheduler
│   ├── config/                 # Config loader (reads config.json) + MCP server config & persistence
│   ├── context/                # Context management: instruction rendering, compaction cascade /
│   │                           #   summarization, token budgeting
│   ├── env/                    # Runtime environment detection (OS/distro/arch, package manager,
│   │                           #   toolchains)
│   ├── herdr/                  # Herdr terminal-multiplexer lifecycle reporter
│   ├── interfaces/             # Shared contracts (approval/clarify gates, notifiers, log funcs)
│   ├── knowledge/              # Knowledge base: note storage, [[wikilinks]], relevance-ranked
│   │                           #   search, the eight knowledge tools
│   ├── mcp/                    # MCP client: multi-server manager (stdio + streamable HTTP)
│   ├── sandbox/                # Workspace path confinement, bubblewrap (bwrap) subprocess
│   │                           #   isolation, system_exec, sandbox-aware file ops
│   ├── session/                # Session store & service (persistence, resumability, stale cleanup)
│   ├── skill/                  # Markdown & Python skill discovery/validation + the evolver
│   ├── tui/                    # Bubble Tea TUI: split panes, slash commands, math & markdown
│   │                           #   rendering, MCP panel, approval/clarify modals, clipboard
│   ├── util/                   # Structured JSON debug logging, queues, token utils
│   ├── vision/                 # Vision tool (image loading, vision-model routing)
│   └── web/                    # HTTP server (chi): API handlers (auth, chat SSE, sessions, tasks,
│                               #   files, knowledge, skills, MCP, cron, channels, config,
│                               #   approval, clarify), middleware (security headers, login rate
│                               #   limiter), SSE bridge, SPA embed (prod tag) / live disk
│                               #   serving (dev tag)
├── webui/                      # Web UI - Vue 3 + TypeScript + Vite + Tailwind 4 SPA (pnpm)
│   └── src/
│       ├── views/              # Chat, Sessions, Tasks, Knowledge, Skills, MCP, Cron, Channels,
│       │                       #   Files, Settings, Login
│       ├── components/         # Chat (message bubbles, markdown renderer, attachments, thinking
│       │                       #   blocks, image lightbox), approval & clarify modals, UI kit
│       ├── stores/             # Pinia stores (app, auth, session, task, approval, clarify, theme)
│       ├── composables/        # SSE hook, notifications, mermaid
│       ├── lib/                # API client, markdown pipeline, SSE client, utils
│       └── router/             # Vue Router with auth guard
├── docs/                       # Design/plan documents (e.g. markdown-rendering)
├── .agents/skills/             # Portable markdown skills (SKILL.md) shipped with the repo -
│                               #   includes the hakase self-skill
├── config.json.example         # Example config template (config.json itself is runtime, gitignored)
├── Makefile                    # build / build-frontend / dev-frontend / dev-backend / test / clean
├── go.mod / go.sum             # Go module dependencies (module amurru/hakase)
├── skills/                     # Persisted Python skill library (runtime)
├── knowledge/                  # Persistent knowledge base (runtime)
├── downloads/                  # Downloaded files (PDFs, images, datasets - runtime)
├── outputs/                    # Generated artifacts (HTML files, charts, reports - runtime)
└── .venv/                      # Python virtual environment (auto-created, runtime)
```

**Wiring gotchas:**

- `web`/`serve` are intercepted in `cmd/hakase/main.go` BEFORE `cli.Dispatch`. The `web`/`serve`/`tui` entries registered inside `internal/cli/command.go` are stubs (`notMigrated`/placeholder); the real TUI launches only when no subcommand is given.
- The web/serve bootstrap (`cmd/hakase/web.go`) must live in `package main`: `internal/web/handlers` imports `internal/cli`, so a shared bootstrap package would create an import cycle.
- `cmd/hakase/main.go` wires `agent.Deps` with bridge factories (MCP manager, skill discovery, knowledge tools, cron) to keep `internal/agent` decoupled from those packages; new agent-facing cross-package capabilities usually need a factory added there.

---

## Build System & Tags

Two build modes via Go build tags on `internal/web` (`internal/web/embed_prod.go` vs `embed_dev.go`):

- **`prod` (default, `!dev`)** - the built SPA is embedded into the binary via `//go:embed all:dist` from `internal/web/dist` (a real copy of `webui/dist`; go:embed cannot follow symlinks)
- **`dev`** - the SPA is served live from `webui/dist` on disk, so frontend changes are visible without rebuilding the binary

Critical setup gotcha: `internal/web/dist/` is gitignored but required at compile time by `//go:embed all:dist`. On a fresh clone, `go build ./...` and `go test ./...` fail for `internal/web` until you run `make build-frontend` (after that, the mirror exists and plain Go commands work). `make clean` removes it again.

### Makefile Targets

| Target | Description |
| ------ | ----------- |
| `make build` | Full production binary: frontend build + `go build -tags prod -o hakase ./cmd/hakase/` |
| `make release` | Same as `make build`, then echo the stamped version (run after tagging) |
| `make build-windows` | Cross-compile `hakase.exe` (windows/amd64, unsigned) and assemble `dist/hakase-<version>-windows-amd64.zip` with SHA256SUMS.txt |
| `make build-frontend` | `pnpm install && pnpm build` in `webui/`, then mirror `webui/dist/` into `internal/web/dist/` |
| `make dev` | Prints the two-terminal development mode instructions |
| `make dev-frontend` | Vite dev server with HMR on port 5173 |
| `make dev-backend` | `go run -tags dev ./cmd/hakase/ web` - serves `webui/dist` from disk, API on :8080 |
| `make test` | `go test ./...` |
| `make clean` | Remove `webui/dist`, `internal/web/dist`, and the `hakase` binary |

Frontend tests: `cd webui && pnpm test` (vitest, jsdom). `pnpm build` runs `vue-tsc -b` first, so the typecheck is part of the build.

---

## Release Engineering

Every `make build` stamps build metadata into the binary via `-ldflags` so `hakase version` can report it:

```bash
$ hakase version
hakase v0.1.0-alpha.1
  commit: 69e922d
  built:  2026-08-23T10:00:00Z
  go:     go1.26.5 (linux/amd64)

$ hakase version --short   # version string only, for scripts
v0.1.0-alpha.1
```

The version defaults to `git describe --tags --always --dirty` output (latest tag, e.g. `v0.1.0-alpha.1` or `v0.1.0-alpha.1-3-g69e922d`; `dev` outside a git repo) and can be overridden with `make VERSION=vX.Y.Z`. The commit and UTC build date are captured automatically. User-facing changes are tracked in [CHANGELOG.md](../CHANGELOG.md) (Keep a Changelog format, semver-ish during 0.x).

### Release Pipeline (GitHub Actions)

Pushing a release tag triggers [.github/workflows/release.yml](../.github/workflows/release.yml), which fully automates publishing:

1. **Build job** - runs `make build` on a GitHub-hosted runner (frontend + ldflags-stamped prod binary, so `hakase version` matches the tag exactly), packages the binary into `.deb` and `.rpm` via [nfpm](https://nfpm.goreleaser.com) (`make package-linux`, config in `packaging/nfpm.yaml`), creates the GitHub release with generated notes, and uploads `hakase-<tag>-linux-amd64`, the packages, and `SHA256SUMS.txt`. Prerelease versions are encoded with `~` (`0.1.0~alpha.2`) so dpkg/rpm sort them before the final release.
2. **Provenance job** - delegates to the [SLSA generic generator](https://github.com/slsa-framework/slsa-github-generator) reusable workflow, which keylessly signs SLSA Build L3 provenance (in-toto/DSSE via Sigstore) for the uploaded binaries and attaches `<binary>.intoto.jsonl` to the release.

Arch Linux users can also install the prebuilt binary from the AUR as [`hakase-bin`](../packaging/aur/hakase-bin/PKGBUILD).

Release flow: update `CHANGELOG.md`, commit, then `git tag -a vX.Y.Z && git push origin vX.Y.Z` - the pipeline handles the rest. Every third-party action in the workflow is pinned to a full commit SHA; the SLSA reusable workflow is pinned to its release tag (`@v2.1.0`) because that ref is what `slsa-verifier` validates against.

Windows: the release pipeline also runs a cross-compile job (unsigned `hakase-<tag>-windows-amd64.zip` uploaded as a workflow artifact) and a `windows-latest` job that runs the test suite plus a `hakase.exe` boot smoke. Neither gates the Linux release; Authenticode signing for the Windows binary is a designated follow-up.

### Verifying Release Binaries

Release assets ship with SLSA L3 provenance. Verify a downloaded binary with [slsa-verifier](https://github.com/slsa-framework/slsa-verifier):

```bash
# provenance is attached to the release as hakase-<tag>-linux-amd64.intoto.jsonl
slsa-verifier verify-artifact hakase-v0.1.0-alpha.2-linux-amd64 \
  --provenance-path hakase-v0.1.0-alpha.2-linux-amd64.intoto.jsonl \
  --source-uri github.com/amurru/hakase \
  --source-tag v0.1.0-alpha.2

# and cross-check the sha256 in SHA256SUMS.txt
sha256sum -c SHA256SUMS.txt --ignore-missing
```

A passing verification proves the binary was built by the release workflow from the tagged source of this repository and has not been tampered with since. As a final check, `hakase version` should report the same version and commit as the release tag it was downloaded from.

---

## Web UI Development Flow

Run two processes; Vite proxies `/api` to the Go server on :8080, so open <http://localhost:5173> in the browser:

```
make dev-frontend   # terminal 1 - Vite dev server, HMR, port 5173
make dev-backend    # terminal 2 - Go server with the dev tag, port 8080
```

---

## Development Platform

hakase is developed and tested on **Linux** (primary platform) and builds natively on **Windows** (`make build-windows` - no WSL2 required). On Windows the `system_exec` toolset routes shell commands through `cmd /D /C` and the sandbox runs in `paths` mode (`bubblewrap`/`landlock` are Linux-only and coerce with a warning).

## Windows Notes

- **Shell semantics** - string commands run via `cmd /D /C` (`/D` suppresses per-user AutoRun registry scripts). POSIX-only constructs - globs, `$()`, backticks, `VAR=x cmd` - are NOT interpreted; use cmd syntax (`%VAR%`, `&&`, `|`, `>`). The `system_exec` tool description carries the same note so the model adapts.
- **Executable resolution** - bare executable names resolve from PATH only, never the working directory: hakase sets `NoDefaultCurrentDirectoryInExePath=1` process-wide, rewrites bare names to absolute PATH paths before exec, and refuses to execute a file planted in the workspace under a bare command name.
- **Python** - install Python from [python.org](https://www.python.org/downloads/) so the `py` launcher (or `python`) is on PATH; the code interpreter probes `py -3` then `python` and creates the venv under `.venv\Scripts\`.
- **Sandbox** - `bubblewrap` and `landlock` modes are Linux-only; on Windows they coerce to `paths` mode with a warning (audit entries record the effective mode).
- **Unsigned binary** - v1 Windows builds are not code-signed; SmartScreen and antivirus heuristics may flag `hakase.exe`. Verify the sha256 in `SHA256SUMS.txt` and, once installed, the binary behaves like any local tool.
- **Browser MCP** - the browsing stack is a config swap; on Windows use the [presets](browser-mcp-presets.md) with Lightpanda (headless) or `chrome-devtools-mcp` on Edge.
- **Known differences (v1)** - TUI image paste is unsupported on Windows (text paste works; `readImageFromClipboard` probes only the Wayland/X11 tools), and the web server shuts down via ctrl-c only (SIGTERM is not externally deliverable on Windows).

---

## Testing

- Go tests live next to sources (`*_test.go`), are self-contained (temp dirs, `isolateHome` redirects `$HOME`/`XDG_CONFIG_HOME`), and need no network, config.json, or MCP servers.
- Tests write `logs/exec-audit.jsonl` under `cmd/hakase/` and `internal/agent/`; these `logs/` dirs are runtime artifacts, gitignored via `logs/` - do not commit them.
- Runtime/generated (all gitignored): `config.json`, `tasks.json`, `sessions/`, `outputs/`, `downloads/`, `.venv/`, `.hakase-tmp/`, `webui/dist/`, `internal/web/dist/`, root `hakase` binary.
- Single Go test: `go test ./internal/agent/ -run TestName`
- Frontend tests: `cd webui && pnpm test` (vitest, jsdom). Single file: `cd webui && pnpm vitest run src/lib/markdown/useMermaid.test.ts`
- `pnpm build` runs `vue-tsc -b` first, so the typecheck is part of the build

---

## Architecture & Agent Orchestration

Powered by [Google ADK](https://github.com/google/adk) (`google.golang.org/adk/v2`):

| Agent | Role |
| ----- | ---- |
| **orchestrator** | Root agent that delegates tasks to sub-agents based on intent |
| **web_researcher** | Searches the web, navigates pages, downloads files, extracts content |
| **code_interpreter** | Executes Python, performs data analysis, manages the skill library |
| **general_purpose** | Reads, writes, edits, and searches files in the workspace |

The orchestrator delegates via `delegate_task` or `transfer_to_agent`. Sub-agents include loop guard, task registry, malformed tool-call repair, and audit logging. Providers are Gemini / OpenAI / OpenAI-compatible + fallback.

---

## TUI Deep Dive

A split-pane terminal interface built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [Lip Gloss](https://github.com/charmbracelet/lipgloss):

- **Left panel** -- Chat viewport displaying agent responses and tool call logs
- **Right panel** -- Real-time status and execution logs
- **Bottom** -- Multi-line text input (auto-grows up to 3 lines) with focus cycling (`Tab` to switch panes) and a hint bar showing the most used shortcuts
- **Mid-run messaging** -- Type and send while the agent is working: your message is queued (shown as `N queued` in the hint bar), steered into the running session at the next model-call boundary as a `USER INTERJECTION`, then processed as its own turn when the current run completes
- **Mid-run questions** -- The agent can pause and ask you a question mid-task via the `clarify` tool; choose from up to 4 options or type a free-text answer, [esc] to dismiss
- **Help overlay** -- Press `Ctrl+/` (`?` when not typing) for a full keyboard shortcut reference
- **Herdr awareness** - when launched inside a [Herdr](https://github.com/amurru/herdr) pane, hakase reports its lifecycle (start/finish) and releases authority on exit so the multiplexer stops tracking it
- **Inline math rendering** -- LaTeX math in agent responses renders natively in the chat pane: display math (`$$...$$`) compiles to a transparent PNG via tectonic + poppler and displays through the kitty graphics protocol in kitty/WezTerm/ghostty terminals; everywhere else it degrades to a Unicode character grid (stacked fractions, `∑`/`∫` limits, matrix delimiters) via the pure-Go termtex parser. Inline math (`$...$`) always uses Unicode. Streaming shows Unicode math that upgrades to images when the message completes; no terminal or toolchain is required for the fallback to work.

### Keyboard Shortcuts

| Shortcut | Action |
| -------- | ------ |
| `Ctrl+C` | Quit the application (also cancels a running agent) |
| `Esc` `Esc` | Interrupt the running agent (double-press within 2s) |
| `Esc` | Close the help overlay (never quits) |
| `Ctrl+/` or `?` | Toggle the help overlay |
| `Tab` / `Shift+Tab` | Cycle focus: input -> chat -> log -> task |
| `Ctrl+T` | Toggle the thinking display |
| `Enter` | Send the message (queued while the agent is busy) |
| `Shift+Enter` / `Ctrl+J` | Insert a newline in the input |
| `↑`/`k`, `↓`/`j` | Scroll the focused pane (older/newer content) |
| `PgUp`/`b`, `PgDn`/`f` | Page up / down in the focused pane |
| `u` / `d` | Half page up / down in the focused pane |
| `Home`/`g`, `End`/`G` | Jump to top / bottom of the focused pane |
| `Ctrl+A` / `Ctrl+E` | Jump to line start / end in the input |
| `Ctrl+U` | Clear the input |

Mouse wheel scrolling works on whichever pane is focused. The log pane stays pinned to the bottom unless you scroll up to read history.

### Slash Commands

Type `/` in the input to see a filtered command menu (arrow keys navigate, `Tab` completes, `Enter` runs).

| Command | Action |
| ------- | ------ |
| `/board` | Task board: `summary`, `list`, `new <title>`, `get <id>`, `update <id>`, `done <id>`, `fail <id>`, `cancel <id>`, `delete <id>`, `archive <id>`, `claim <id>` |
| `/mcp` | Manage MCP servers: open the interactive server panel, or `list` / `enable <name>` / `disable <name>` / `reconnect <name>` |
| `/compact [focus]` | Summarize the conversation to free context, continuing the same session; optional focus instructions steer the summary |
| `/new` | Start a fresh session (previous sessions stay resumable) |
| `/sessions` | Open the session chooser to switch or resume old sessions |
| `/help` | Show the keyboard shortcut and slash command reference |
| `/exit` / `/quit` | Exit hakase |

`/compact` runs the deterministic history snip immediately (keeps the last two turns) and schedules an async LLM summary - the same compaction cascade used by automatic context management (`summary_model`), exposed manually.

**Slash commands in the web UI too:** typing `/` in the web chat input opens an autocomplete palette (arrow keys navigate, Tab/Enter complete) with the same commands except `/exit`, which is terminal-only. `/compact [focus]` calls `POST /api/sessions/{id}/compact` on the server; `/sidekick <question>` asks the sidekick model; `/new`, `/sessions`, `/board`, and `/mcp` open or switch to their SPA views; `/help` lists everything inline. Unknown `/tokens` are sent to the agent as ordinary text.

### File Attachments

Attach files and images to a message without the agent having to find them:

- **`@file`** - type `@` to open a workspace file picker; arrow keys navigate, `Enter` attaches the highlighted file as a chip (`@name.go`). Text files embed their content; images embed as multimodal input.
- **Image paste** - copy an image (screenshot) and press `Ctrl+V`; it is read from the clipboard and attached as a `[image 1]` chip. Text pastes still work normally.
- Chips render in a row above the input; `Backspace` on an empty input removes the last chip. Attachments are sent alongside the prompt text and are persisted with the session (re-attached on resume).

Sandbox note: `@` paths resolve through the sandbox read roots - files outside the approved workspace are rejected with a hint.

---

## Knowledge Base

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

Search results are relevance-ranked (BM25-style: title/alias/tag matches outrank summary/metadata/body, with an alphabetical tiebreak) while keeping the exact same result set as the old substring search. Optional HyDE-lite LLM query expansion is available via the `search_expansion` config field (default off).

#### The `hakase cron` CLI

The `hakase cron` command manages scheduled tasks:

- `hakase cron list|status|pause <id>|resume <id>|run <id>|tick` - list all jobs, show the registry path and state counts, pause/resume a job by ID or name, trigger a job immediately, or run all due jobs once

The in-process scheduler runs while the TUI is open (a 30-second tick fires due jobs headless); `hakase cron tick` runs all due jobs once from the CLI. `run` and `tick` bootstrap the model for headless execution; the other subcommands are pure file operations.

#### The `hakase channels` CLI

The `hakase channels` command manages communication channels (see [Channels configuration](#channels-configuration)); it touches only the state file, so it works while the web server is running:

- `hakase channels status` - paired users, chat bindings, pending pairing code state
- `hakase channels pair-code` - print (generating if needed) a pairing code
- `hakase channels revoke <user-id>` - unpair a Telegram user

---

## Skills System

### Python Code Interpreter

- Runs Python code in an isolated `.venv` virtual environment
- **Auto-resolves missing dependencies** -- detects `ModuleNotFoundError`, installs the package via pip, and retries
- Sets `PYTHONPATH` to include `./skills` so persisted skills are importable
- **Sandbox-aware** -- when the sandbox is active, the script temp dir and working directory are pinned to the workspace root (`.hakase-tmp/`) so script writes stay inside the approved workspace
- **Process hardening** -- the interpreter runs in its own process group with a parent-death signal, so children (and grandchildren) are reaped if the agent crashes

### Self-Evolving Skill Library

The agent can save tested Python scripts as reusable skills:

1. Code is executed and verified via `python_interpreter`
2. The agent calls `save_skill` to persist the script to `./skills/`
3. Skills are registered in `skills/skills.json` with name, description, and import usage
4. On subsequent runs, the agent loads all saved skills and can reuse them via `from skills.<name> import ...`

#### Skill evolution (darwinian-evolver style)

hakase ships a native, cron-driven evolution loop over the Python skill library (`evolver.go`), inspired by the darwinian-evolver contract (no AGPL import - the upstream `imbue-ai/darwinian_evolver` is never wrapped):

- **Evaluator** - each skill's `skills/<name>.eval.json` defines input / expected cases (trainable + holdout split). Skills without an eval set, or whose module fails to load, are skipped.
- **Mutator** - failing skills are fed to the configured model (current source + failure cases) which proposes a fixed implementation.
- **A/B gate** - a mutation is promoted only when it beats the incumbent by >=5% on the trainable score with zero holdout regressions. The incumbent is preserved as `<name>.py.bak`. Skills with an eval hit rate below 30% are auto-deprecated in `skills/skills.json`.
- **Driver** - run `hakase skill evolve [--mutate]` for a manual pass, or schedule the nightly pass as a native cron job (`native: "evolve"` via the `cronjob` tool, or `hakase cron`). Every pass writes an auditable report to `outputs/cron/evolve-*.md` for human review. No live self-modification.

The `darwinian-evolver` markdown skill (`.agents/skills/darwinian-evolver/`) documents the loop, the eval-set format, and the cron wiring.

#### Reflexion (lessons learned)

The orchestrator writes "lessons learned" knowledge notes after failed or complex tasks (tagged `lessons-learned`) and recalls them at session start, so hard-won solutions and dead-ends are not re-learned.

### Markdown Skills

hakase supports markdown-based skills in addition to Python skills. Each skill is a directory containing a `SKILL.md` file with YAML frontmatter and a progressive-disclosure body.

#### The `hakase skill` CLI

- `hakase skill create <name> [--dir <path>] [--description <text>] [--template python] [--force]` - Scaffolds `<dir>/<name>/SKILL.md` with valid frontmatter (`name`, `description`, `license: MIT`, `metadata: author/version`) plus `scripts/` and `references/` subdirectories. The `<name>` must match `^[a-z0-9]+(-[a-z0-9]+)*$`. The default directory is the git project root's `.agents/skills/`.
- `hakase skill list` - Prints discovered skills (Python from `./skills/skills.json` plus markdown from project and user directories) with source paths.
- `hakase skill validate <dir>` - Parses and validates a single skill; exits non-zero on failure (CI-friendly).
- `hakase skill evolve [--dir ./skills] [--mutate] [--report <path>]` - Runs one darwinian-evolver-style skill-evolution pass over the Python skill library. Default is evaluation-only; `--mutate` enables the mutator step (requires a configured model).

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

#### The `hakase` Self-Skill

The repository ships a self-knowledge skill at `.agents/skills/hakase/SKILL.md` that documents the agent itself: identity, architecture, sub-agents, tools, configuration, skills system, knowledge base, sandbox/safety model, user home (`~/.hakase`), CLI commands, and troubleshooting. Being committed to the repository, it ships with the agent and is versioned with it.

#### Interoperability

Skills authored to this format (e.g. from Claude Code, Codex CLI, Gemini CLI, or OpenCode) work in hakase by dropping them into `.agents/skills/`.

#### Research skills (ported from Hermes Agent)

The repository ships a research skill category ported from Hermes Agent's `optional-skills/research/` (MIT): `domain-intel`, `osint-investigation`, `drug-discovery`, `bioinformatics`, `scrapling`, and `darwinian-evolver`. See `.agents/skills/research/` for the porting manifest. Generic web search is covered by the `web_researcher` sub-agent; qmd's retrieval ideas were folded into the knowledge-search quality work.

#### LaTeX / math skill

`latex-math` (`.agents/skills/latex-math/`) is an original doctrine skill for LaTeX typesetting: mode classification (document/snippet/beamer), a verbatim preamble catalog (`references/`), notation conventions, a compile-verify-fix loop with a 40-entry error playbook, quality checklists, and `scripts/compile.sh` (.tex -> PDF + transparent PNG via tectonic + poppler) + `scripts/lint.sh`.

#### Coexistence with Python Skills

Python skills (`skills.json` + `.py` files) are unchanged. On a name collision, the markdown skill wins in the prompt and the Python entry is omitted with a logged warning (the `.py` file remains importable).

---

## Project Context Files (AGENTS.md)

hakase loads project context files - `AGENTS.md` - into every agent's system instruction, so repository conventions, architecture notes, and coding rules are followed without being repeated in every prompt.

- **Project scope** - `AGENTS.md` files are collected from the current directory up to the git root (closest first; nested files stack). Only when no `AGENTS.md` exists anywhere in the walk is a **project-scoped** `CLAUDE.md` used as a fallback.
- **User scope** - `~/.hakase/AGENTS.md` (or `$HAKASE_HOME/AGENTS.md`) when present. The Claude Code global `~/.claude/CLAUDE.md` is deliberately **never** loaded.
- **Custom files** - `instruction_files` in `config.json` adds more context: absolute paths, `~/`-prefixed paths, project-relative paths, or `http(s)://` URLs (fetched at startup with a short timeout; failures are skipped, never fatal).

Each loaded file is rendered as `Instructions from: <path>` followed by its content under a `### PROJECT CONTEXT FILES:` header. Content is **prompt-injection scanned** and **truncated per file** (Hermes-style 70% head / 20% tail split, default 20,000 characters, configurable via `context_files.max_chars`). The rendered block is accounted for in the context-compaction token budget.

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

Beyond the startup block, reading a file (`read_file`) or searching a directory (`search_files`) below the workspace root attaches any `AGENTS.md` in that directory tree - not already in the system prompt - to the tool result under a `SUBDIRECTORY CONTEXT` header. Each file is attached **once per session**, injection-scanned, and capped at 8,000 characters per file.

#### Live reconcile

If a loaded context file changes mid-session, hakase detects it (cheap path/size/mtime fingerprint, checked before every model call) and injects a one-shot `PROJECT CONTEXT UPDATE` notice so the model follows the updated instructions.

#### The `hakase rules` CLI

```bash
hakase rules list    # list the context files that would be loaded (render order + scope)
hakase rules show AGENTS.md   # show one file's content (path or basename)
```

---

## Runtime Environment Awareness

At session start hakase detects the environment it runs on and injects a compact block into every agent's system instruction:

- **OS / architecture / distro** - e.g. `linux/amd64 (Arch Linux)`, plus the kernel release on Linux
- **Package manager** - resolved by PATH availability first (handles hybrids like Nix-on-Ubuntu), with a distro-ID map fallback (`pacman`, `apt`, `dnf`, `zypper`, `apk`, `brew`, ...)
- **Shell, locale, timezone** - `$SHELL`, `$LC_ALL`/`$LANG`, and the current zone + UTC offset
- **User, home, workspace** - identity and the workspace root
- **Disk & memory** - free space on the workspace filesystem and total memory
- **Toolchains** - available compilers/interpreters/VCS with their versions (`go`, `gcc`, `clang`, `rustc`, `python3`, `node`, `git`, `docker`), probed with a 1s timeout

The block is labeled a startup snapshot; the model is directed to use `system_exec` for live system state. Two safeguards keep the snapshot honest:

- **Staleness refresh** - if disk free or available memory drifts materially (>= 1 GiB or >= 5%) since the startup snapshot, hakase injects a one-shot `ENVIRONMENT UPDATE` notice into the running session
- **Sandbox note** - when the sandbox is `bubblewrap`, the block carries a note that host-detected toolchains may be unreachable inside the sandbox

The `hakase env` CLI prints the exact block without running the agent: `hakase env`

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
- `max_chars` - caps the rendered block (0 uses the 800-char default).
- `apply_to` - restricts which agents receive the block; empty means all four agents.

Linux is the primary platform: on other OSes the portable fields are still reported and the Linux-specific ones degrade gracefully.

### Preferred Measurement Units

hakase can report physical quantities in your preferred measurement system. Set `units.system` in `config.json` (or in the web UI's **Settings -> Preferred Measurement Units**):

```json
{
  "units": { "system": "metric" }
}
```

- `metric` (default) -- meters/km, kilograms, liters, degrees Celsius, km/h, m²/ha (SI / ISO).
- `imperial` -- miles, pounds, US gallons, degrees Fahrenheit, mph, sq ft/acres.

When unset, metric (SI / ISO) is used. The preference is injected as a system reminder into every agent's instruction.

---

## Sandboxing & Workspace Confinement

hakase confines subprocesses and file operations to approved workspaces out of the box. The `sandbox` block in `config.json` selects a strategy:

| Mode | Description |
| ---- | ----------- |
| `paths` (default) | Pure path confinement -- all file ops (`read_file`/`write_file`/`patch`/`search_files`), downloads, and the Python interpreter resolve paths against approved read/work/deny roots. `system_exec` commands are audited so absolute path arguments must stay under the read roots or trusted system dirs. Symlink escapes are prevented via `securejoin` + `EvalSymlinks` re-verification. |
| `bubblewrap` | Adds kernel-level subprocess isolation -- `system_exec` and Python runs are wrapped in [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`) with separate PID/IPC/UTS/user namespaces, dropped capabilities, minimal filesystems, read-only system dirs, and optional network unshare. |
| `landlock` | Reserved for future in-process Landlock + seccomp confinement (Phase 3). |
| `off` | Explicitly disables confinement (opt-in only). |

Key properties:

- **On by default** -- an absent or unset `sandbox` block yields `paths` mode, so the agent cannot write outside approved workspaces without explicit configuration
- **Roots** -- `workspace_roots` (writable, default `["."]`), `read_roots` (readable, default = workspace roots), and `deny_roots` (always rejected, highest precedence); all are symlink-evaluated and de-duplicated
- **Secret files** -- hakase's own secret-bearing files are implicitly denied on top of any configured roots, wherever hakase is launched from: `config.json` and `.env` in the working directory, plus `config.json`, `mcp.json`, `credentials.json`, `jwt-secret`, and `cronjobs.json` under the hakase home (`~/.hakase` or `$HAKASE_HOME`). They cannot be read or written through any sandboxed tool or web file API and are hidden from directory listings; edit config via the web Settings view or a text editor. Deliberately *not* denied: `~/.hakase/AGENTS.md`, `~/.hakase/skills/`, and a user-global knowledge base
- **Downloads** -- the filename is basename-sanitized and the output path is confined to the workspace
- **Current sandbox** -- if `bwrap` is not installed, bubblewrap mode falls back to the safe path-confinement exec path and logs a warning

---

## MCP Integration

hakase is an MCP (Model Context Protocol) **client**. Any number of stdio or streamable-HTTP MCP servers can be configured, and their tools are exposed to the orchestrator (and delegated sub-agents) dynamically as `mcp_<server>_<tool>` tools. Manage servers at runtime from the TUI with `/mcp`.

Configuration lives in the `mcp` block of `config.json` (project scope, never written by the app) merged with the user registry at `~/.hakase/mcp.json` (written by the TUI):

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
- `command` + `env`: the stdio child process. `HAKASE_*`/`AWS_*`/`GITHUB_*`/`OPENAI_*` are scrubbed from its environment; the server's own `env` block is applied on top, with `${VAR}` / `${VAR:-default}` expansion (also applied to `url`, `headers`, and command args).
- `url` + `headers`: a streamable-HTTP endpoint. `headers` values are env-expanded, so keep tokens out of committed config.
- `disabled: true`: explicit opt-out (new servers default to enabled).
- `tools.include` / `tools.exclude`: per-server allow/deny lists on the namespaced tool names (`mcp_<server>_<tool>`); exclude wins.
- `timeout_ms` and `oauth` are reserved for the auth phase (remote OAuth).

The legacy single-server `mcp_server_url` field still works and is auto-migrated to a server named `lightpanda`.

### Browser MCP presets

Any spec-compliant browser MCP is a config-only swap, on Linux and Windows alike. [browser-mcp-presets.md](browser-mcp-presets.md) ships four copy-pasteable presets with `web_researcher` tool shaping:

| Preset | Runtime tradeoff |
| --- | --- |
| **Lightpanda** (default when available) | dedicated headless engine - lightest, fastest |
| `chrome-devtools-mcp` | controlled real browser (Edge on Windows, Chrome elsewhere) |
| `@playwright/mcp` | real browser, headless or headed, deterministic selectors |
| `@browsermcp/mcp` | your live signed-in browser session |

---

## Media Generation

Hakase ships pluggable `generate_image`, `generate_video` (and stub `generate_audio`) as first-class ADK tools:

- **Offline fallback (`pil`)** - pure Go rendering via `image/draw` + embedded Go font. Works with zero config, zero network, zero pip. Not photorealistic but deterministic for posters/diagrams/infographics (baoyu-infographic now prefers `generate_image` when available).
- **Cloud quality when keys present** - `openai` (OpenAI Images or any OpenAI-compatible endpoint including OpenRouter via `openai_image_base_url` + `openai_image_path` override) and `fal.ai` (image via `fal-ai/flux/schnell`). Video generation runs through the async jobs API of the same router: OpenRouter `/api/v1/videos` (default model `google/veo-3.1-lite`, ~$0.03/s at 720p with audio off) with first-frame **image-to-video** support (`generate_video` accepts an image path/URL to anchor the clip); fal video (`fal-ai/wan/v2.7/text-to-video`) remains text-to-video only. `auto` order is `openai, fal, pil`; `pil` guarantee means zero-config always succeeds for images.
- **Sandboxed & observable** - all output goes through `outputs/media/<ulid>.<ext>` via `securejoin` + atomic write + `io.CopyN` caps (20MB image / 100MB video), rendered inline in chat via `mediaLinks` + `/api/files/inline` (same-origin, no CSP change). Manifest appends to `outputs/media/manifest.jsonl`; status via `GET /api/media/status` (never leaks keys).

Configure via the `media` block in `config.json` (all fields optional, defaults shown in `config.json.example`) or env vars `HAKASE_MEDIA_IMAGE_PROVIDER`, `HAKASE_MEDIA_VIDEO_PROVIDER`, `HAKASE_MEDIA_VIDEO_MODEL`, `HAKASE_MEDIA_OUTPUT_DIR`, `HAKASE_FAL_KEY` (house convention is `HAKASE_*` only - `OPENAI_API_KEY`/`FAL_KEY` are not read). See [media-generation/support.md](media-generation/support.md) for the full matrix and troubleshooting.

---

## Sidekick (Second Model)

The sidekick is a second, independently-configured LLM that runs alongside the main orchestrator. It can answer a direct question (`/sidekick <question>` in the TUI or typed in the web chat) or quietly watch a run and surface advisory notes as quiet inline chips. Every explicit interaction is recorded in `sessions/<id>.json` under `"kind": "sidekick"` - the question as a tagged user turn, the answer with `"role": "sidekick"` - so the session log itself shows which model produced what. It reads the conversation only - it never runs tools or touches the sandbox or workspace.

Modes (`sidekick.mode`):

- `off` - disabled (default).
- `on_demand` - the sidekick answers direct questions only; no watchdog.
- `watch` - the sidekick reviews the current run on a debounce and emits advisory notes; no side-process tool.
- `full` - both on-demand and watchdog behavior.

Enabling `sidekick` without a `mode` defaults to `on_demand`. Advisory notes are quiet inline chips only - there are no toasts, sounds, or notification pings for any severity. On-demand asks are **grounded in the conversation**: the question is framed with a tail-biased transcript of the session's chat turns (bounded by `transcript_window_chars`, default 6000), so follow-ups like "what's your take?" refer to what was actually discussed. Tool call/result transcripts are excluded from that window to keep prompts bounded; the watchdog consult uses the same budget.

**Cost warning:** the sidekick is a separate model, billed like any other request. Watch and full modes add evaluations per run, so a chatty watchdog has a real token cost. Tune `max_evaluations_per_run`, `max_notes_per_turn`, and `evaluate_debounce_seconds` to bound it.

**Privacy:** sidekick requests send conversation excerpts to the configured endpoint. On-demand asks include chat turns only (tool call/result transcripts are excluded); watchdog mode sends the full run transcript, which may include tool output. Point it at a local model via `openai-compatible` (for example Ollama at `http://localhost:11434/v1`) so nothing leaves your machine.

See [sidekick-agent/](../docs/sidekick-agent/) for the design doc and the full config block in [Configuration Reference](#configuration-reference).

---

## File Operations & System Execution

### File Operations

The `general_purpose` agent provides workspace file tools:

- **`read_file`** -- read file contents, optionally restricted to a line range (`offset`/`limit`)
- **`write_file`** -- create new files (or overwrite existing ones with `overwrite=true`)
- **`patch`** -- targeted string replacement inside an existing file
- **`search_files`** -- recursive regex search over file contents with `content` / `files_with_matches` / `count` output modes

Search is hardened against pathological trees: `head_limit` defaults to `100` when unset, the walk visits at most `50,000` entries, and a per-call `30s` deadline gracefully returns partial matches (marked `truncated`) instead of hanging. When the sandbox is active, all four tools resolve paths through workspace confinement (reads confined to read roots, writes to workspace roots).

### File Download

- Downloads files from any HTTP/HTTPS URL
- Saves to `./downloads/` with automatic filename resolution
- Supports PDFs, images, datasets, and binary blobs
- **Filename sanitized** -- a supplied filename is stripped to its base component (`filepath.Base`) so `../` traversal attempts are neutralized
- **Sandbox-aware** -- when the sandbox is active, the download target is resolved through workspace confinement and rejected if it would land outside the approved workspace

### Vision

The `vision` tool loads an image (URL, local file path, or `data:` URL) so the model can see it. When the main model is vision-capable, the image is attached directly to the model context; otherwise a configured `vision_model` describes the image as text. Images are SSRF-guarded, size-capped, and auto-converted or resized to fit provider limits. Configure via `vision_model`, `vision_provider`, `vision_base_url`, `vision_api_key`, and `model_vision`.

Attached images (`@file` or pasted screenshots) are handled the same way: on a non-vision main model they are described by `vision_model` before reaching the model (required on OpenAI-compatible providers, whose adapter rejects raw image parts); on a vision-capable model they pass through as inline input.

### System Command Execution

A `system_exec` toolset runs shell commands, scripts, and executables directly on the host machine, with several safety guarantees:

- **Shell routing** -- when no `args` are provided the whole command line is passed to the platform shell (`sh -c` on Unix, `cmd /D /C` on Windows), so pipes, redirects, and compound commands work naturally; explicit `(command, args...)` calls keep full control
- **Process hardening** -- spawned processes are placed in their own process group with a parent-death signal, so they and their children are reaped if the agent dies
- **Path confinement (all sandbox modes)** -- when the sandbox is active, absolute path arguments in the command line are audited against the sandbox read roots and trusted system dirs (`/usr`, `/lib`, `/bin`, `/etc`, `/proc`, `/dev`, `/sys`, `/tmp`, `/run`); anything else is rejected with an actionable error. This stops whole-filesystem scans like `find / -type d -name skills` from escaping the workspace. Add directories to `sandbox.read_roots` in `config.json` to permit them.
- **Default timeout** -- synchronous `system_exec` kills the command after 120s when `timeout_seconds` is omitted, so a hung command can never block the agent indefinitely. Long-running work should use `system_exec_start` (background) or an explicit `timeout_seconds`.
- **Sandbox integration** -- under a `bubblewrap` sandbox the command is wrapped in `bwrap` with filesystem + network isolation; sensitive env vars (`HAKASE_*`, `AWS_*`, `GITHUB_*`, `OPENAI_*`) are scrubbed so they never leak into sandboxed subprocesses; the working directory is pinned to the workspace root

---

## Configuration Reference

Edit `config.json` (see `config.json.example`):

```json
{
  "provider": "gemini",
  "model_name": "gemini-3.7-flash",
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

hakase supports multiple LLM providers, selected via the `provider` field in `config.json`. An empty or missing `provider` value defaults to `gemini`.

| Provider | Description | Default Model |
| -------- | ----------- | ------------- |
| `gemini` | Google Gemini | `gemini-3.7-flash` |
| `openai` | OpenAI API | `gpt-5.6-terra` |
| `openai-compatible` | OpenAI-compatible endpoints (Ollama, vLLM, etc.) | none - `model_name` required |

When `model_name` is empty, the provider's default model is used. `openai-compatible` endpoints have no universal default.

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

### Provider configuration fields

- `base_url` -- Base URL for OpenAI-compatible endpoints (e.g. `http://localhost:11434/v1` for Ollama). Ignored when empty; used only by the `openai` / `openai-compatible` providers.
- `fallback_providers` -- Optional ordered list of provider names to try if the primary provider fails (e.g. `["openai"]`). Empty by default.
- `provider_options` -- Optional map of provider-specific settings. Reserved for future use.
- `instruction` - Optional, additional customization rendered into the agent instructions as a `USER CONFIG INSTRUCTION` section (alongside the discovered `AGENTS.md` context). It is not a replacement for the built-in system prompts - it only adds.
- `instruction_files` - Optional list of extra context files merged into the project context (see [Project Context Files](#project-context-files-agentsmd)): absolute paths, `~/`-prefixed paths, project-relative paths, or `http(s)://` URLs.
- `context_files` - Optional tuning for the project context files: `max_chars` (per-file truncation cap, default `20000`) and `apply_to` (restrict which agents receive the block; empty = all).
- `system_env` - Optional tuning for the runtime-environment block (see [Runtime Environment Awareness](#runtime-environment-awareness)): `enabled` (default `true`), `max_chars` (block cap, default `800`), and `apply_to` (restrict which agents receive the block; empty = all).
- `mcp_server_url` - Legacy single MCP server URL (Lightpanda browser automation). Auto-migrated to a server named `lightpanda` in the `mcp` block.
- `mcp` - Optional MCP server configuration (see [MCP Integration](#mcp-integration)): `servers` map of name -> `{type, command, env, url, headers, disabled, tools, timeout_ms, oauth}`.
- `knowledge_dir` - Directory for the persistent knowledge base (default `./knowledge`; a leading `~` expands to the user home, e.g. `~/.hakase/knowledge` for a user-global base).
- `search_expansion` - Optional HyDE-lite LLM query expansion for `search_knowledge` (default `false`). When off, search behavior is byte-identical to plain substring search (just relevance-ordered). When on, the summarization model rephrases the query into 2-3 phrasings which are OR-matched and fused with Reciprocal Rank Fusion; on failure or timeout it falls back silently to plain substring search. Set `HAKASE_SEARCH_EXPANSION` to override via environment.
- `summary_model` -- Optional cheaper/weaker model used for context-compaction summarization (e.g. `gemini-3.5-flash-lite`). When empty, the primary model handles summaries. Set `HAKASE_SUMMARY_MODEL` to override via environment.
- `vision_model` - Optional multimodal model used to describe images as text when the main model lacks vision (legacy mode). Empty = disabled.
- `vision_base_url` - Optional separate endpoint for the vision model; empty = primary `base_url`.
- `vision_api_key` - Optional separate key for the vision model; empty = primary `api_key`.
- `vision_provider` - Optional provider for the vision model: `gemini`, `openai`, or `openai-compatible`. Empty = primary provider (a `vision_base_url` alone still forces an OpenAI-compatible endpoint). Use this when the vision model lives on a different backend than the main model - e.g. a Gemini vision model while the primary provider is OpenAI-compatible.
- `model_vision` - Override multimodal detection for the main model: `auto` | `yes` | `no` (default `auto`).
- `sandbox` -- Optional confinement block (see [Sandboxing & Workspace Confinement](#sandboxing--workspace-confinement)). Absent -> `paths` mode. Fields: `mode` (`paths` | `bubblewrap` | `landlock` | `off`), `workspace_roots`, `read_roots`, `deny_roots`, `allow_network`, `allow_pip_install`, `permissions`.
- `loop_guard` -- Optional anti-degeneration guardrails that abort a run stuck in a repetition loop or text-only bloat instead of burning the whole context/output window. Zero values use the defaults. Fields: `max_output_tokens` (cap on provider `maxOutputTokens`, default `8192`), `repetition_limit` (abort after this many consecutive identical non-thought chunks, default `8`), `max_text_without_tool` (abort after this many runes of text with zero tool calls, default `20000`). Set `HAKASE_MAX_OUTPUT_TOKENS` to override the cap via environment.
- `approval` - Interactive approval gate for sensitive tool calls: `mode` (`interactive` or off) and `expiry_seconds` (auto-deny timeout, default `60`). Works in both the TUI and the web UI.
- `clarify` - Mid-run clarify questions: `expiry_seconds` (auto-dismiss timeout).
- `auth` - Web server auth tuning: `allow_insecure_cookie` (permit the session cookie without the `Secure` flag on non-loopback plain-HTTP; the `--insecure-cookie` CLI flag overrides it).
- `thinking_level` - Thinking budget hint for models that support it (editable in the web UI Settings view).
- `chat_buffer_size` / `show_thinking` / `task_checkpoint` - TUI chat history size, thinking display default, and task checkpointing toggles.

### Sidekick configuration

The `sidekick` block in `config.json` configures the second model. All fields are optional; an empty/absent block disables the feature.

- `enabled` - `true` to turn the sidekick on. Pointer-style so "absent" stays distinct from `false`.
- `mode` - `off` | `on_demand` | `watch` | `full` (default `on_demand` when enabled without a mode).
- `provider` - `gemini` | `openai` | `openai-compatible`. Empty reuses the primary provider.
- `model_name` - the sidekick model. Required - without a model the feature is forced off.
- `base_url` - optional endpoint override for the sidekick model.
- `api_key` - optional key override for the sidekick model.
- `evaluate_debounce_seconds` - spacing between watchdog evaluations (default 20).
- `max_evaluations_per_run` - cap on watchdog evaluations per run (default 5).
- `max_notes_per_turn` - advisory notes emitted per watchdog turn (default 2).
- `max_note_chars` - max rendered length of each advisory note (default 1200).
- `transcript_window_chars` - bounds the run-transcript text sent to the watchdog (default 6000).

Provider resolution mirrors the vision resolver: explicit `provider` wins, else a `base_url` forces `openai-compatible`, else the primary provider is reused. See `config.json.example` for a full template.

### Channels configuration

The `channels` block configures communication channels - chat transports that prompt the agent, watch progress, answer approval/clarify prompts, and manage tasks/cron remotely (Telegram today; the `internal/channel` core is transport-neutral). Channels run inside the `web`/`serve` process, sharing its runner, gates, SSE bridge, and session service, so approvals can be answered from the web UI or the phone - first responder wins. Everything is off unless explicitly enabled.

- `channels.enable_cron_scheduler` - start the background cron scheduler in web/serve mode (normally TUI-only, so scheduled jobs fire only while the terminal is open). Set `true` for headless deployments so cron results can be delivered to a channel.
- `channels.telegram.enabled` - `*bool`; the channel starts only when explicitly `true` **and** a `bot_token` is present.
- `channels.telegram.bot_token` - the [@BotFather](https://t.me/BotFather) bot token. Write-only through the web config API (set via the `telegram_bot_token` control key, cleared via `clear_telegram_bot_token`; never returned by `GET /api/config`).
- `channels.telegram.allowed_user_ids` - static allowlist of Telegram numeric user IDs (deny-by-default). Empty = runtime pairing via `/start <code>`.
- `channels.telegram.pairing_code` - optional static pairing code for scripted setups instead of the generated rotating code. Also write-only through the web config API.

Pairing codes generated at runtime are 6 digits, valid 15 minutes, and surfaced three ways: the server console at boot, `hakase channels pair-code`, or `POST /api/channels/pairing-code` (the Channels page in the web UI). The pending code is never returned by `GET /api/channels` - only its expiry. Pairings and per-chat bindings (session, notify flag) persist in `~/.hakase/channels.json` (0600, flock-protected, sandbox-denied); revoke via the web UI, `hakase channels revoke <user-id>`, or by deleting the entry from the file while the server is stopped.

Behavior notes: inbound text and photo captions are supported (albums buffer ~1.5s into one prompt; voice/files are deferred); each chat runs one agent turn at a time with `/stop` to cancel; approval/clarify gate expiries are clamped to >=300s when a channel is enabled (mobile round-trip); env overrides are `HAKASE_TELEGRAM_ENABLED` and `HAKASE_TELEGRAM_BOT_TOKEN`. Config changes to this block need a server restart to take effect.

### Environment variables

Environment variables override the matching `config.json` fields, with environment variables taking precedence over the file. If `config.json` is missing but at least one of these is set, the config is built entirely from the environment:

| Variable | Overrides |
| -------- | --------- |
| `HAKASE_API_KEY` | `api_key` |
| `HAKASE_PROVIDER` | `provider` |
| `HAKASE_MODEL` | `model_name` |
| `HAKASE_BASE_URL` | `base_url` |
| `HAKASE_SUMMARY_MODEL` | `summary_model` |
| `HAKASE_VISION_MODEL` | `vision_model` |
| `HAKASE_VISION_BASE_URL` | `vision_base_url` |
| `HAKASE_VISION_API_KEY` | `vision_api_key` |
| `HAKASE_VISION_PROVIDER` | `vision_provider` |
| `HAKASE_MODEL_VISION` | `model_vision` |
| `HAKASE_DEBUG` | `debug` |
| `HAKASE_MAX_OUTPUT_TOKENS` | `loop_guard.max_output_tokens` |
| `HAKASE_TELEGRAM_ENABLED` | `channels.telegram.enabled` |
| `HAKASE_TELEGRAM_BOT_TOKEN` | `channels.telegram.bot_token` |
| `HAKASE_HOME` | user home directory (default `~/.hakase`) |

Note: `HAKASE_*` variables are scrubbed from the environment of subprocesses spawned by the agent (see `system_exec`), so the API key used for providers never leaks into shell commands or sandboxed Python runs.

### User home (`~/.hakase/`)

User-level agent state lives under `~/.hakase/` (Claude-style; override with `$HAKASE_HOME`):

- `~/.hakase/config.json` - user-level config fallback, used when no project `config.json` exists
- `~/.hakase/skills/` - user-level markdown skills, discovered automatically
- `~/.hakase/knowledge/` - optional user-global knowledge base (set `knowledge_dir: "~/.hakase/knowledge"`)
- `~/.hakase/cronjobs.json` - cron job registry (flock-protected)
- `~/.hakase/channels.json` - channel state: paired users, per-chat bindings, pending pairing code (flock-protected, sandbox-denied)

### Migration note

hakase migrated from the ADK v1 stack to the ADK v2 stack (`google.golang.org/adk/v2`). The configuration format is unchanged, so existing `config.json` files continue to work without modification (backward compatible). An empty `provider` field still selects Gemini, matching the previous single-provider behavior.

### Troubleshooting

- **Unsupported provider error** -- `unsupported provider: <name>` means the `provider` field is set to a value other than `gemini`, `openai`, or `openai-compatible`. Correct the value or leave it empty to use the default.
- **Empty API key error** -- `gemini provider requires an api_key` or `openai provider requires an api_key` means the `api_key` field is missing for the selected provider. Set a valid key in `config.json`.
- **OpenAI-compatible endpoint unreachable** -- when using `openai-compatible`, confirm the server at `base_url` is running and serves an OpenAI-compatible API (e.g. Ollama at `http://localhost:11434/v1`), and that it is reachable from the machine running the agent.

---

## Dependencies

Go (see `go.mod`, module `amurru/hakase`):

| Package | Purpose |
| ------- | ------- |
| `charm.land/bubbletea/v2` | TUI framework |
| `charm.land/bubbles/v2` | TUI components (text input, viewport) |
| `charm.land/lipgloss/v2` | Terminal styling |
| `charm.land/glamour/v2` | Terminal markdown rendering |
| `doug/termtex` | Pure-Go Unicode math rendering (TUI fallback renderer) |
| `google.golang.org/adk/v2` | Google Agent Development Kit (v2) |
| `google.golang.org/genai` | Gemini AI client |
| `github.com/openai/openai-go/v3` | OpenAI API client |
| `github.com/modelcontextprotocol/go-sdk` | MCP client (multi-server, stdio + HTTP) |
| `github.com/go-chi/chi/v5` | Web server router (internal/web) |
| `github.com/golang-jwt/jwt/v5` | JWT issuing/verification (web auth) |
| `golang.org/x/crypto` | Argon2id password hashing (web auth) |
| `golang.org/x/time` | Login rate limiting (web middleware) |
| `github.com/cyphar/filepath-securejoin` | Symlink-safe secure path joining for sandbox confinement |
| `github.com/robfig/cron/v3` | 5-field cron parsing for scheduled tasks |
| `github.com/atotto/clipboard` | Clipboard access for image paste (TUI) |
| `gopkg.in/yaml.v3` | Skill frontmatter parsing |

Web UI (`webui/`, installed with pnpm): Vue 3, Vue Router, Pinia, Vite, TypeScript, Tailwind CSS 4, reka-ui, markdown-it + KaTeX + Mermaid + highlight.js + DOMPurify (rendering), vue-sonner (toasts), @vueuse/core; vitest + jsdom for tests.

---

## TODO -- Deferred Items

The creative skills port (see `.omo/plans/creative-skills-port.md`) deliberately deferred a subset of the Hermes creative skills that depend on capabilities hakase did not have at the time. Native **MCP client support on the orchestrator** is now implemented - connect any MCP server (stdio or HTTP) via the `mcp` config block and manage it from the TUI with `/mcp`. **Media generation is now implemented**: `generate_image` (cloud via OpenAI/OpenAI-compatible including OpenRouter, fal.ai, plus offline pil fallback - zero config) and `generate_video` (OpenRouter/OpenAI-compatible async jobs API incl. image-to-video via first frame; fal.ai text-to-video) are available as orchestrator tools; `generate_audio` is a stub wired for v2. `baoyu-infographic` now prefers `generate_image`. Still deferred:

- `touchdesigner-mcp` - requires a TouchDesigner MCP server; can now be added via the generic `mcp` config once a server endpoint is available.
- `comfyui` - ported as doctrine + gated on user infra (ComfyUI/comfy-cli, GPU or cloud); native image/video generation is now provided by the media layer, ComfyUI integration is deferred to v2 (GPU + local models).
- `songwriting-and-ai-music` - doctrine ported; generation is external (Suno); native TTS/audio deferred to v2 (generate_audio stub).
- `git push` / `git pull` / `git clone` and destructive git ops (`checkout`/`reset`/`clean`) - structured git tools ship read-only + stage/commit in v1 (see [docs/git-tools/](docs/git-tools/)); push/pull/clone (network policy interplay) and destructive ops (force-flag mapping) are deferred to v2.
