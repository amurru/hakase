# hakase Architecture - Deep Dive

## Runtime Topology

```
hakase binary (Go)
├── main.go            - entry point, CLI dispatch, config load, TUI boot
├── agent.go           - setupRunner(): builds all 4 ADK agents + tools
├── ui.go              - Bubble Tea TUI (chat / log / task panes)
├── delegate.go        - delegate_task tool, progress reporting, dedup cache, watchdog
├── sandbox.go         - path confinement (securejoin, root normalization)
├── sandboxexec.go     - bubblewrap (bwrap) subprocess isolation
├── systemexec.go      - system_exec toolset (shell routing, hardening, env scrubbing)
├── fileops.go         - read/write/patch/search file tools
├── knowledge*.go      - knowledge base engine + 8 tools + CLI
├── skill*.go          - skill discovery, SKILL.md parser, skill CLI
├── session*.go        - session persistence, history building
├── gate.go            - harmful-command approval gate
├── loopguard.go       - anti-degeneration guardrails
└── provider*.go       - gemini / openai / openai-compatible providers
```

## Agent Construction (setupRunner)

1. Loads sandbox config, approval config, loop guard from `cfg`.
2. Creates the provider via `ProviderFactory(cfg)`; validates config; resolves model name.
3. Creates MCP toolset (Lightpanda browser) + download tool -> `web_researcher`.
4. Creates python interpreter tool, save_skill tool, discovers markdown skills, creates
   list/load skill tools, knowledge tools -> `code_interpreter`.
5. Creates file-op tools -> `general_purpose`.
6. Creates system_exec tools, 6 task-board tools, delegate_task tool.
7. Wires the root `orchestrator` with all of the above + history builder + sub-agents.

## Delegation Model

- `delegate_task(agent_name, goal, context)` spawns an isolated sub-agent session with
  task tracking. Recommended over `transfer_to_agent` for most work.
- Sub-agents CANNOT call: `delegate_task`, `clarify`, `memory`, `send_message`, `cronjob`.
- Watchdog: a delegated run is aborted after `delegate_timeout_seconds` (default 300s).
- Progress streams to the TUI via `delegationProgressNotify`.
- A dedup cache prevents re-delegating identical in-flight tasks.

## Context Management

- The root orchestrator has a HistoryBuilder that persists conversation history to the
  session store and injects it via a `BeforeModelCallback`.
- Budget math uses model capabilities fetched in the background (`FetchModelInfo`).
- `summary_model` (cheaper/weaker) compacts context when the budget gets tight;
  falls back to the primary model when unset or on creation failure.
- Sub-agents keep isolated context by design.

## Thinking & Generation

- `buildGenerationConfig(level)` maps `thinking_level`: `off` disables thoughts;
  named levels (`low`/`high`/`maximum`/`xhigh`) pass through to the provider.
- `loop_guard.max_output_tokens` caps `maxOutputTokens` for every agent.

## Time Reminder

Every agent instruction appends `buildTimeReminder()`, injecting the current wall-clock
date/time and UTC offset so the model reasons about recency correctly and prefers live
search results over stale training data.
