# Execution Plan: Session rewind (#21)

Strategy: snapshots ride the existing per-session JSON store — new methods
on `SessionStore` reusing its flock/atomic/perm helpers, one hook at the
user-turn persistence choke point, two endpoints beside `/compact`, and a
confirm-dialog action on user message bubbles. Truncate-in-place with a
mandatory pre-restore snapshot (see spec Decisions).

## Phases

### Phase 1 — Store + config

1. `internal/session/snapshot.go`: SaveSnapshot/ListSnapshots/LoadSnapshot/
   DeleteSnapshots on `SessionStore`; `DefaultSnapshotMax = 50`;
   `NewSessionStoreWithSnapshotLimit`; name validation
   `^[0-9]+-(pre|pre-restore)$`. Spec: SR-001.
2. `internal/config`: `SessionConfig{Snapshots SnapshotsConfig{Enabled
   *bool, Max int}}` + ApplyDefaults/Validate/accessors + env overrides +
   `envConfigSet`. Spec: SR-002.

### Phase 2 — Hook + API

3. `SessionService`: snapshots switch + pre-message hook in
   `RecordUsageInSession`/`RecordUsageWithAttachments`; `DeleteSnapshots`
   in `DeleteSession`. Spec: SR-003, SR-006.
4. `ChatAPI`: `GET /sessions/{id}/snapshots` + `POST /sessions/{id}/restore`
   with the compact-style mid-run 409; register routes. Spec: SR-004.
5. cmd wiring: web.go + main.go pass config values into the store/service.

### Phase 3 — Web UI

6. `lib/api.ts` snapshot/restore calls; `MessageDTO.sequence` mapped in
   `loadSessionHistory`; restore action + dialog in ChatView/
   MessageBubble. Spec: SR-005.

### Phase 4 — Tests + docs

7. Store/service/handler/config tests mirroring the compact_test pattern;
   webui vitest for the API helpers if the repo covers lib/api. Spec: SR-007
   in tasks.md.
8. CHANGELOG entry, README mention, tasks.md ticked in the landing PR.

## Critical path

1 → 3 → 4 are sequential (store → service hook → endpoints); 2 is
independent; 5 needs 1+2; 6 needs 4.

## Verification baseline

- Store: ring pruning, 0600 perms, traversal-safe names, list order.
- Service: one `pre` snapshot per user turn (not for replies, not for a
  session's first message), disabled switch writes nothing.
- Handler: restore truncates, creates `pre-restore` undo snapshot, 409
  mid-run, keeps ProjectID; snapshots list shape.
- Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`, `pnpm test`.

## Risk register

- `.snapshots/` inside the sessions dir — safe: `listSessionIDs` ignores
  subdirectories (`session_store.go:487`); asserted by a test.
- Snapshot JSON drift: snapshots marshal the same `*Session` the store
  saves — no second schema.
- Legacy sessions with zero `Sequence` values: the UI aligns snapshots by
  message-count equality with the array index, not by sequence.
