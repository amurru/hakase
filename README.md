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
| `google.golang.org/adk`                  | Google Agent Development Kit          |
| `google.golang.org/genai`                | Gemini AI client                      |
| `github.com/modelcontextprotocol/go-sdk` | MCP client for browser automation     |

---

## License

MIT
