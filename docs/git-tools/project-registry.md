# Project Registry (remote-web Phase 2)

Feature: `project-registry`. Status: design (2026-09-03). Companion:
`project.md` (D-P1..D-P5, derived local identity), `research.md` /
`spec.md` / `tasks.md` (git-tools, incl. the v2.1 clone/push/pull tools this
phase builds on).

## 1. Problem

`project.md` shipped the **derived** project identity: the git root under the
launch cwd, computed once per process. That covers TUI and locally-run web
(server and client share a filesystem). It cannot cover **hakase web running
on a remote host**: the client's repository is not on that host, and there is
nothing for `git_status`/`git_commit`/the workspace snapshot to anchor to.
Without a way to materialize the client's code on the host, a remote hakase
web can only work on whatever the server operator already has.

The ecosystem answer (OpenHands, Codex cloud, Claude web) is a **registered
project**: an explicit entry `{name, clone source}` that the host materializes
into a managed directory, with delivery back through push/PR. The v2.1 git
tools (`git_clone`, `git_push`, `git_pull`) already provide the engine; this
phase adds the registry, session binding, and web surface.

## 2. Model

A project has two lives:

| | Derived (shipped, D-P1) | Registered (this phase) |
| --- | --- | --- |
| Where the code lives | already on the host under the launch cwd | cloned by hakase into a managed dir |
| Identity source | upward `.git` walk (`project.FindRoot`) | registry entry (explicit) |
| Workspace roots | unchanged (confinement as configured) | pinned to the project checkout |
| Sessions | implicit (whatever repo the cwd is in) | bound via `project_id` |
| Delivery | user's own git workflow | `git_push`/PR via `gh` skill |

Registry entry (JSON, one file, no DB):

```json
{
  "id": "proj_01J...",            // ULID, stable across renames
  "name": "hakase-web",           // display name, unique per registry
  "source": {
    "url": "https://github.com/amurru/hakase.git",
    "ref": "main"                 // default branch to clone/checkout
  },
  "checkout": "~/.hakase/projects/proj_01J...",  // managed dir (host-side)
  "status": "ready" | "cloning" | "sync_error",
  "created_at": "...", "updated_at": "..."
}
```

Storage: `projects.json` under `config.HakaseHome()` (same home as
`config.json`; never inside a project checkout; same sensitive-file treatment
as the config - the file itself contains no secrets, only clone URLs).
Loaded once per process like the rest of config; writes are
read-modify-write with a lock mirroring `tasks.json` handling.

## 3. Lifecycle and decisions

- **DP-6 (register → clone → pin)** — Registering a project records the
  entry and runs the materialization through the existing `git_clone`
  engine (`sandbox.gitCloneContent`), into `~/.hakase/projects/<id>`.
  Clone-source policy is the v2.1 D9 allowlist (https/git/ssh over the
  network; local paths only when no sandbox is active) — on a remote host
  the network forms are the point.
- **DP-7 (session binding)** — Sessions created from the web UI may carry a
  `project_id`. When present, the session's workspace roots are pinned to
  the project checkout (this finally resolves the "pin workspace to
  project" item deferred in D-P2 — but **only for registered projects**,
  where the pin is explicit and never a silent widening of a derived
  layout). The existing per-process project root/snapshot machinery then
  works unchanged because the checkout IS the process cwd's repo...  —
  correction: on a multi-project server the process cwd cannot serve all
  projects, so the snapshot/git-default path must read the *session's*
  project root, not the process cwd. This is the one architectural change
  to `internal/project`: make the session root settable per session
  (thread/context-scoped value consulted by `resolveRepoDir` and the
  snapshot builder) instead of a process global.
- **DP-8 (credentials)** — hakase never stores git credentials. Clone/push/
  pull authenticate through the host's own mechanisms: git credential
  helpers, `gh auth`, or SSH agents - exactly what `system_exec` git would
  use. Documented operator step for remote deployments; per-project token
  config is deliberately out of scope (secrets stay out of `projects.json`).
- **DP-9 (delivery)** — Pulling upstream state is `git_pull` (ff-only,
  approval-gated). Publishing work is the existing story: `git_push` for
  hosts the client trusts with push rights, or the `gh`-based PR skill
  (branch → push → PR) matching the Hermes GitHub flow. Nothing new is
  needed in the git tools.
- **DP-10 (deletion)** — Deleting a project removes the registry entry and
  the local checkout; it never touches the remote (no force-push, no remote
  deletion). Re-registering re-clones.

## 4. Web surface (remote deployments)

- `GET  /api/projects` — list registered projects (name, ref, status).
- `POST /api/projects` — register: `{name, url, ref?}` → clones, returns
  the entry. Clone runs synchronously with the standard bounded output;
  failures leave the entry in `sync_error` with the bounded stderr.
- `DELETE /api/projects/{id}` — unregister + remove checkout (DP-10).
- `POST /api/projects/{id}/sync` — `git_pull --ff-only` on the checkout
  (approval-gated on the agent side; in the web UI this is a user action).
- Session creation accepts `project_id`; the chat header shows the project
  name; the agent's instructions get the same `GIT WORKSPACE` snapshot as
  local sessions once the snapshot source is session-scoped (DP-7).

Local TUI/local-web behavior is untouched: no registry entries, derived
project identity as today. The registry is additive.

## 5. Phased tasks

- **P1 - Registry core [BE]** *(done 2026-09-03)*: `internal/registry`
  package: entry model, `projects.json` load/save with lock, CRUD + id/name
  uniqueness, source-URL allowlist (https/http/git/ssh). Session-scoped
  project root: `internal/project` gained `WithRoot`/`RootFrom`
  (context-scoped value with process-root fallback), threaded through
  `resolveRepoDir` + `BuildGitWorkspaceBlock`. Verify: package tests;
  existing git-tools tests stay green.
- **P2 - Materialization + CLI [BE]**: register/sync/delete wired to the
  git_clone/git_pull content functions (reuse, no new exec surface),
  `~/.hakase/projects/<id>` layout, status transitions, `hakase projects`
  CLI subcommand. Verify: temp-home tests registering a local bare remote
  (sandbox-off, per D9), syncing after an external push.
- **P3 - Web API + session binding [BE]**: the four endpoints under the
  existing auth middleware; session model gains `project_id`; workspace
  roots pinning per DP-7; snapshot per session. Verify: handler tests with
  isolated home; `go test ./internal/web/...`.
- **P4 - UI + docs [QA]**: project selector in the web sidebar, chat header
  chip, README remote-deployment section (incl. the credential note DP-8).
  Verify: `cd webui && pnpm test`, manual smoke against a local server.

## 6. Non-goals (recorded)

- Multi-tenant isolation (one operator, one host filesystem - the sandbox
  still confines agent actions per session).
- Cloud sync/PR orchestration beyond the existing `gh` skill.
- Per-project credentials storage.
