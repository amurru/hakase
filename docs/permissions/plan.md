# Execution Plan: Permissions Policy

Spec: [spec.md](spec.md). Engine first, then layering, queue, audit.

## Phases

### Phase 1 — engine (PM-001/002)

1. New `internal/permissions/` (or `gate.go` extension): triple compile + match + deny>ask>allow eval; canonical resolve reuse.
2. Wire into `EvaluateCommand` (after hard-deny), `fileops taskResolve`, `auditCandidatePaths`.
3. Honor `approval.mode` in `ApproveExec`; `ReplayNever` interaction pinned.

### Phase 2 — layering (PM-001)

4. Loader: user + project (trust-gated, mtime-cached per root) + enterprise file + URL poll; `allowManagedOnly`/`disableBypass`; `PinnedTo` non-widening.
5. `internal/config` plumbing + `Validate` in `LoadConfig`.

### Phase 3 — queue + audit (PM-003/004)

6. `GET /api/approvals/pending` + RBAC roles; channel first-wins preserved.
7. Audit additive fields + `hakase audit export/verify` + SIEM forward config + retention.
8. tasks.md ticked; CHANGELOG; README/docs mention.

## Critical path

1 → 2 → 4 → 6 → 7 sequential. 3, 5 ride 1-2.

## Verification baseline

- Precedence matrix (deny>ask>allow, multi-resource deny-wins, default ask).
- Layering/lock matrix + trust-lapse on script rewrite + folder-trust gate for project allow.
- Queue first-wins e2e (web + Telegram stub); chain tamper detection; rotation compat.
- Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.

## Risk register

- **Glob semantics drift vs opencode**: pin `*`/`?` + `~` expansion + absolute canonicalization in tests.
- **Project allow escalation**: folder-trust gate required; enterprise `allowManagedOnly` strips lower allow.
- **Audit size**: additive fields only; rotation unchanged; export streams, never loads all.
