# Roadmap

Last updated: 2026-09-29. Living index of planned work. The GitHub issues are
the source of truth for status — update this file only when priorities or
scope change, not when individual boxes tick.

## How work is tracked

- **GitHub issues** — one issue per unit of work, evidence-linked (file:line),
  labeled `enhancement` / `bug` / `tech-debt`.
- **`docs/<feature>/`** — per-feature design docs (house convention:
  `spec.md` + `plan.md` + `tasks.md`), written when an issue is picked up and
  kept truthful as the work lands.
- **CHANGELOG "Planned"** — deferrals of already-shipped features.
- **This file** — the tiered index tying it all together.

## Current state (September 2026)

Single-binary Go 1.26 agent harness (ADK Go v2 + embedded Vue 3 SPA) with
three surfaces — TUI, web (SSE + execution canvas), Telegram (forum-topic
sessions) — ~60 built-in tools, MCP (stdio + http), bwrap-ready sandboxing, a
knowledge wiki, three skill systems, and the shipped SkillOpt-Sleep offline
self-improvement loop. Recent arcs: execution canvas, Telegram threads,
built-in web search fallback, git tools + project registry, sidekick, media
generation, SkillOpt-Sleep phases 0–4, auto-memory, MCP 2026-07-28, OTel
tracing, session rewind, Telegram voice, hakase-as-MCP-server.

## Tier 1 — Catch up to the 2026 harness landscape (complete)

All shipped and closed (2026-09-20 → 2026-09-27):

| Issue | What | Ecosystem driver |
|---|---|---|
| [#16](https://github.com/amurru/hakase/issues/16) | Agent-written auto-memory (typed notes, session-start injection) | Claude Code's key differentiator; compounds with SkillOpt-Sleep's miner |
| [#17](https://github.com/amurru/hakase/issues/17) | MCP 2026-07-28 upgrade (go-sdk v1.7+): elicitation → approval/clarify gates, CIMD OAuth, `skill://` serving | largest spec revision to date; hakase's gate system makes elicitation a differentiator |
| [#18](https://github.com/amurru/hakase/issues/18) | OpenTelemetry GenAI tracing from graph events + audit log | converged observability interchange; maps ~1:1 onto existing `GraphEvent`s |
| [#19](https://github.com/amurru/hakase/issues/19) | Session checkpoints / restore-to-message rewind | proven UX (Gemini CLI); cheap given session-per-JSON + the MessageRail |
| [#20](https://github.com/amurru/hakase/issues/20) | Telegram voice in/out (local whisper.cpp + optional Piper TTS) | explicitly deferred today; proven fully-local pattern |
| [#21](https://github.com/amurru/hakase/issues/21) | Session checkpoints (see #19 — split across the two) | — |

## Tier 2 — Bigger bets (pick one or two)

Parking lot: [#20](https://github.com/amurru/hakase/issues/20) (the current
open issue). Per-item notes live there; ordered by expected value:

1. **Hooks system** — shipped (user-scope `PreToolUse`/`PostToolUse` +
   project-scope `.hakase/hooks.json` with content-hash trust +
   `SessionStart`, then `UserPromptSubmit` per-prompt context, per-hook
   `enabled` with live in-place reload, and full CRUD on CLI / web /
   TUI). Design: `docs/hooks/`. Trust model is **Codex's
   content-hash**, not Gemini CLI's fingerprint-and-approve (that one is
   known-broken — gemini-cli#27900).
2. **Hybrid retrieval for knowledge** — shipped: optional dense
   embeddings (OpenAI-compatible endpoint or Ollama) fused with BM25
   via reciprocal rank fusion, default off to preserve
   zero-dependency (`hybrid_search` + `knowledge_embed_model`).
   Design: `docs/hybrid-retrieval/` (HR-001..HR-008).
3. **Second channel transport** — shipped (Discord): DM-only v1
   bot over the gateway websocket (discordgo, no privileged intents),
   mirroring Telegram's auth/runs/gates/push on the shared service.
   Design: `docs/discord/` (DC-001..DC-013). Guilds/voice/Slack stay
   future options.
4. **Durable human-in-the-loop resume** — shipped (opt-in
   `durable_resume`): classic `Runner` + durable pauses, not the
   graph engine (researched and rejected).
   Design: `docs/durable-resume/` (DR Phases 1–7). TUI gates
   covered; channel re-emission is a follow-up.
5. **Audio generation** — shipped: Piper TTS is routed through the
   media registry as the `piper` audio provider, and `generate_audio`
   synthesizes end-to-end (`audio_provider: piper` + `text_to_speech`
   default voice). Design: `docs/media-generation/spec.md` MG-012.

- `docs/git-tools/tasks.md` T7.x remainder: `git_remote` management,
  `git_rebase`/`git_merge`, `git_commit --amend`/signing.

## Tier 3 — Next bets (ordered by expected value)

Designs landed 2026-10-08 as `docs/<feature>/spec.md` + `plan.md` + `tasks.md`
(per maintenance convention 1, specs written ahead of issues so each issue
can point at its design). Open one GitHub issue per item when work starts;
per-item notes live in the issues, not here.

1. **FinOps** — shipped (2026-10-09, opt-in `finops.enabled`): full
   usage capture, priced local ledger, rolling-window budgets (warn/block
   with audit chaining), prompt-cache guard, TUI/web cost meters,
   `GET /api/stats`, `hakase stats`. Design: `docs/finops/` (FO-001..FO-005).
2. **Context hygiene** -- shipped: threshold-gated skill stub + `search_skills`,
   KB count/byte caps, persist-time tool-output cap, durable-pin bin.
   Lazy loading already exists (`internal/agent/agent.go:1109,1215`); this
   scales it past ~100 skills without changing under-threshold behavior.
   Design: `docs/context-hygiene/` (CX-001..CX-004).
3. **Permissions policy** — `permissions.json` allow/ask/deny per
   tool+path (opencode triples + Claude deny>ask>allow), user < project
   (trust-gated) < enterprise layering, mobile approval queue endpoint,
   hash-chained audit export. Builds on gates + content-hash trust already
   shipped. Design: `docs/permissions/` (PM-001..PM-004).
4. **MCP gateway** — shipped: registry search (`hakase mcp search/install`),
   `mcp audit` (inventory, shadow drift, poisoning scan, auth posture, sandbox,
   provenance), scoped-token UX (`mcp logout`), gateway meta-tools
   (`mcp_search_tools`, `mcp_describe_tool`, `mcp_call_tool`) with auto-degrade
   over 40-tool budget. Reuses `UpsertServer`/`Reconnect`/`Diagnose`.
   Design: `docs/mcp-gateway/` (MG-001..MG-006).
5. **ADK adoption (phased enabler)** — replace bespoke delegation runner
   with `agenttool.New` first (H value / L-M effort), then one workflow
   pilot, then artifacts + `plugin.Plugin`; skills toolset + A2A deferred.
   Cloud backends stay opt-in only. Design: `docs/adk-adoption/`
   (AD-001..AD-005).

## Deferred ledger (intentionally not scheduled)

| Item | Where recorded | Note |
|---|---|---|
| ComfyUI media provider (v2) | `docs/media-generation/spec.md` ("Deferred to v2") | design preserved; house-pattern conventions locked |
| Seekable video streaming (HTTP Range) | `docs/markdown-rendering/` | whole-file serving only today |
| Telegram group chats + webhooks | `internal/channel/telegram` package doc | forum topics cover the 1:1 flow end to end |
| `dream_consolidate` memory trials | `internal/sleep/cycle.go`; `docs/skillopt-sleep/plan.md` | reserved surface from Phase 3 |
| Landlock enforcement (Phase 3) | [#14](https://github.com/amurru/hakase/issues/14) (closed: fail-loud shipped, mode refused) | `sandbox.mode: landlock` is refused at config load and exec time until a real LSM enforcement lands: ruleset for FS read/write/exec per workspace/read/deny roots, ABI-version detection, clear error when the kernel lacks support; see the Proposed solution in #14 |
| Web UI voice input (mic capture → speech pipeline) | `docs/telegram-voice/` deferred section | Telegram voice notes work end-to-end and `internal/speech` is transport-neutral; the web UI has no recording capture yet |
| Project-scope hooks + content-hash trust store | `docs/hooks/spec.md` Phase 2; shipped this arc | project `.hakase/hooks.json` loads layered under user config with Codex-style content-hash trust + explicit-accept gate, sandbox write-deny, and `SessionStart`. Gemini CLI's fingerprint-and-approve remains the documented anti-pattern (gemini-cli#27900) |

## Maintenance conventions

1. Pick an issue → write `docs/<feature>/spec.md` + `plan.md` + `tasks.md`
   following the existing feature directories.
2. Tick `tasks.md` in the same PR that lands the work — drift is how
   `docs/media-generation/tasks.md` ended up 21 boxes behind a shipped
   feature (reconciled 2026-09-19).
3. New deferrals go to the CHANGELOG "Planned" section **and** the deferred
   ledger above.
4. Close an issue only when `tasks.md`, the docs, and the code agree.
