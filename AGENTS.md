# AGENTS.md

Go 1.26 agent harness (module `amurru/hakase`) + Vue 3 SPA in `webui/`. CI `.github/workflows/test.yml` runs `gofmt -l`, `go vet`, a darwin cross-compile, `go test ./...`, and `pnpm --dir webui test` on every push/PR - run the same locally before pushing.

## Fresh-clone gotcha

`internal/web/dist/` is gitignored but required at compile time by `//go:embed all:dist` in `internal/web/embed_prod.go`. On a fresh clone `go build` and `go test` fail for `internal/web` until you run `make build-frontend`. `make clean` removes the mirror again.

## Verify before pushing

```
make build-frontend
unformatted=$(gofmt -l .); test -z "$unformatted"
go vet ./...
GOOS=darwin go build ./...
go test ./...
pnpm --dir webui test
```

## Commands

- `make build` - full prod binary: frontend + `go build -tags prod -o hakase ./cmd/hakase/`
- `make build-frontend` - `pnpm install && pnpm build` in `webui/`, then real copy into `internal/web/dist/` (go:embed cannot follow symlinks)
- `make test` - `go test ./...`
- Single Go test: `go test ./internal/agent/ -run TestName`
- Single frontend file: `cd webui && pnpm vitest run src/composables/useMermaid.test.ts`
- `pnpm build` runs `vue-tsc -b` first, so the typecheck is part of the build
- Dev flow, two terminals: `make dev-frontend` for Vite HMR on 5173, `make dev-backend` for `go run -tags dev ./cmd/hakase/ web` on 8080; open 5173, Vite proxies `/api` to 8080

## Layout

- `cmd/hakase/` - only entry point. No Go files at repo root.
- `internal/agent` - ADK runner, sub-agents, providers, gates; `internal/agentrun` - transport-neutral single-turn driver shared by web chat and channels; `internal/web` - chi server, handlers, SSE bridge, SPA embed; `internal/channel` - channel subsystem, `channel/state` leaf store, `channel/telegram` and `channel/discord` transports. Other packages follow their names: `auth`, `cli`, `config`, `sandbox`, `session`, `skill`, `sleep`, `knowledge`, `mcp`, `tui`, `vision`, etc. See `docs/DEVELOPMENT.md` for the full map.
- `webui/` - Vue 3 + TypeScript + Vite + Tailwind 4 SPA. Uses pnpm, never npm/yarn.
- Skills: `.agents/skills/` is committed markdown skills shipped with the repo; `skills/` at root is the runtime Python skill library. Do not confuse them.
- Build tags on `internal/web`: default prod embeds `internal/web/dist`; `dev` tag serves `webui/dist` live from disk.

## Wiring rules

- `web`/`serve`/`tui` dispatch through `cli.Dispatch`. Real handlers live in package main (`cmd/hakase/web.go`, `main.go`) and are injected via `cli.RegisterCommand`; `internal/cli/command.go` holds only fallbacks for tests.
- The web/serve bootstrap must stay in package main: `internal/web/handlers` imports `internal/cli`, so any shared bootstrap package creates an import cycle.
- `cmd/hakase/main.go` wires `agent.Deps` with bridge factories for MCP, skills, knowledge, cron, media. New cross-package agent capabilities need a factory there to keep `internal/agent` decoupled.
- Import direction: `internal/cli` must never import `internal/channel` except the leaf `internal/channel/state`; `internal/agent` must never import `internal/sleep`. The sleep replay harness legitimately imports agent, not the reverse.
- Sandbox: `landlock` mode is refused on every platform; `bubblewrap` is Linux-only and coerces to `paths` with a warning on Windows.
- Config: `config.json` is gitignored runtime state, copy from `config.json.example`. `HAKASE_*` env vars override file values; `HAKASE_HOME` relocates `~/.hakase`.

## Channels

- Telegram and Discord bots run INSIDE the `web`/`serve` process, sharing its runner, gates, bridge, and sessions. Approvals can be answered from web UI or phone, first responder wins.
- When any channel is enabled, approval/clarify expiry is clamped up to a 300s floor so phone round-trips stay answerable.
- Pairing state is `~/.hakase/channels.json`, mode 0600, sandbox-denied. Manage via web Channels page or `hakase channels status|pair-code|revoke`.

## Execution canvas

> WARNING: this file is rendered into the orchestrator instruction block, and ADK treats a brace-quoted identifier as session-state templating - a missing key fails every run. Never write a braced identifier here; write URL placeholders as `<id>`, not the braced form.

- Chat graph panel is driven by the structured `graph` SSE event with per-session monotonic `seq` from the bridge; emitters are the `agentrun` driver loop and the delegation reporter in `internal/agent`. Bridge keeps the last 500 events per session for backfill via `GET /api/sessions/<id>/graph`, nothing persisted.
- Frontend canvas code: pure reducer in `webui/src/lib/canvas.ts`, state in `stores/canvas.ts`, components under `webui/src/components/canvas/`.

## Testing quirks

- Go tests are hermetic: live next to sources, use temp dirs, redirect `HOME` and `XDG_CONFIG_HOME` via `isolateHome`, no network, no `config.json`, no MCP servers.
- Tests write `logs/exec-audit.jsonl` under `cmd/hakase/` and `internal/agent/`; `logs/` dirs are gitignored runtime artifacts, never commit them.
- Never commit runtime artifacts: `config.json`, `tasks.json`, `sessions/`, `outputs/`, `downloads/`, `.venv/`, `.hakase-tmp/`, `webui/dist/`, `internal/web/dist/`, root `hakase` binary, `.hakase/` state.

## Workflow

- Branch off `develop`, PRs target `develop`. One logical change per PR.
- Issues first: every unit of work has an issue with file-colon-line evidence. Substantial features follow the house convention `docs/<feature>/` with spec plus plan plus tasks files.
- Commits follow Conventional Commits (`feat(scope): ...`, `fix(scope): ...`, etc.) and reference issues with `Closes #N`.
- Jules tasks: Jules reads this file automatically. For PR work, also read `.agents/prompts/jules.md` and follow it - fetch inline review comments first and treat unresolved findings as in-scope.
