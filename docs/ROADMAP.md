# Roadmap

Last updated: 2026-09-20. Living index of planned work. The GitHub issues are
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
generation, SkillOpt-Sleep phases 0–4.

## Tier 1 — Catch up to the 2026 harness landscape

| Issue | What | Ecosystem driver |
|---|---|---|
| [#16](https://github.com/amurru/hakase/issues/16) | Agent-written auto-memory (typed notes, session-start injection) | Claude Code's key differentiator; compounds with SkillOpt-Sleep's miner |
| [#17](https://github.com/amurru/hakase/issues/17) | MCP 2026-07-28 upgrade (go-sdk v1.7+): elicitation → approval/clarify gates, CIMD OAuth, `skill://` serving | largest spec revision to date; hakase's gate system makes elicitation a differentiator |
| [#18](https://github.com/amurru/hakase/issues/18) | OpenTelemetry GenAI tracing from graph events + audit log | converged observability interchange; maps ~1:1 onto existing `GraphEvent`s |
| [#19](https://github.com/amurru/hakase/issues/19) | Session checkpoints / restore-to-message rewind | proven UX (Gemini CLI); cheap given session-per-JSON + the MessageRail |
| [#20](https://github.com/amurru/hakase/issues/20) | Telegram voice in/out (local whisper.cpp + optional Piper TTS) | explicitly deferred today; proven fully-local pattern |

## Tier 2 — Bigger bets (pick one or two)

- [#21](https://github.com/amurru/hakase/issues/21) — backlog with per-item
  notes: hakase-as-MCP-server, hybrid embeddings retrieval, hooks system,
  second channel transport (Discord/Slack), durable HITL resume (ADK Go 2.x
  graph engine), audio generation.
- `docs/git-tools/tasks.md` T7.x remainder: `git_remote` management,
  `git_rebase`/`git_merge`, `git_commit --amend`/signing.

## Deferred ledger (intentionally not scheduled)

| Item | Where recorded | Note |
|---|---|---|
| ComfyUI media provider (v2) | `docs/media-generation/spec.md` ("Deferred to v2") | design preserved; house-pattern conventions locked |
| `generate_audio` | CHANGELOG Planned; [#21](https://github.com/amurru/hakase/issues/21) | stub returns an actionable error; config already validates providers |
| Seekable video streaming (HTTP Range) | `docs/markdown-rendering/` | whole-file serving only today |
| Telegram group chats + webhooks | `internal/channel/telegram` package doc | forum topics cover the 1:1 flow end to end |
| `dream_consolidate` memory trials | `internal/sleep/cycle.go`; `docs/skillopt-sleep/plan.md` | reserved surface from Phase 3 |
| Landlock enforcement (Phase 3) | [#14](https://github.com/amurru/hakase/issues/14) (closed: fail-loud shipped, mode refused) | `sandbox.mode: landlock` is refused at config load and exec time until a real LSM enforcement lands: ruleset for FS read/write/exec per workspace/read/deny roots, ABI-version detection, clear error when the kernel lacks support; see the Proposed solution in #14 |

## Maintenance conventions

1. Pick an issue → write `docs/<feature>/spec.md` + `plan.md` + `tasks.md`
   following the existing feature directories.
2. Tick `tasks.md` in the same PR that lands the work — drift is how
   `docs/media-generation/tasks.md` ended up 21 boxes behind a shipped
   feature (reconciled 2026-09-19).
3. New deferrals go to the CHANGELOG "Planned" section **and** the deferred
   ledger above.
4. Close an issue only when `tasks.md`, the docs, and the code agree.
