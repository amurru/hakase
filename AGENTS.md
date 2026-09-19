# AGENTS.md

Go 1.26 agent harness (module `amurru/hakase`) with a Vue 3 web UI. No CI or lint config exists; verification is `go build ./...` + `go test ./...` and `pnpm test` in `webui/`.

## Critical setup gotcha

`internal/web/dist/` is gitignored but required at compile time by `//go:embed all:dist` (internal/web/embed_prod.go). On a fresh clone, `go build ./...` and `go test ./...` fail for `internal/web` until you run:

```
make build-frontend
```

(after that, the mirror exists and plain Go commands work). `make clean` removes it again.

## Commands

- `make build` - full production binary: frontend build + `go build -tags prod -o hakase ./cmd/hakase/`
- `make build-frontend` - `pnpm install && pnpm build` in `webui/`, then copy `webui/dist/` into `internal/web/dist/` (go:embed cannot follow symlinks, hence the real copy)
- `make test` - `go test ./...`
- Single Go test: `go test ./internal/agent/ -run TestName`
- Frontend tests: `cd webui && pnpm test` (vitest, jsdom). Single file: `cd webui && pnpm vitest run src/composables/useMermaid.test.ts`
- `pnpm build` runs `vue-tsc -b` first, so the typecheck is part of the build

## Build tags (internal/web)

- default (`!dev`): SPA embedded from `internal/web/dist`
- `dev` tag: SPA served live from `webui/dist` on disk - no Go rebuild for frontend changes

## Development flow (web UI)

Two terminals; open http://localhost:5173 (Vite proxies `/api` to the Go server on :8080):

```
make dev-frontend   # Vite dev server, HMR, port 5173
make dev-backend    # go run -tags dev ./cmd/hakase/ web
```

## Layout

- `cmd/hakase/` - the only entry point. Root has no .go files; do not add Go files to the repo root.
- `internal/` - all packages: `agent` (ADK orchestration, delegation, gates, providers), `agentrun` (transport-neutral single-turn driver shared by web chat and channels), `auth` (argon2id credentials + JWT), `channel` (communication-channel subsystem: pairing/auth, per-chat run state, bridge event router, formatting; `channel/state` is the `~/.hakase/channels.json` leaf store, `channel/telegram` the Telegram transport), `cli` (subcommand dispatcher), `config`, `context` (compaction/summarization), `env`, `herdr`, `interfaces` (shared gate/notifier contracts), `knowledge`, `mcp`, `sandbox`, `session`, `skill`, `tui`, `util`, `vision`, `websearch` (keyless `web_search`/`web_fetch` fallback shown when no research MCP is connected), `web` (chi HTTP server, handlers, SSE bridge, SPA embed).
- `webui/` - Vue 3 + TypeScript + Vite + Tailwind 4 SPA. Uses **pnpm** (workspace file present), not npm/yarn.
- `.agents/skills/` - markdown skills shipped with the repo (committed).

## Channels (Telegram)

- The Telegram bot runs INSIDE the `web`/`serve` process (started in `cmd/hakase/web.go` when `channels.telegram.enabled` + `bot_token` are set, or `HAKASE_TELEGRAM_*` env). It reuses the process's runner, web gates, SSE bridge, and session service - approvals/clarifications can be answered from the web UI or the phone, first responder wins.
- Transport-neutral logic lives in `internal/channel`; transports implement `channel.Channel` + `channel.PushHandler` and register with `channel.Service`. Pairing state persists in `~/.hakase/channels.json` (0600, flock, sandbox-denied).
- `internal/channel` may import `internal/cli` (cron wrappers) and `internal/web/sse` (event bus), but `internal/cli` must never import `internal/channel` - only the leaf `internal/channel/state` (see the `channels` subcommand in `internal/cli/channels.go`).
- Inbound prompts go through `agentrun.Driver` (extracted from the web chat handler): same sandbox, project binding, tool-call repair, and persistence as browser runs. One run per chat; `/stop` cancels it.

## Execution canvas (web UI)

> WARNING: this file is rendered into the orchestrator's instruction block, and
> ADK treats a brace-quoted identifier (like the URL placeholder this file once
> had for session endpoints) as session-state templating — a missing key fails
> every run with "state key does not exist". Never write a braced identifier
> here; write URL placeholders as `<id>`, not the braced form.

- The chat view's toggleable graph panel is driven by the structured `graph` SSE event: typed `interfaces.GraphEvent` frames (`agent_start/agent_end/tool_start/tool_end/agent_text/agent_thought/transfer`) with per-session monotonic `seq` assigned by the bridge. Emitters: the `agentrun` driver loop (root node, tool calls with args/results/durations, ADK `transfer_to_agent`) and `internal/agent`'s delegation reporter (sub-agent nodes, keyed to the parent run via `interfaces.TaskIDFromCtx`).
- `agentrun.EventSink` carries `OnGraphEvent`, so every transport sink implements it: the web `bridgeSink` publishes under the session topic, the Telegram `runView` mirrors to the bridge (phone-started runs appear on the web canvas), the TUI drops them (it has `DelegationProgress`).
- The SSE bridge retains the last 500 canvas events per session (in-memory ring, `internal/web/sse/graph.go`); `GET /api/sessions/<id>/graph` backfills the frontend after reload/reconnect and session delete prunes it. Nothing is persisted to disk.
- Frontend: pure reducer/layout in `webui/src/lib/canvas.ts`, state in `stores/canvas.ts`, components under `webui/src/components/canvas/` (Vue Flow, lazy-loaded chunk).

## Wiring gotchas

- `web`/`serve` are intercepted in `cmd/hakase/main.go` BEFORE `cli.Dispatch`. The `web`/`serve`/`tui` entries registered inside `internal/cli/command.go` are stubs (`notMigrated`/placeholder); the real TUI launches only when no subcommand is given.
- The web/serve bootstrap (`cmd/hakase/web.go`) must live in package main: `internal/web/handlers` imports `internal/cli`, so a shared bootstrap package would create an import cycle.
- `cmd/hakase/main.go` wires `agent.Deps` with bridge factories (MCP manager, skill discovery, knowledge tools, cron) to keep `internal/agent` decoupled from those packages; new agent-facing cross-package capabilities usually need a factory added there.
- `internal/sleep` (SkillOpt-Sleep offline skill-evolution loop, `docs/skillopt-sleep/plan.md`) may import `skill/session/knowledge/config/sandbox/util` but NEVER `internal/agent` - the model seams are function-injected (`ModelCaller`/`TargetRunner`/`RubricJudge`/`Reflector`); the headless CLI layer (`internal/cli/sleep.go`, `cron_sleep.go`) wires them to `agent.ModelPromptFn`. Mined task files carry a `generated_at` marker and are refused by real-backend consumers (`evolve-md`, `sleep run`) until `hakase sleep review` signs a sidecar; redaction is flag-only (`redact_secrets:false` is refused in config and env, `HAKASE_SLEEP_*REDACT*` hard-errors). Native cron `sleep` jobs are CLI-only (`hakase sleep schedule`); the cronjob tool denies them per SL-006. Runtime artifacts: `outputs/sleep/` (gitignored), `.hakase/sleep-state.json` (+ `.lock`, gitignore `.hakase/`).

## Testing quirks

- Go tests live next to sources (`*_test.go`), are self-contained (temp dirs, `isolateHome` redirects `$HOME`/`XDG_CONFIG_HOME`), and need no network, config.json, or MCP servers.
- Tests write `logs/exec-audit.jsonl` under `cmd/hakase/` and `internal/agent/`; these `logs/` dirs are runtime artifacts, gitignored via `logs/` - do not commit them.
- Runtime/generated (all gitignored): `config.json`, `tasks.json`, `sessions/`, `outputs/`, `downloads/`, `.venv/`, `.hakase-tmp/`, `webui/dist/`, `internal/web/dist/`, root `hakase` binary, `~/.hakase/channels.json` (+ `.lock`).
