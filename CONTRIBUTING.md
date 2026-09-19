# Contributing to Hakase

Thanks for your interest in improving Hakase! This guide covers what you
need to know to land a change. For architecture and deeper context, see
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md); for what to work on, see
[docs/ROADMAP.md](docs/ROADMAP.md) and the
[issue tracker](https://github.com/amurru/hakase/issues).

## Getting started

Prerequisites: Go 1.26+, Node.js + [pnpm](https://pnpm.io), Python 3 (some
skill tooling), and on Linux optionally `bubblewrap` (enables the sandbox
isolation tests locally).

```bash
git clone https://github.com/amurru/hakase && cd hakase
make build-frontend   # REQUIRED before any Go command on a fresh clone:
                      # internal/web/dist/ is gitignored but go:embed needs it
go test ./...         # hermetic: no network, no config.json, no MCP servers
cd webui && pnpm test # frontend suite (vitest, jsdom)
```

Single tests: `go test ./internal/agent/ -run TestName` and
`cd webui && pnpm vitest run src/composables/useMermaid.test.ts`.

## How we work

- **Issues first.** Every unit of work has an issue with evidence
  (file:line) and a label (`bug`, `enhancement`, `tech-debt`). Pick one,
  or open a new one describing the problem and the evidence before writing
  code. Good entry points are labeled `good first issue`.
- **Feature docs.** Substantial features follow the house convention: a
  `docs/<feature>/` directory with `spec.md` + `plan.md` + `tasks.md`.
  See existing feature dirs under `docs/` for the shape.
- **Branches & PRs.** Fork (or branch) off `develop`; PRs target
  `develop`. Keep PRs to one logical change.
- **Commit messages** follow Conventional Commits, matching the existing
  history: `feat(scope): ...`, `fix(scope): ...`, `docs: ...`,
  `chore: ...`, `style: ...`, `test: ...`.
- **Reference issues** in the commit body (`Closes #N`).

## Code conventions

- Tests live next to the sources (`*_test.go`) and must be self-contained:
  temp dirs, no network, no `config.json`, no running MCP servers. Tests
  that need isolation redirect `$HOME`/`XDG_CONFIG_HOME` to a temp dir.
- Never commit runtime artifacts: `config.json`, `sessions/`, `outputs/`,
  `logs/`, `.hakase/`, `internal/web/dist/`, binaries.
- `internal/` package import rules are documented in
  [AGENTS.md](AGENTS.md) — notably `internal/sleep` must never import
  `internal/agent`, and `internal/cli` must never import `internal/channel`.
- Run `gofmt -l .` (CI enforces it), `go vet ./...`, and both test suites
  before pushing — CI runs exactly this on every PR
  (`.github/workflows/test.yml`).

## Reporting problems

- Bugs: [open an issue](https://github.com/amurru/hakase/issues/new?template=bug_report.yml)
  with steps, expected vs actual, and version (`hakase version`).
- Security vulnerabilities: **do not open a public issue** — see
  [SECURITY.md](SECURITY.md) for private disclosure.
