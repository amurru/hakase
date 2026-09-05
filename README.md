# Hakase

A high-autonomy, general-purpose AI research and navigation agent built in Go, featuring a rich terminal TUI **and a browser-based web UI**, Google ADK orchestration across multiple model providers (Gemini, OpenAI, and OpenAI-compatible endpoints), MCP server integration, a Python code interpreter, and a self-evolving skill library.

[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/amurru/hakase)
![Go](https://img.shields.io/badge/Go-1.26-blue?logo=go)
![License](https://img.shields.io/badge/License-MIT-green)

> **New to hakase?** This README is the user quick-start. For the full technical deep dive -- project structure, build tags, release engineering, architecture, sandboxing, MCP, sidekick, and every config field -- see **[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)**.

---

## Overview

**hakase** is an AI agent harness inspired by the Hermes Agent framework. It orchestrates multiple specialized sub-agents -- a **Web Researcher** and a **Code Interpreter** -- through a Google ADK root orchestrator, powered by configurable LLM providers (Gemini, OpenAI, or any OpenAI-compatible endpoint). Interaction happens either in a split-pane terminal TUI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea), or in a browser through the bundled web UI (`hakase web`).

The agent can:

- 🔍 **Browse & research** the web using MCP-connected browser tools
- 📥 **Download** files, PDFs, and images
- 🐍 **Execute Python** in an isolated venv with auto-dependency resolution
- 📊 **Analyze data**, generate charts, and produce visual artifacts
- 🧠 **Learn & persist skills** -- novel Python workflows are saved for reuse
- 📚 **Manage a knowledge base** -- wiki-style markdown notes with `[[wikilinks]]`
- 🛡️ **Sandboxed execution** -- path confinement by default, optional bubblewrap isolation
- 💻 **Run system commands** via `system_exec`
- 📂 **Manage outputs** in `./outputs/`
- ⏰ **Schedule recurring tasks** via `cronjob` (cron/interval/ISO, persisted to `~/.hakase/cronjobs.json`)

---

## Quick Start

### Prerequisites

- **Go** 1.26+
- **Node.js + pnpm** -- builds the web UI (`webui/`). The SPA is embedded into the Go binary via `//go:embed`, so a frontend build is part of compiling the project
- **Python 3** -- code interpreter (`.venv` execution) and the skill library (`./skills/`)
- An **API key** for your chosen provider -- Gemini, OpenAI, or an OpenAI-compatible endpoint (Ollama, vLLM). The required key depends on the `provider` field in `config.json`.
- **Lightpanda** (optional but recommended) -- the MCP browser automation server at [lightpanda.ai](https://lightpanda.ai) (`http://localhost:9223/mcp` by default). Any spec-compliant browser MCP also works -- see [Browser MCP presets](docs/browser-mcp-presets.md).

### Setup

```bash
# 1. Clone and configure
cp config.json.example config.json
# Edit config.json with your API key (matching your provider) and MCP server URL

# 2. Build the frontend (required once on a fresh clone)
make build-frontend

# 3. Run the TUI
go mod download
go run ./cmd/hakase/
```

Type your question and press `Enter`. The agent will research, analyze, and respond -- all from your terminal.

```bash
# 4. (Optional) Run the web UI instead
go run ./cmd/hakase/ auth set-password   # one-time: create the admin login
go run ./cmd/hakase/ web                 # SPA + API on http://127.0.0.1:8080
```

> **Fresh clone gotcha:** `internal/web/dist/` is gitignored but required at compile time by `//go:embed all:dist` ([internal/web/embed_prod.go](internal/web/embed_prod.go)). Until you run `make build-frontend`, `go build ./...` and `go test ./...` fail for `internal/web`. `make clean` removes the mirror again. See [Build System & Tags](docs/DEVELOPMENT.md#build-system--tags).

<details>
<summary><b>Building a production binary</b></summary>

```bash
make build          # frontend + go build -tags prod -o hakase ./cmd/hakase/
./hakase            # run the binary
make build-windows  # cross-compile windows/amd64 zip (unsigned)
make clean          # remove webui/dist, internal/web/dist, and binary
```

| Target | Description |
| ------ | ----------- |
| `make build` | Full production binary (prod tag, embedded SPA) |
| `make release` | Same as `make build`, then echo the stamped version |
| `make build-frontend` | `pnpm install && pnpm build` + mirror into `internal/web/dist/` |
| `make dev-frontend` | Vite dev server with HMR on port 5173 |
| `make dev-backend` | `go run -tags dev ./cmd/hakase/ web` (live disk serving, API on :8080) |
| `make test` | `go test ./...` |
| `make clean` | Remove build artifacts |

Every `make build` stamps version/commit/date so `hakase version` is reproducible. See [Release Engineering](docs/DEVELOPMENT.md#release-engineering) for tagging and SLSA provenance.

</details>

<details>
<summary><b>Two-terminal web UI development (HMR)</b></summary>

```
make dev-frontend   # terminal 1 - Vite dev server, HMR, port 5173
make dev-backend    # terminal 2 - Go server with the dev tag, port 8080
```

Open <http://localhost:5173> -- Vite proxies `/api` to the Go server on :8080. No Go rebuild needed for frontend changes. Frontend tests: `cd webui && pnpm test`.

</details>

---

## Web UI

`hakase web` serves the full SPA + API at `http://127.0.0.1:8080` (default). `hakase serve` is API-only at `:8081`. Both share the same flags:

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--port <n>` | `8080` (web) / `8081` (serve) | Port to listen on |
| `--host <addr>` | `127.0.0.1` | Host address to bind to |
| `--insecure-cookie` | off | Allow session cookie without `Secure` on plain HTTP (local dev only) |

The SPA is Vue 3 + TypeScript + Vite + Tailwind 4 (Pinia, Vue Router, reka-ui, markdown-it + KaTeX + Mermaid + highlight.js). Key views: **Chat** (SSE-streamed, markdown + LaTeX + Mermaid, `@` attachments, image lightbox), **Sessions**, **Tasks**, **Knowledge**, **Skills**, **MCP**, **Cron**, **Files**, **Settings**. Approval and clarify gates work in the browser too.

> Before `web`/`serve` will start, create the admin login: `hakase auth set-password` (argon2id, stored at `~/.hakase/credentials.json` mode `0600`). The JWT secret lives at `~/.hakase/jwt-secret`. See [Authentication](#authentication) and [Reverse Proxy](#reverse-proxy-caddy).

For the full API surface and SPA details, see [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#web-ui-development-flow) and [internal/web](../internal/web).

---

## CLI Reference

Running with no subcommand launches the TUI; `web`/`serve` start the HTTP server. Other subcommands are file-only (no model needed unless noted):

| Command | Action |
| ------- | ------ |
| `skill` | Manage markdown skills (`create`, `list`, `validate`, `evolve`) |
| `task` | Manage the task board (`create`, `list`, `get`, `update`, `complete`, ...) |
| `knowledge` | Manage the knowledge base (`list`, `read`, `search`, `lint`, `create`, `link`, `bench`) |
| `session` | Manage sessions (`list`, `delete`, `archive`) |
| `rules` | List/show active project context files (`AGENTS.md`) |
| `env` | Print the detected runtime-environment block |
| `cron` | Manage scheduled tasks (`list`, `status`, `pause`, `resume`, `run`, `tick`) |
| `auth` | Manage web authentication (`set-password`) |
| `version` | Print build version (version, commit, build date, Go runtime) |

```bash
hakase version
hakase knowledge search "quantum"
hakase skill list
hakase cron list
```

---

## Configuration

Copy the template and edit the few fields you need:

```bash
cp config.json.example config.json
```

```json
{
  "provider": "gemini",
  "model_name": "gemini-3.7-flash",
  "api_key": "your_api_key",
  "mcp": {
    "servers": {
      "lightpanda": { "type": "http", "url": "http://localhost:9223/mcp" }
    }
  }
}
```

| Provider | When to use | Default model |
| -------- | ----------- | ------------- |
| `gemini` | Google Gemini (default) | `gemini-3.7-flash` |
| `openai` | OpenAI API | `gpt-5.6-terra` |
| `openai-compatible` | Ollama, vLLM, any OpenAI-compatible endpoint | none -- `model_name` required |

Environment variables override `config.json` and can build the config entirely from the environment when the file is missing: `HAKASE_API_KEY`, `HAKASE_PROVIDER`, `HAKASE_MODEL`, `HAKASE_BASE_URL` (plus `HAKASE_SUMMARY_MODEL`, `HAKASE_VISION_*`, `HAKASE_HOME`, etc.).

<details>
<summary><b>Full configuration reference</b></summary>

All fields are optional unless noted. See [docs/DEVELOPMENT.md#configuration-reference](docs/DEVELOPMENT.md#configuration-reference) and [config.json.example](config.json.example).

- `provider` / `model_name` / `api_key` / `base_url` -- provider selection (above)
- `instruction` / `instruction_files` / `context_files` -- project context (`AGENTS.md`) loading. See [Project Context Files](docs/DEVELOPMENT.md#project-context-files-agentsmd).
- `system_env` -- runtime environment block (`enabled`, `max_chars`, `apply_to`). See [Runtime Environment Awareness](docs/DEVELOPMENT.md#runtime-environment-awareness).
- `knowledge_dir` -- knowledge base directory (default `./knowledge`; `~` expands to home).
- `mcp` / `mcp_server_url` -- MCP servers (legacy `mcp_server_url` auto-migrates to `lightpanda`). See [MCP Integration](docs/DEVELOPMENT.md#mcp-integration).
- `sandbox` -- confinement (`paths` default, `bubblewrap`, `landlock`, `off`). See [Sandboxing](docs/DEVELOPMENT.md#sandboxing--workspace-confinement).
- `loop_guard`, `approval`, `clarify`, `auth`, `thinking_level`, `chat_buffer_size` -- gates and TUI tuning
- `vision_*` / `model_vision` -- vision routing for non-vision main models
- `summary_model` -- cheaper model for context compaction
- `search_expansion` -- HyDE-lite query expansion for `search_knowledge` (off by default)
- `sidekick` -- second model (on-demand/watch). See [Sidekick](docs/DEVELOPMENT.md#sidekick-second-model) and [docs/sidekick-agent/](docs/sidekick-agent/).
- `media` -- image/video generation (`openai`, `fal`, `pil` fallback). See [Media Generation](docs/DEVELOPMENT.md#media-generation) and [docs/media-generation/support.md](docs/media-generation/support.md).
- `units.system` -- `metric` (default, SI/ISO) or `imperial`
- `HAKASE_HOME` -- user home dir (default `~/.hakase`): holds `config.json` fallback, `credentials.json`, `jwt-secret`, `mcp.json`, `cronjobs.json`, `skills/`, `knowledge/`

**Example -- OpenAI-compatible (Ollama):**

```json
{
  "provider": "openai-compatible",
  "model_name": "llama-3.3-70b",
  "base_url": "http://localhost:11434/v1",
  "api_key": "optional_key"
}
```

</details>

<details>
<summary><b>Troubleshooting</b></summary>

- `unsupported provider: <name>` -- `provider` must be `gemini`, `openai`, or `openai-compatible` (empty defaults to `gemini`).
- `gemini/openai provider requires an api_key` -- set `api_key` in `config.json` or `HAKASE_API_KEY`.
- `openai-compatible` endpoint unreachable -- confirm `base_url` is running and serves an OpenAI-compatible API (e.g. Ollama at `http://localhost:11434/v1`).

</details>

---

## Features at a Glance

| Feature | What it does |
| ------- | ------------ |
| **Terminal TUI** | Split-pane Bubble Tea UI: chat, logs, multi-line input, mid-run queuing, help overlay (`Ctrl+/`) |
| **Web UI** | Vue 3 SPA with the same agent, sessions, tasks, knowledge, skills, MCP, cron, files, settings |
| **Multi-Agent Orchestration** | ADK root orchestrator delegates to `web_researcher`, `code_interpreter`, `general_purpose` |
| **Python Interpreter** | Isolated `.venv`, auto pip install on `ModuleNotFoundError`, sandbox-aware |
| **Skill Library** | Persisted Python skills + markdown skills, with a darwinian evolver loop |
| **Knowledge Base** | Wiki-style notes with `[[wikilinks]]`, 8 knowledge tools, `hakase knowledge` CLI |
| **Git Operations** | Structured `git_status`/`git_diff`/`git_log`/`git_branch` (read-only) and `git_stage`/`git_commit` (mutating, approval-gated) through the same policy as `system_exec` |
| **Sandboxing** | `paths` by default (bubblewrap optional), secret-file deny list, symlink-safe |
| **MCP Client** | Any number of stdio/HTTP MCP servers as `mcp_<server>_<tool>` tools, `/mcp` panel |
| **Media Generation** | `generate_image`/`generate_video` (OpenAI/fal/pil fallback), sandboxed to `outputs/media/` |
| **Sidekick** | Optional second model for on-demand Q&A and watch-mode advisory notes |
| **Vision** | Image loading with SSRF guard, vision-model routing, `@file` and paste support |

Each feature's full reference lives in [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md). Links to focused docs:

- Browser MCP presets: [docs/browser-mcp-presets.md](docs/browser-mcp-presets.md)
- Media generation matrix: [docs/media-generation/support.md](docs/media-generation/support.md)
- Sidekick design: [docs/sidekick-agent/](docs/sidekick-agent/)
- Markdown rendering: [docs/markdown-rendering/](docs/markdown-rendering/)

<details>
<summary><b>Terminal TUI -- keyboard shortcuts, slash commands, attachments</b></summary>

**Keyboard Shortcuts**

| Shortcut | Action |
| -------- | ------ |
| `Ctrl+C` | Quit (also cancels a running agent) |
| `Esc` `Esc` | Interrupt the running agent (double-press within 2s) |
| `Esc` | Close help overlay (never quits) |
| `Ctrl+/` or `?` | Toggle help overlay |
| `Tab` / `Shift+Tab` | Cycle focus: input -> chat -> log -> task |
| `Ctrl+T` | Toggle thinking display |
| `Enter` | Send (queued while busy) |
| `Shift+Enter` / `Ctrl+J` | Insert newline |
| `↑`/`k`, `↓`/`j` | Scroll focused pane |
| `PgUp`/`b`, `PgDn`/`f` | Page up / down |
| `u` / `d` | Half page up / down |
| `Home`/`g`, `End`/`G` | Jump to top / bottom |
| `Ctrl+A` / `Ctrl+E` | Jump to line start / end in input |
| `Ctrl+U` | Clear input |

Mouse wheel scrolling works on the focused pane. The log pane stays pinned to the bottom unless you scroll up.

**Slash Commands** -- type `/` for the filtered menu (arrows navigate, `Tab` completes, `Enter` runs):

| Command | Action |
| ------- | ------ |
| `/board` | Task board: `summary`, `list`, `new <title>`, `get <id>`, `update <id>`, `done <id>`, `fail <id>`, `cancel <id>`, `delete <id>`, `archive <id>`, `claim <id>` |
| `/mcp` | Manage MCP servers: panel or `list` / `enable <name>` / `disable <name>` / `reconnect <name>` |
| `/compact [focus]` | Summarize conversation to free context (same cascade as auto-compaction) |
| `/new` | Start a fresh session |
| `/sessions` | Open session chooser |
| `/help` | Shortcut and slash command reference |
| `/exit` / `/quit` | Exit (terminal only) |

Slash commands also work in the web UI (autocomplete palette, `Tab`/`Enter` complete) except `/exit`. `/compact` calls `POST /api/sessions/{id}/compact`; `/sidekick <question>` asks the sidekick model.

**File Attachments**

- `@file` -- type `@` for the workspace file picker; `Enter` attaches as a chip (`@name.go`). Text embeds as content; images embed as multimodal input.
- **Image paste** -- copy an image and press `Ctrl+V`; attached as a `[image 1]` chip.
- Chips render above the input; `Backspace` on an empty input removes the last chip. Attachments persist with the session.

**Mid-run messaging & math**

- Messages typed while the agent is busy are queued (`N queued` in the hint bar) and steered as a `USER INTERJECTION` at the next model-call boundary.
- The agent can pause with a `clarify` question (up to 4 options + free text, `Esc` to dismiss).
- LaTeX math renders inline: display math (`$$...$$`) via tectonic+poppler+kitty graphics on supported terminals, Unicode fallback elsewhere; inline math (`$...$`) always uses Unicode.

See [docs/DEVELOPMENT.md#tui-deep-dive](docs/DEVELOPMENT.md#tui-deep-dive).

</details>

<details>
<summary><b>Advanced features -- sandboxing, MCP, media, sidekick, context files</b></summary>

- **Sandboxing** -- `paths` (default) confines all file ops/downloads/Python to approved roots; `bubblewrap` adds kernel namespaces; `off` disables. Secret files (`config.json`, `.env`, `~/.hakase/credentials.json`, `jwt-secret`, `cronjobs.json`, etc.) are implicitly denied. See [Sandboxing](docs/DEVELOPMENT.md#sandboxing--workspace-confinement).

- **MCP** -- configure in the `mcp` block of `config.json` (merged with `~/.hakase/mcp.json`). Tools appear as `mcp_<server>_<tool>`; manage live with `/mcp`. Any spec-compliant browser MCP is a config swap -- see [presets](docs/browser-mcp-presets.md). Full reference in [docs/DEVELOPMENT.md#mcp-integration](docs/DEVELOPMENT.md#mcp-integration).

- **Media generation** -- `generate_image` (cloud via OpenAI/OpenAI-compatible incl. OpenRouter, `fal-ai/flux/schnell`, or offline `pil` fallback -- zero config) and `generate_video` (OpenRouter `/api/v1/videos` incl. image-to-video, `fal-ai/wan/v2.7`). All output goes to `outputs/media/` via `securejoin` + atomic write. Configure via the `media` block or `HAKASE_MEDIA_*` / `HAKASE_FAL_KEY`. See [docs/media-generation/support.md](docs/media-generation/support.md).

- **Sidekick** -- optional second model (`sidekick.mode`: `off`/`on_demand`/`watch`/`full`). On-demand Q&A is grounded in the conversation transcript; watch mode emits quiet inline chips. Privacy: on-demand sends chat turns only; watch sends the full transcript. Point at a local endpoint to keep data on-device. See [docs/DEVELOPMENT.md#sidekick-second-model](docs/DEVELOPMENT.md#sidekick-second-model).

- **Project context files** -- `AGENTS.md` collected from cwd up to git root + `~/.hakase/AGENTS.md` + `instruction_files` (paths or `https://` URLs), injection-scanned, truncated, and injected into every agent. Subdirectory `AGENTS.md` attaches on `read_file`/`search_files`. `hakase rules list|show` previews the active context. See [docs/DEVELOPMENT.md#project-context-files-agentsmd](docs/DEVELOPMENT.md#project-context-files-agentsmd).

- **Knowledge base** -- wiki notes in `knowledge/` with YAML frontmatter (`title`, `tags`, `status`, `confidence`, `sources`, `related`, `metadata`) and `[[wikilinks]]`. Eight tools: `save_knowledge`, `recall_knowledge`, `search_knowledge` (BM25-style ranking, optional HyDE-lite expansion), `update_knowledge`, `link_knowledge`, `cite_knowledge`, `list_knowledge`, `lint_knowledge`. CLI: `hakase knowledge create|read|search|lint|bench`. See [docs/DEVELOPMENT.md#knowledge-base](docs/DEVELOPMENT.md#knowledge-base).

</details>

---

## Authentication

```bash
hakase auth set-password   # prompts for username + password (argon2id, 0600 at ~/.hakase/credentials.json)
```

- **Web UI** -- log in through the browser; the server issues a JWT in an HttpOnly cookie.
- **API** -- authenticate with a bearer token (the same JWT) on each request.
- The JWT signing secret lives at `~/.hakase/jwt-secret` (generated on first run, `0600`).

---

## Reverse Proxy (Caddy)

hakase serves plain HTTP only -- terminate TLS at a reverse proxy. Caddy obtains and renews Let's Encrypt certificates automatically:

```
hakase.example.com {
    reverse_proxy localhost:8080
}
```

Point DNS at the machine, run Caddy, and the site is HTTPS automatically. Add `basic_auth` (via `caddy hash-password`) or an IP allowlist as needed. See [Caddy docs](https://caddyserver.com/docs). For production hardening (bind to localhost, protect `credentials.json`, rotate `jwt-secret`), see [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) and the Production Deployment notes below.

---

## Example Workflows

**Research a Topic**
> *"Summarize the latest developments in quantum computing and provide key citations."*
> The orchestrator delegates to `web_researcher`, which navigates sources and returns a synthesized Markdown answer.

**Generate an HTML Game**
> *"Create a fully playable browser game as a single HTML file."*
> The `code_interpreter` writes a self-contained HTML+JS game to `./outputs/` and persists the script as a reusable skill in `./skills/`.

**Data Analysis**
> *"Download this CSV, compute summary statistics, and generate a chart."*
> The agent downloads the file, runs Python with pandas/matplotlib in `.venv`, and saves the output artifact.

---

## Skills

hakase supports **Python skills** (`./skills/` + `skills/skills.json`) and **markdown skills** (`.agents/skills/<name>/SKILL.md`). Markdown skills are discovered from project (`.agents/skills/`, `.claude/skills/`, `.opencode/skills/`, `.gemini/skills/`), custom `skill_dirs`, and user (`~/.hakase/skills/`, etc.), deduped by name. Ported research skills include `domain-intel`, `osint-investigation`, `drug-discovery`, `bioinformatics`, `scrapling`, plus the original `latex-math` and `darwinian-evolver`.

```bash
hakase skill create my-skill --description "Does something useful"
hakase skill list
hakase skill validate .agents/skills/my-skill
hakase skill evolve --mutate          # darwinian-evolver pass over Python skills
```

See [docs/DEVELOPMENT.md#skills-system](docs/DEVELOPMENT.md#skills-system) and [.agents/skills/](../.agents/skills/).

---

## Documentation

| Document | What it covers |
| -------- | -------------- |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | **Start here for developers** -- project structure, build/release, architecture, every feature deep dive, full config reference |
| [docs/browser-mcp-presets.md](docs/browser-mcp-presets.md) | Browser MCP presets (Lightpanda, chrome-devtools-mcp, @playwright/mcp, @browsermcp/mcp) |
| [docs/media-generation/support.md](docs/media-generation/support.md) | Media generation provider matrix and troubleshooting |
| [docs/sidekick-agent/](docs/sidekick-agent/) | Sidekick second-model design |
| [docs/markdown-rendering/](docs/markdown-rendering/) | Markdown rendering plan/design |
| [.agents/skills/hakase/SKILL.md](.agents/skills/hakase/SKILL.md) | Self-knowledge skill -- authoritative agent reference |
| [CHANGELOG.md](CHANGELOG.md) | User-facing changes (Keep a Changelog, semver-ish) |
| [config.json.example](config.json.example) | Full config template with defaults |

---

## Production Deployment

- Use a strong password (`hakase auth set-password`), keep `~/.hakase/credentials.json` at `0600` and out of backups/repos.
- Keep the default `--host 127.0.0.1` and let the reverse proxy forward to it; never expose the Go server directly.
- Rotate `~/.hakase/jwt-secret` periodically to invalidate outstanding tokens.
- TLS is the proxy's job -- never run `--host 0.0.0.0` without a reverse proxy.

### Remote web deployments (registered projects)

When hakase web runs on a host that does *not* already have the client's code,
there is nothing for `git_status`/`git_commit`/the workspace snapshot to anchor
to. Registered projects fix that: an explicit `{name, clone source}` entry that
the host materializes into a managed checkout. See
[docs/git-tools/project-registry.md](docs/git-tools/project-registry.md).

- **Register a project** on the host with
  `hakase projects register <name> <url> [--ref <branch>]` (list/sync/delete
  too), or from the web API via `POST /api/projects`. Checkouts live under
  `~/.hakase/projects/<id>`; entries and statuses persist in
  `~/.hakase/projects.json`.
- **Sessions bind to a project** by picking it in the web UI's New Session
  dialog (or `POST /api/sessions` with `project_id`). Bound sessions anchor
  every git tool to the project checkout and start each run with a fresh
  `GIT WORKSPACE` snapshot of that checkout.
- **Credentials are never stored** (DP-8): clone/push/pull authenticate
  through the host's own mechanisms -- git credential helpers, `gh auth`, or
  an SSH agent -- exactly what running git yourself would use. Nothing secret
  is written into `projects.json` (clone URLs only).
- **Sandbox confinement is per-session**: when the host sandbox is active, a
  project-bound session's git, file, and exec operations are confined to the
  project checkout (the run derives a per-session sandbox pinned to the
  checkout). Sessions not bound to a project keep the process-wide
  configuration.
- **Deleting a project** removes the registry entry and the local checkout; it
  never touches the remote. Re-registering re-clones.

---

<details>
<summary><b>Windows notes</b></summary>

- Shell: string commands run via `cmd /D /C`; POSIX constructs (`$()`, backticks, `VAR=x cmd`) are NOT interpreted -- use cmd syntax (`%VAR%`, `&&`, `|`, `>`).
- Bare executable names resolve from PATH only (`NoDefaultCurrentDirectoryInExePath=1`), rewritten to absolute PATH paths before exec.
- Python: install from [python.org](https://www.python.org/downloads/) so `py` or `python` is on PATH; venv under `.venv\Scripts\`.
- Sandbox: `bubblewrap`/`landlock` coerce to `paths` with a warning on Windows.
- Unsigned binary: v1 Windows builds are not code-signed; verify sha256 in `SHA256SUMS.txt`.
- Browser MCP: use [presets](docs/browser-mcp-presets.md) with Lightpanda or `chrome-devtools-mcp` on Edge.
- Known v1 differences: TUI image paste unsupported (text paste works), web server shuts down via `Ctrl+C` only.

See [docs/DEVELOPMENT.md#windows-notes](docs/DEVELOPMENT.md#windows-notes).

</details>

---

## License

MIT
