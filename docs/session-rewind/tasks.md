# Tasks: Session checkpoints / restore-to-message rewind (#21)

Pre-message session snapshots + restore-to-here from the chat UI.
Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule: tick boxes
in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[FE]` web UI, `[QA]` tests, `[DOCS]` docs.

## Phase 1 — Store + config

- [x] **T1.1 [BE]** `SessionStore` snapshot methods (save/list/load/delete)
      with ring retention, 0600, name validation,
      `NewSessionStoreWithSnapshotLimit`. Spec: SR-001.
- [x] **T1.2 [BE]** `config.SessionConfig` (session.snapshots.enabled/max)
      + defaults + validate + env overrides + `envConfigSet`. Spec: SR-002.
- [x] **T1.3 [QA]** store tests: ring, perms, order, traversal guard,
      dot-dir invisible to the index. Spec: SR-001.

## Phase 2 — Hook + API

- [x] **T2.1 [BE]** `SessionService` snapshot switch + pre-message hook;
      `DeleteSnapshots` in `DeleteSession`. Spec: SR-003, SR-006.
- [x] **T2.2 [BE]** `GET /sessions/{id}/snapshots` +
      `POST /sessions/{id}/restore` (pre-restore undo snapshot, mid-run
      409, ProjectID preserved). Spec: SR-004.
- [x] **T2.3 [BE]** cmd wiring (web.go, main.go) from config. Spec:
      SR-001/002.
- [x] **T2.4 [QA]** service + handler tests (hook timing, restore
      truncation, guards, undo snapshot, project binding). Spec: SR-004/006.

## Phase 3 — Web UI

- [x] **T3.1 [FE]** `lib/api.ts` snapshot list + restore calls. Spec:
      SR-005.
- [x] **T3.2 [FE]** map `MessageDTO.sequence` in `loadSessionHistory`.
      Spec: SR-005.
- [x] **T3.3 [FE]** restore action on user bubbles + snapshots dialog
      (preselect by message index), reload-after-restore. Spec: SR-005.
- [x] **T3.4 [QA]** webui suite green (vitest + `vue-tsc -b` via build);
      no dedicated vitest for the api helpers beyond existing coverage of
      the module. Spec: SR-005.

## Phase 4 — Docs

- [x] **T4.1 [DOCS]** README + CHANGELOG entry.
- [x] **T4.2 [QA]** Full suite green: `gofmt -l`, `go vet ./...`,
      `go test ./...`, `cd webui && pnpm test`.

## Deferred (follow-ups, not in #21 acceptance)

- Workspace snapshots for registered projects (`git stash create` refs +
  diff in the confirm dialog) — issue item 3.
- Auto-checkpoint hook before gated destructive actions — issue item 4.
- TUI `/restore` slash command; restore from Telegram.
- Branching (fork-on-restore) semantics.
