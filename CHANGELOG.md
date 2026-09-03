# Changelog

All notable changes to hakase are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
During the alpha phase (0.x), breaking changes may land in any minor release;
the web UI and `config.json` formats aim for backward compatibility but are not
guaranteed stable until 1.0.

## [Unreleased]

### Added

- **Structured git tools** - `git_status`, `git_diff`, `git_log`, `git_branch` (read-only, no approval) and `git_stage`, `git_commit` (mutating) for the orchestrator and general-purpose agent. Every tool runs `git` through the same harmful-command policy, interactive approval gate, path audit, env scrubbing, and audit log as `system_exec` (`status`/`diff`/`log`/`branch` classify LOW/allow; `add`/`commit` MEDIUM/ask). Repo directories are sandbox-confined (write containment for mutations), output is bounded (256 KiB combined, 20k diff lines) and wrapped as untrusted data, and `GIT_TERMINAL_PROMPT=0` prevents hanging on credential prompts. Push/pull/clone/checkout/reset/clean are deferred to v2 - see [docs/git-tools/](docs/git-tools/).
- **Project-aware sessions** - the session is anchored to a project identity (the git root under the launch cwd, resolved by the new `internal/project` package; skill/AGENTS.md discovery already walked to the same root). The orchestrator and general-purpose agents receive a compact `GIT WORKSPACE` snapshot in their instructions at session start - branch and upstream tracking, staged/modified/untracked/conflicts counts, and the three newest commits - marked as a snapshot to re-check with `git_status`/`git_diff`. Git tools default `repo_dir` to the project root (after any explicitly pinned sandbox workspace), and the snapshot's token cost folds into the context-compaction reserve. Workspace (confinement) and project (identity) stay separate concepts; a registry + clone/push model for remote web deployments is deferred to v2 - see [docs/git-tools/project.md](docs/git-tools/project.md).
- **Remote git operations** - `git_clone`, `git_push`, and `git_pull` (the v2.1 remote slice) join the structured git toolset for the orchestrator and general-purpose agent. Clone sources are scheme-allowlisted (https/http/git/ssh; `file://` or local paths only when no sandbox is active, since they bypass the sandbox read roots) and targets resolve through write containment. Push exposes `--set-upstream` but never force-flags (that stays a HIGH-risk system_exec concern); pull is always `--ff-only` so an agent can never silently create merge commits. All three are MEDIUM-risk approval-gated network operations that run through the same policy/audit pipeline as every other git tool - see [docs/git-tools/](docs/git-tools/).
- **Destructive git operations** - `git_checkout`, `git_reset`, and `git_clean` (the v2.2 slice) complete the structured git toolset (12 tools total). Checkout switches branches (`-b` create) or restores one path from the index and never passes force flags - git itself refuses switches that would clobber uncommitted changes. Reset exposes soft/mixed/hard with revision-validated refs; `--hard` flows through verbatim so the existing gate classifies it HIGH and the approval card shows the real command. Clean always removes only untracked data (`-f`, or `-n` dry-run), with `-d`/`-x` mapped onto the existing HIGH combinations and an optional path filter. All three are approval-gated through the same policy/audit pipeline - see [docs/git-tools/](docs/git-tools/).
- **Project registry + materialization CLI** - `internal/registry` stores registered remote projects in `projects.json` under the hakase home (CRUD with id/name uniqueness and a source allowlist that mirrors git_clone: https/http/git/ssh, plus `file://` for local bare remotes). `internal/project` project identity became context-scoped (`WithRoot`/`RootFrom`, process-root fallback) so a server hosting several checkouts can resolve each run against its own project (DP-7). On top of it, `registry.Service` + `hakase projects list|register|sync|delete` materialize projects into `<hakase home>/projects/<id>` through the git_clone/git_pull engine under operator authority (DP-11, no second exec surface): register clones (cloning → ready, `sync_error` on failure with a retryable entry), sync fast-forwards (`--ff-only`; a diverged or dirty tree fails into `sync_error` without ever deleting local work), delete removes the checkout and entry and never touches the remote. Checkout paths derive from the store location, so a temp-home registry keeps its checkouts in its own tree. Local TUI/local-web behavior is unchanged - the registry is additive, targeting remote-web deployments - see [docs/git-tools/project-registry.md](docs/git-tools/project-registry.md).
- **Project registry web API + session binding** - remote-web deployments can now drive the registry from the UI: `GET/POST /api/projects`, `POST /api/projects/{id}/sync` and `DELETE /api/projects/{id}` under the existing auth middleware (register clones synchronously; a failed register/sync is returned as a `sync_error` entry with the bounded git error so the UI can retry via sync). Sessions gained a `project_id`/`project_name` binding: `POST /sessions` accepts `project_id` and validates it against the registry (ready checkout only), and the session list/detail responses expose the binding. Project-bound chat runs anchor to the project checkout (DP-7): the run context carries the checkout as the project root so git tools - including delegated runs - default there, and a fresh per-session `GIT WORKSPACE` snapshot is injected at the start of each bound run. Unbound sessions keep the process sandbox as-is; project-bound sessions on sandboxed hosts derive a per-session sandbox pinned to their checkout (see the per-run sandbox pinning bullet below). See [docs/git-tools/project-registry.md](docs/git-tools/project-registry.md).
- **Project registry UI + remote-deployment docs** - the New Session dialog lists ready registered projects (from `/api/projects`) so a session can be bound to a project checkout at creation; bound sessions are tagged with a git-branch chip in the session list and the chat header, and choosing one jumps straight into the bound conversation. The README's Production Deployment section now covers registered projects for remote-web hosts: registration (`hakase projects` / the API), session binding, and the DP-8 credentials note (clone/push/pull use the host's own git auth - credential helpers, `gh`, SSH agents - nothing is stored in `projects.json`).
- **Per-run sandbox pinning for project-bound sessions** - closes the DP-7 confinement gap: when the host sandbox is active, a registered-project session's git, file, and exec operations are confined to the project checkout instead of the process-wide workspace. `internal/sandbox` gained context-scoped sandbox plumbing (`WithConfig`/`ConfigFrom` - a per-run override consulted before the boot `CurrentSandbox` - and `PinnedTo`, which copies the active sandbox with workspace/read roots replaced by the checkout; sandbox-off stays off). The ctx-aware resolver is used by file ops (`taskResolve`), the git engine (`resolveRepoDir`/`runGit` via the new `BuildExecCommandFor`), and the `system_exec` handlers; web chat applies it in `runAgentTask` for bound runs, and delegated sub-runs inherit it through the run context. Unbound/local sessions are unchanged.

### Planned

- `generate_audio` implementation (currently a stub wired for v2)
- `landlock` sandbox mode (in-process Landlock + seccomp confinement, Phase 3)
- `git stash`/`rebase`/`merge`, `commit --amend` / signing, `git remote`/`tag` management - [docs/git-tools/](docs/git-tools/)
- ComfyUI / TouchDesigner / Suno native integrations (see README TODO)

## [0.1.0-alpha.4] - 2026-08-31

### Added

- **Native Windows port (Path A)** - hakase now builds and runs natively on Windows (`GOOS=windows`, no WSL2 required). Shell routing uses `cmd /D /C`, executable lookup is PATH-only with `NoDefaultCurrentDirectoryInExePath=1` and workspace-hijack rejection, the Python interpreter probes `py -3` then `python` with `.venv\Scripts` layout, file locking uses `LockFileEx`, process trees are tracked via Job Objects (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`), and Win32 path aliases (trailing dots/spaces, ADS streams, `\\?\`/`\\.\` namespaces, drive-relative `C:foo`, 8.3 short names, `%VAR%`/`!VAR!` expansion including whitespace names) are rejected or canonicalized via `GetFinalPathNameByHandle`. Bubblewrap/landlock modes coerce to `paths` with a warning on Windows. A `windows-latest` CI job runs the full test suite and a web boot smoke test; `make build-windows` cross-compiles `hakase.exe` and assembles the unsigned `dist/hakase-<version>-windows-amd64.zip` (see Windows notes in the README).
- **Any-browser MCP presets** - `docs/browser-mcp-presets.md` ships four copy-pasteable MCP server presets for the `web_researcher` (Lightpanda, `chrome-devtools-mcp` on Edge, `@playwright/mcp`, `@browsermcp/mcp`) with `tools.include`/`tools.exclude` shaping and a runtime tradeoff matrix. The browsing stack is a config-only swap on Linux and Windows alike.
- **Web UI slash-command palette** - typing `/` in the web chat input opens an autocomplete popup (arrow keys navigate, Tab/Enter complete, Esc dismisses) exposing the TUI commands except `/exit`: `/compact [focus]` (new `POST /api/sessions/{id}/compact` endpoint running the same snip + async summary cascade), `/sidekick <question>`, `/new`, `/sessions`, `/board`, `/mcp`, `/help`. Unknown `/tokens` pass through as ordinary text.
- **Sidekick agent** - a second, independently-configured LLM that answers direct questions (`/sidekick <question>` in the TUI or typed in the web chat) or quietly watches a run and surfaces advisory notes as quiet inline chips. On-demand asks are framed with a bounded recent-conversation transcript (`transcript_window_chars`, tail-biased) so follow-up questions have context. Explicit interactions are persisted to the session file under `"kind": "sidekick"` (answers carry `"role": "sidekick"`) so provenance is auditable. Modes `off` / `on_demand` / `watch` / `full`; no notification dispatch; local `openai-compatible` models keep conversation excerpts on-machine. See the README Sidekick section.

### Fixed

- Windows sandbox audit now rejects `%VAR%` and `!VAR!` (including `!X Y!`) expansion tokens before `cmd.exe` expands them (CWE-22).
- Python `pip` spawns use bounded waits: on cancellation the whole process tree is killed before `Wait` so output pipes inherited by descendants cannot block forever.
- Tightened several Windows correctness and docs issues flagged in CodeRabbit reviews (Pdeathsig assertion, symlink test scoping, `USERPROFILE`/`HOME` alignment in skill discovery tests, zip fallback, shell-routing docs).

## [0.1.0-alpha.3] - 2026-08-24

### Added

- **Linux packages (.deb / .rpm)** built via [nfpm](https://nfpm.goreleaser.com)
  from a single description (`packaging/nfpm.yaml`); published on GitHub
  releases alongside the binary, covered by `SHA256SUMS.txt` and SLSA
  provenance. Prerelease versions are encoded with `~`
  (`0.1.0~alpha.3`) so dpkg/rpm sort them before the final release.
  Local builds: `make package-deb` / `package-rpm` / `package-linux`.

## [0.1.0-alpha.2] - 2026-08-23

### Added

- **AUR prebuilt-binary package** (`packaging/aur/hakase-bin/PKGBUILD`):
  installs the released linux/amd64 binary as `/usr/bin/hakase` with docs;
  `provides`/`conflicts` on `hakase` so a future source-build package can
  coexist cleanly.

### Changed

- Release binaries are now built with `CGO_ENABLED=0` (fully static,
  distro-independent) - required by the `-bin` package, safer everywhere.

## [0.1.0-alpha.1] - 2026-08-23

First testing release. Linux-only; developed and tested on Arch Linux.

### Added

- **Release pipeline with SLSA L3 provenance** (`.github/workflows/release.yml`):
  pushing a `v*` tag builds the production binary via the Makefile on a
  GitHub-hosted runner, creates the release with `SHA256SUMS.txt`, and attaches
  keylessly signed SLSA Build L3 provenance (`<binary>.intoto.jsonl`) via the
  SLSA generic generator reusable workflow (pinned to `@v2.1.0`; all other
  actions pinned by commit SHA). Verifiable with `slsa-verifier` - see the
  README's "Verifying release binaries" section.

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

[Unreleased]: https://github.com/amurru/hakase/compare/v0.1.0-alpha.4...HEAD
[0.1.0-alpha.4]: https://github.com/amurru/hakase/compare/v0.1.0-alpha.3...v0.1.0-alpha.4
[0.1.0-alpha.3]: https://github.com/amurru/hakase/releases/tag/v0.1.0-alpha.3
[0.1.0-alpha.2]: https://github.com/amurru/hakase/releases/tag/v0.1.0-alpha.2
[0.1.0-alpha.1]: https://github.com/amurru/hakase/releases/tag/v0.1.0-alpha.1
