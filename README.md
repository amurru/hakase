# hermes-go-agent

A high-autonomy, general-purpose AI research and navigation agent built in Go, featuring a rich terminal TUI, Google ADK orchestration with Gemini, MCP server integration, a Python code interpreter, and a self-evolving skill library.

![Go](https://img.shields.io/badge/Go-1.26-blue?logo=go)
![License](https://img.shields.io/badge/License-MIT-green)

---

## Overview

**hermes-go-agent** is a terminal-based AI agent harness inspired by the Hermes Agent framework. It orchestrates multiple specialized sub-agents — a **Web Researcher** and a **Code Interpreter** — through a Google ADK root orchestrator, powered by Gemini models. The entire interaction happens inside a simple split-pane TUI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

The agent can:

- 🔍 **Browse & research** the web using MCP-connected browser tools
- 📥 **Download** files, PDFs, and images from the internet
- 🐍 **Execute Python** code in an isolated virtual environment with auto-dependency resolution
- 📊 **Analyze data**, generate charts, and produce visual artifacts
- 🧠 **Learn & persist skills** — novel Python workflows are automatically saved to a local skill library for future reuse
- 📂 **Manage outputs** — generated HTML files, data artifacts, and more are saved to `./outputs/`

---

## Project Structure

```
hermes-go-agent/
├── main.go                  # Entry point — loads config, boots the TUI and agent runner
├── agent.go                 # Core agent logic: ADK setup, sub-agents, tools (Python interpreter, downloader, skill manager)
├── ui.go                    # Bubble Tea TUI — split-pane layout with chat, log, and input views
├── config.go                # Config loader (reads config.json)
├── config.json              # Runtime configuration (API key, model, MCP server URL)
├── config.json.example      # Example config template
├── go.mod / go.sum          # Go module dependencies
├── skills/                  # Persisted Python skill library
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
- **Bottom** — Text input with focus cycling (`Tab` to switch panes)
- Keyboard shortcuts: `Ctrl+C` / `Esc` to quit, `Enter` to submit

### 🤖 Multi-Agent Orchestration

Powered by [Google ADK](https://github.com/google/adk):

| Agent                | Role                                                                 |
| -------------------- | -------------------------------------------------------------------- |
| **orchestrator**     | Root agent that delegates tasks to sub-agents based on intent        |
| **web_researcher**   | Searches the web, navigates pages, downloads files, extracts content |
| **code_interpreter** | Executes Python, performs data analysis, manages the skill library   |

### 🐍 Python Code Interpreter

- Runs Python code in an isolated `.venv` virtual environment
- **Auto-resolves missing dependencies** — detects `ModuleNotFoundError`, installs the package via pip, and retries
- Sets `PYTHONPATH` to include `./skills` so persisted skills are importable

### 🧠 Self-Evolving Skill Library

The agent can save tested Python scripts as reusable skills:

1. Code is executed and verified via `python_interpreter`
2. The agent calls `save_skill` to persist the script to `./skills/`
3. Skills are registered in `skills/skills.json` with name, description, and import usage
4. On subsequent runs, the agent loads all saved skills and can reuse them via `from skills.<name> import ...`

### 📥 File Download

- Downloads files from any HTTP/HTTPS URL
- Saves to `./downloads/` with automatic filename resolution
- Supports PDFs, images, datasets, and binary blobs

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

hermes-go-agent is currently being developed and tested on **Linux**.

### Configuration

Edit `config.json`:

```json
{
  "provider": "gemini",
  "model_name": "gemini-3.5-flash-lite",
  "api_key": "your-gemini-api-key",
  "instruction": "You are a web automation agent harness.",
  "mcp_server_url": "http://localhost:9223/mcp"
}
```

### Providers

hermes-go-agent supports multiple LLM providers, selected via the `provider` field in `config.json`. An empty or missing `provider` value defaults to `gemini`, preserving previous behavior.

| Provider             | Description                                        | Default Model      |
| -------------------- | -------------------------------------------------- | ------------------ |
| `gemini`             | Google Gemini                                      | `gemini-2.5-flash` |
| `openai`             | OpenAI API                                         | `gpt-4o-mini`      |
| `openai-compatible`  | OpenAI-compatible endpoints (Ollama, vLLM, etc.)    | `gpt-4o-mini`      |

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

#### Environment variables

Environment variable support is planned and has not landed yet. When implemented, the following variables will override the matching `config.json` fields, with environment variables taking precedence over the file:

| Variable          | Overrides    |
| ----------------- | ------------ |
| `HERMES_API_KEY`  | `api_key`    |
| `HERMES_PROVIDER` | `provider`   |
| `HERMES_MODEL`    | `model_name` |
| `HERMES_BASE_URL` | `base_url`   |

#### Migration note

hermes-go-agent migrated from the ADK v1 stack to the ADK v2 stack (`google.golang.org/adk/v2`). The configuration format is unchanged, so existing `config.json` files continue to work without modification (backward compatible). An empty `provider` field still selects Gemini, matching the previous single-provider behavior.

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

Skills are stored in `./skills/` and can be extended by the agent at runtime.

---

## Dependencies

| Package                                  | Purpose                               |
| ---------------------------------------- | ------------------------------------- |
| `charm.land/bubbletea/v2`                | TUI framework                         |
| `charm.land/bubbles/v2`                  | TUI components (text input, viewport) |
| `charm.land/lipgloss/v2`                 | Terminal styling                      |
| `google.golang.org/adk/v2`               | Google Agent Development Kit (v2; was v1) |
| `google.golang.org/genai`                | Gemini AI client                      |
| `github.com/openai/openai-go/v3`         | OpenAI API client                     |
| `github.com/modelcontextprotocol/go-sdk` | MCP client for browser automation     |

---

## License

MIT
