# hakase Architecture - Deep Dive

## Runtime Topology

```
hakase binary (Go)
├── main.go            - entry point, CLI dispatch, config load, TUI boot
├── agent.go           - setupRunner(): builds all 4 ADK agents + tools
├── ui.go              - Bubble Tea TUI (chat / log / task panes, help overlay, hint bar)
├── slash.go           - slash command registry + command menu (/compact, /new, /sessions, /help, /exit)
├── task_slash.go      - /board slash command (TUI mirror of the task CLI)
├── attach.go          - message attachments (@ mention menu, chips, genai parts)
├── clipboard.go       - clipboard copy/paste backends (wl-copy/xclip/xsel) + image paste + pane copy
├── queue.go           - mid-run message queue + run control (Esc-interrupt state)
├── delegate.go        - delegate_task tool, progress reporting, dedup cache, watchdog
├── cronjob.go         - cronjob tool, schedule parsing, 30s scheduler, headless executor (outputs/cron/)
├── sandbox.go         - path confinement (securejoin, root normalization)
├── sandboxexec.go     - bubblewrap (bwrap) subprocess isolation
├── systemexec.go      - system_exec toolset (shell routing, hardening, env scrubbing)
├── fileops.go         - read/write/patch/search file tools
├── instruction_context.go - project context files (AGENTS.md) discovery, rendering,
│                            subdirectory hints, live reconcile
├── rule_cli.go        - hakase rules CLI (list/show project context files)
├── knowledge*.go      - knowledge base engine + 8 tools + CLI
├── skill*.go          - skill discovery, SKILL.md parser, skill CLI
├── session*.go        - session persistence, history building
├── summarize.go       - async LLM summarization (9-section template) for compaction + /compact
├── gate.go            - harmful-command approval gate
├── loopguard.go       - anti-degeneration guardrails
└── provider*.go       - gemini / openai / openai-compatible providers
```

## Agent Construction (setupRunner)

1. Loads sandbox config, approval config, loop guard from `cfg`.
2. Creates the provider via `ProviderFactory(cfg)`; validates config; resolves model name.
3. Discovers project context files (AGENTS.md, with a project-scoped CLAUDE.md fallback),
   renders the block, and records session state via `initContextState` (hints + reconcile).
4. Creates MCP toolset (Lightpanda browser) + download tool -> `web_researcher`.
5. Creates python interpreter tool, save_skill tool, discovers markdown skills, creates
   list/load skill tools, knowledge tools -> `code_interpreter`.
6. Creates file-op tools -> `general_purpose`.
7. Creates system_exec tools, 6 task-board tools, delegate_task tool, and the cronjob tool (orchestrator only).
8. Wires the root `orchestrator` with all of the above + history builder + sub-agents.

## Delegation Model

- `delegate_task(agent_name, goal, context)` spawns an isolated sub-agent session with
  task tracking. Recommended over `transfer_to_agent` for most work.
- Sub-agents CANNOT call: `delegate_task`, `clarify`, `memory`, `send_message`, `cronjob`.
- Watchdog: a delegated run is aborted after `delegate_timeout_seconds` (default 300s).
- Progress streams to the TUI via `delegationProgressNotify`.
- A dedup cache prevents re-delegating identical in-flight tasks.

## Scheduled Tasks (cronjob)

- **One tool, action-style API**: the `cronjob` tool on the orchestrator (only) manages
  jobs with actions `create` / `list` / `update` / `pause` / `resume` / `run` / `remove`.
  Sub-agents cannot call it (see the blocked list above).
- **Schedules** (`parseSchedule`): relative one-shot `'30m'` / `'2h'` / `'1d'` / `'45s'`,
  recurring intervals `'every 30m'` / `'every 2 hours'`, 5-field cron `'0 9 * * *'`
  (6-field rejected; parsed via `github.com/robfig/cron/v3`), and ISO timestamps
  `'2026-06-01T09:00:00'`.
- **Persistence**: `~/.hakase/cronjobs.json` (or `$HAKASE_HOME/cronjobs.json`), written
  atomically via tmp-file + rename under a mutex and a cross-process flock - the same
  pattern as `tasks.json`.
- **Scheduler**: a 30-second ticker goroutine started in `setupRunner` fires due jobs
  while the TUI is open. `hakase cron tick` runs all due jobs once from the CLI.
- **Headless execution**: due jobs run in fresh isolated sub-agent sessions, reusing the
  delegate sub-agent pattern (watchdog, loop guard, tool-call repair) with optional
  markdown skill context injected before the prompt; `cronModelBootstrap` bootstraps the
  model for headless `run`/`tick`.
- **Outputs**: results are written to `outputs/cron/<job-id>-<timestamp>.md`; completed
  and failed jobs surface a notice in the TUI chat pane via `CronJobMsg`. A `[SILENT]`
  marker in a job's output suppresses delivery (monitoring-style jobs).
- **Lifecycle**: `scheduled`, `paused`, `running`, `completed`. One-shot jobs complete
  after firing; recurring jobs persist until paused/removed; an optional repeat count
  (0 = unlimited) completes a job after N runs.
- **CLI**: `hakase cron list|status|pause <id>|resume <id>|run <id>|tick` - `list`/
  `status`/`pause`/`resume` are pure file operations; `run`/`tick` bootstrap the model.
  Exit codes: 0 ok, 1 runtime error, 2 usage.

## Context Management

- The root orchestrator has a HistoryBuilder that persists conversation history to the
  session store and injects it via a `BeforeModelCallback`.
- Budget math uses model capabilities fetched in the background (`FetchModelInfo`);
  the rendered project-context block is folded into the compaction reserve
  (`contextBlockTokens`).
- `summary_model` (cheaper/weaker) compacts context when the budget gets tight;
  falls back to the primary model when unset or on creation failure. Compaction is
  a cascade: deterministic history snip (keeps the last two turns) runs in the
  callback, then `summarize.go` produces an async 9-section running summary that is
  re-injected at the front of history. `/compact [focus]` triggers the same cascade
  manually with an optional steering instruction.
- **Mid-run steering**: the TUI's `pendingQueue` is shared with the HistoryBuilder
  (`SetPendingQueue`); `BeforeModelCallback` injects queued user messages (typed
  while the agent was busy) into the tail of every request as `USER INTERJECTION`
  content. The queue drains at run end; an Esc-interrupt merges all pending prompts
  into a single turn. Messages with attachments persist only path+MIME refs
  (`AttachmentRef`); `messageToContent` re-reads content from `Path` on resume.
- Sub-agents keep isolated context by design.

## Project Context Files (AGENTS.md)

- **Discovery** (`DiscoveredInstructionFiles`): `AGENTS.md` files stacked from cwd up to
  the git root (closest first); a project-scoped `CLAUDE.md` fallback only when no
  `AGENTS.md` exists in the walk; user-global `~/.hakase/AGENTS.md`; config extras from
  `instruction_files` (paths or http(s) URLs). `~/.claude/CLAUDE.md` is never loaded.
- **Injection**: rendered as `### PROJECT CONTEXT FILES:` + `Instructions from: <path>`
  entries, appended to every agent instruction (`context_files.apply_to` gates per
  agent). The `config.instruction` field renders as a `USER CONFIG INSTRUCTION` section.
- **Safety**: every file is prompt-injection scanned (`sanitizeContextContent` - also
  applied to knowledge notes and markdown skill bodies) and truncated per file
  (`context_files.max_chars`, default 20k, 70/20 head/tail split).
- **Subdirectory hints**: `read_file`/`search_files` attach nearby `AGENTS.md` (below
  the workspace root, not already in the prompt) to the tool result, once per session
  (8k per-file cap). The dedup set persists on the session
  (`Session.HintedContextFiles`) so a resumed session does not re-attach.
- **Live reconcile**: `BeforeModelCallback` checks a path/size/mtime fingerprint of the
  loaded context files; on change it injects a one-shot `PROJECT CONTEXT UPDATE` notice.
- **CLI**: `hakase rules list|show` previews the active context files.

## Thinking & Generation

- `buildGenerationConfig(level)` maps `thinking_level`: `off` disables thoughts;
  named levels (`low`/`high`/`maximum`/`xhigh`) pass through to the provider.
- `loop_guard.max_output_tokens` caps `maxOutputTokens` for every agent.

## Time Reminder

Every agent instruction appends `buildTimeReminder()`, injecting the current wall-clock
date/time and UTC offset so the model reasons about recency correctly and prefers live
search results over stale training data.
