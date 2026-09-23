# Spec: Session checkpoints / restore-to-message rewind (#21)

Snapshot every conversation just before each user turn lands, and let the
user roll the session back to any earlier point from the chat UI — Gemini
CLI's checkpoints/rewind model. Hakase's one-JSON-file-per-session store
makes the snapshot a file copy; restore is a bounded, mid-run-guarded,
self-undoing truncate.

Governing issue: amurru/hakase#21.

## Decisions

- **Truncate in place, not branch.** Restoring overwrites the session's
  message list with the snapshot's (same session id — the UI stays where it
  is, project binding survives untouched). The issue's branch-vs-truncate
  concern (auditable history) is covered by a mandatory pre-restore
  snapshot: nothing is lost until retention prunes it, and "undo the undo"
  is itself a restore. Branching sessions would break the continue-here UX
  and fork project binding for no operational gain.
- **Restore-to-here = state before that user prompt** (Gemini semantics): a
  rail entry for prompt N restores the snapshot taken immediately before N,
  so a bad turn *and its prompt* can be redone differently.
- **One snapshot per user message, taken at the persistence choke point** —
  `SessionService.RecordUsageInSession` / `RecordUsageWithAttachments`
  (`session_service.go:138-169`) covers web chat, both TUI send paths, and
  Telegram; cron uses the in-memory service and is correctly out of scope.

## Specs

### Spec SR-001: snapshot store on `SessionStore`

New methods on `SessionStore` (`internal/session/snapshot.go`), reusing the
store's `.lock` flock, `writeFileAtomic`, `validSessionID`, and 0700/0600
discipline:

- Layout: `<sessionsDir>/.snapshots/<sessionID>/<unixNano>-<trigger>.json`
  where trigger is `pre` (before a user message) or `pre-restore`. The
  dot-subdirectory is invisible to `listSessionIDs` (`session_store.go:487`)
  so the index never sees it. Snapshots are the full session JSON — the
  exact struct the store saves.
- `SaveSnapshot(session *Session, trigger string) (name string, err error)`
  writes atomically, then enforces the per-session ring (keep newest
  `max`, default `DefaultSnapshotMax = 50`).
- `ListSnapshots(sessionID)` returns `[]SnapshotInfo{Name, CreatedAt,
  Messages, Trigger, Preview}` (Preview = last message content, truncated)
  sorted newest-first; list reads the bounded set directly.
- `LoadSnapshot(sessionID, name)` → `*Session` (name validated against
  `^[0-9]+-(pre|pre-restore)$` — names come from the network).
- `DeleteSnapshots(sessionID)` removes the per-session snapshot dir
  (session deleted ⇒ snapshots deleted).
- Store constructors: `NewSessionStore(dir)` keeps the current default
  (snapshots on, `DefaultSnapshotMax`); `NewSessionStoreWithSnapshotLimit(dir, max)`
  for config-driven wiring (max ≤ 0 disables snapshot writes).

### Spec SR-002: config `session.snapshots`

`SessionConfig{Snapshots SnapshotsConfig}` in `internal/config/config.go`,
`SnapshotsConfig{Enabled *bool, Max int}` following the MemoryConfig
tri-state pattern (`config.go:291`):

- `enabled` — nil/true = on; explicit `false` disables snapshot writes
  entirely (the pointer keeps "absent" distinguishable from "false").
- `max` — snapshots kept per session, default 50; `Validate` rejects
  negative values loudly.
- Env: `HAKASE_SESSION_SNAPSHOTS_ENABLED` (strict bool policy),
  `HAKASE_SESSION_SNAPSHOTS_MAX` (positive int), both in `envConfigSet`.
- Accessors `SessionSnapshotsEnabled(c)` / `SessionSnapshotsMax(c)` with
  nil-safe defaults.

### Spec SR-003: pre-message hook

`SessionService` gains a snapshots switch (set at wiring from config,
default on when the store has a limit). In both `RecordUsageInSession` and
`RecordUsageWithAttachments`, after load/ensure and BEFORE the new message
is appended: if enabled && role == "user" && the session already has
messages → `store.SaveSnapshot(session, "pre")`. Best-effort: a snapshot
failure logs a warning and never blocks the turn. First messages of a new
session skip the snapshot (nothing to roll back to).

### Spec SR-004: restore API

On `ChatAPI` next to `PostCompact` (`internal/web/handlers/chat.go`), same
auth group and mid-run guard (per-session semaphore → 409):

- `GET /api/sessions/{id}/snapshots` → `{"snapshots":[...]}` (SR-001 info
  list). 404 for unknown sessions like the other session routes.
- `POST /api/sessions/{id}/restore` body `{"snapshot":"<name>"}`:
  1. load the current session (404 if missing);
  2. `SaveSnapshot(current, "pre-restore")` — restore is always undoable;
  3. load the snapshot, bump `UpdatedAt`, `store.Save` (atomic + flock +
     index update — never a raw file copy);
  4. respond `{"status":"restored","messages":<n>}`; the client reloads
     history (same flow as compact — no SSE events needed).
  Project-bound sessions keep `ProjectID`/`ProjectName` from the snapshot;
  sandbox pinning is derived per run from the session, so it stays intact
  (acceptance criterion 3).

### Spec SR-005: web UI

- `lib/api.ts`: `listSnapshots(id)` / `restoreSession(id, name)`.
- `ChatView.loadSessionHistory` maps `MessageDTO.sequence` onto client
  messages (currently ignored) so the UI can address server-side positions.
- Restore affordance on **user** message bubbles (`MessageBubble.vue`
  hover controls, next to copy): "Restore to before this message". Clicking
  opens the existing shadcn `Dialog` pattern (SessionsView.vue:451) with the
  snapshots list, preselecting the snapshot whose `Messages` count equals
  the message's array index (the pre-message snapshot); the user confirms —
  hover, click, confirm = 3 clicks. After success: clear messages → reload
  history (the `runCompact` template, ChatView.vue:539).
  Deviation note: the issue names the MessageRail; the rail's ticks are
  1:1 with these user prompts but are jump-navigation. The action lives on
  the rail's anchor messages (same entries, better hover ergonomics).
- The dialog doubles as a snapshot browser (timestamp, message count,
  preview, trigger `pre`/`pre-restore`) so any snapshot — including
  pre-restore undo points — is reachable.

### Spec SR-006: lifecycle pruning

`SessionService.DeleteSession` calls `store.DeleteSnapshots(id)` after
`store.Delete` — covering both the web delete handler and `CleanupStale`
(single choke point; the handler needs no change). Archive keeps snapshots.

## Non-goals (deferred, tracked in tasks.md)

- Workspace snapshots for registered projects (git stash-create refs).
- Auto-checkpoint before gated destructive actions (approval hook).
- TUI `/restore` and channel-side restore.
- Branching/forking sessions on restore.

## Definition of done

- [ ] A bad `patch`/`system_exec` turn is recoverable from the chat UI in
      ≤ 3 clicks (hover → Restore → confirm).
- [ ] Snapshots bounded by retention (default 50/session) and written 0600
      under `sessions/.snapshots/`.
- [ ] Restore preserves project binding (registered-project sessions keep
      sandbox pinning on the next run).
- [ ] `gofmt`, `go vet`, `go test ./...` and `cd webui && pnpm test` green.
