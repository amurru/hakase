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
- Frontend tests: `cd webui && pnpm test` (vitest, jsdom). Single file: `cd webui && pnpm vitest run src/lib/markdown/useMermaid.test.ts`
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
- `internal/` - all packages: `agent` (ADK orchestration, delegation, gates, providers), `auth` (argon2id credentials + JWT), `cli` (subcommand dispatcher), `config`, `context` (compaction/summarization), `env`, `herdr`, `interfaces` (shared gate/notifier contracts), `knowledge`, `mcp`, `sandbox`, `session`, `skill`, `tui`, `util`, `vision`, `web` (chi HTTP server, handlers, SSE bridge, SPA embed).
- `webui/` - Vue 3 + TypeScript + Vite + Tailwind 4 SPA. Uses **pnpm** (workspace file present), not npm/yarn.
- `.agents/skills/` - markdown skills shipped with the repo (committed).

## Wiring gotchas

- `web`/`serve` are intercepted in `cmd/hakase/main.go` BEFORE `cli.Dispatch`. The `web`/`serve`/`tui` entries registered inside `internal/cli/command.go` are stubs (`notMigrated`/placeholder); the real TUI launches only when no subcommand is given.
- The web/serve bootstrap (`cmd/hakase/web.go`) must live in package main: `internal/web/handlers` imports `internal/cli`, so a shared bootstrap package would create an import cycle.
- `cmd/hakase/main.go` wires `agent.Deps` with bridge factories (MCP manager, skill discovery, knowledge tools, cron) to keep `internal/agent` decoupled from those packages; new agent-facing cross-package capabilities usually need a factory added there.

## Testing quirks

- Go tests live next to sources (`*_test.go`), are self-contained (temp dirs, `isolateHome` redirects `$HOME`/`XDG_CONFIG_HOME`), and need no network, config.json, or MCP servers.
- Tests write `logs/exec-audit.jsonl` under `cmd/hakase/` and `internal/agent/`; these `logs/` dirs are runtime artifacts, gitignored via `logs/` - do not commit them.
- Runtime/generated (all gitignored): `config.json`, `tasks.json`, `sessions/`, `outputs/`, `downloads/`, `.venv/`, `.hakase-tmp/`, `webui/dist/`, `internal/web/dist/`, root `hakase` binary.
