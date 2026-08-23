# Changelog

All notable changes to hakase are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
During the alpha phase (0.x), breaking changes may land in any minor release;
the web UI and `config.json` formats aim for backward compatibility but are not
guaranteed stable until 1.0.

## [Unreleased]

### Added

- **Release pipeline with SLSA L3 provenance** (`.github/workflows/release.yml`):
  pushing a `v*` tag builds the production binary via the Makefile on a
  GitHub-hosted runner, creates the release with `SHA256SUMS.txt`, and attaches
  keylessly signed SLSA Build L3 provenance (`<binary>.intoto.jsonl`) via the
  SLSA generic generator reusable workflow (pinned to `@v2.1.0`; all other
  actions pinned by commit SHA). Verifiable with `slsa-verifier` - see the
  README's "Verifying release binaries" section.

### Planned

- `generate_audio` implementation (currently a stub wired for v2)
- `landlock` sandbox mode (in-process Landlock + seccomp confinement, Phase 3)
- ComfyUI / TouchDesigner / Suno native integrations (see README TODO)

## [0.1.0-alpha.1] - 2026-08-23

First testing release. Linux-only; developed and tested on Arch Linux.

### Added

- **Terminal TUI** (Bubble Tea): split-pane chat/log layout, slash commands
  (`/board`, `/mcp`, `/compact`, `/new`, `/sessions`, `/help`), mid-run
  messaging with queued interjections, clarify questions, approval modals,
  help overlay, `@file` picker attachments, clipboard image paste, inline
  LaTeX math rendering (kitty graphics protocol with Unicode fallback),
  Herdr pane awareness.
- **Web UI** (Vue 3 + TypeScript + Vite + Tailwind 4 SPA served by a chi HTTP
  server): chat with SSE streaming, markdown/KaTeX/Mermaid/syntax-highlight
  rendering, sessions, task board, knowledge base, skills, MCP servers, cron,
  files, settings; approval & clarify gates work in the browser too.
  API-only mode via `hakase serve`; reverse-proxy (Caddy) deployment docs.
- **Authentication**: argon2id credentials (`hakase auth set-password`),
  JWT cookie/bearer auth, login rate limiting, secret files implicitly denied
  to the agent.
- **Multi-agent orchestration** (Google ADK v2): orchestrator delegating to
  `web_researcher`, `code_interpreter`, and `general_purpose` sub-agents;
  providers Gemini / OpenAI / OpenAI-compatible with fallback chains.
- **Tools**: MCP client (multi-server stdio + streamable HTTP, runtime
  enable/disable/reconnect), Python code interpreter with auto dependency
  resolution, sandboxed file ops (`read_file`/`write_file`/`patch`/
  `search_files`), `system_exec` with shell routing and timeouts, downloads,
  vision (direct or described via a configured vision model), media generation
  (`generate_image`, `generate_video`; OpenAI/OpenRouter/fal.ai plus offline
  PIL-style fallback; `generate_audio` stub).
- **Sandboxing**: path confinement on by default (read/work/deny roots,
  symlink-escape protection, command-path auditing), optional bubblewrap
  kernel isolation with env scrubbing; `landlock` reserved.
- **Knowledge base**: wiki-style markdown notes with YAML frontmatter and
  `[[wikilinks]]`, eight knowledge tools, relevance-ranked search with
  optional HyDE-lite query expansion, auto-index/log maintenance, search
  benchmark (`hakase knowledge bench`), reflexion lessons-learned recall.
- **Skills**: markdown skills (agentskills.io-compatible discovery across
  project/user locations), Python skill library, darwinian-evolver-style
  self-evolution loop with A/B promotion gate and auditable reports, bundled
  research/creative skill ports (domain-intel, osint-investigation,
  drug-discovery, bioinformatics, scrapling, latex-math, hakase self-skill).
- **Scheduling**: cronjob tool and background scheduler (cron/interval/ISO
  schedules) with `hakase cron list|status|pause|resume|run|tick`.
- **Context management**: AGENTS.md project-context loading with progressive
  subdirectory injection, live reconcile, prompt-injection scanning, token-
  budgeted truncation; compaction cascade with manual `/compact`;
  configurable summary/vision models; loop guard anti-degeneration guardrails.
- **Runtime awareness**: environment snapshot block (OS/distro, package
  manager, toolchains, disk/memory) with staleness refresh; preferred
  measurement units (metric/imperial); `hakase rules`, `hakase env`,
  `hakase session`, `hakase task`, `hakase skill` management CLIs.
- **Release engineering** (this release): `hakase version` reporting the
  ldflags-stamped version/commit/date (`--short` for scripts), Makefile
  version stamping from `git describe` (`make VERSION=vX.Y.Z` to override),
  and this changelog.

[Unreleased]: https://github.com/amurru/hakase/compare/v0.1.0-alpha.1...HEAD
[0.1.0-alpha.1]: https://github.com/amurru/hakase/releases/tag/v0.1.0-alpha.1
