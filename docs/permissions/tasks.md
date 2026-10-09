# Tasks: Permissions Policy

Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[FE]` frontend, `[QA]` tests, `[DOCS]` docs.

## Phase 1 — engine

- [x] **T1.1 [BE]** triple engine + eval precedence (`internal/permissions/` or `gate.go`). Spec: PM-001/002. (Done: `internal/permissions/permissions.go` - Compile + Evaluate, deny>ask>allow, multi-resource deny-wins, default ask, `*`-spans-separators globs + `~`/`$HOME` expansion + absolute cleaning; issue #85.)
- [x] **T1.2 [BE]** wiring into `EvaluateCommand`, fileops, path audit; `approval.mode` honor. Spec: PM-002. (Done, issue #85: policy eval after all deny-class checks in `EvaluateCommand` - deny/ask override, allow falls through on RiskUnknown; deny enforcement in `taskResolve` + `AuditSystemCommandPaths`; `approval.mode` deny/allow in `ApproveExec` with ReplayNever pinned. Nil policy = zero behavior change; additive sites act on matched rules only.)
- [x] **T1.3 [QA]** precedence matrix + no-match default + multi-resource deny-wins. Spec: PM-002. (Done: `internal/permissions/permissions_test.go` - 8 tests, all green.)

## Phase 2 — layering

- [x] **T2.1 [BE]** user/project/enterprise loader + trust gate + URL sync + locks. Spec: PM-001. (Done, issue #85: `internal/permissions/loader.go` - strict-key file load, `perm:<sha256>` project trust gate with rewrite-lapse, mtime caches incl. per-root + content-fp project entries, enterprise file + URL poll with last-good retention and best-effort disk cache, allowManagedOnly prefix-aware stripping, agent overlays; `InstallLayered`/`LookupForAgent`/`BypassDisabled` source; `disable_bypass` enforced in `ApproveExec`.)
- [x] **T2.2 [BE]** config plumbing + Validate. Spec: PM-001. (Done, issue #85: `PermissionsConfig` block - enabled default-true, enterprise path/URL, poll minutes - with `HAKASE_PERMISSIONS_*` env overrides; strict `approval.mode` validation failing startup loudly; `initPermissions` wired into `SetupRunner` with the hooks trust store - corrupt user/enterprise fails startup, untrusted project dropped; `config.json.example` block.)
- [x] **T2.3 [QA]** layering/lock matrix + trust-lapse + non-widening pin. Spec: PM-001. (Done: full enterprise/user/project x allow/ask/deny matrix, per-root isolation for the PinnedTo case, concurrent Load+Lookup under -race; trust-lapse covered in T2.1 tests.)

## Phase 3 — queue + audit

- [x] **T3.1 [BE]** pending endpoint + RBAC + batch respond (first-wins preserved). Spec: PM-003. (Done, issue #85: `pendingPrompt` metadata on the gate, `GET /api/approvals/pending` incl. resurrected, `POST /api/approvals/respond` batch looping `RespondApproval`, viewer/approver/admin `RoleMap` over `auth.web_roles` with strict validation; open map = today's behavior.)
- [x] **T3.2 [BE]** hash chain fields + `hakase audit export/verify` + SIEM forward. Spec: PM-004. (Done, issue #85: `prev_hash`/`entry_hash` on every append with tail-read + rotation-spanning links, `VerifyAuditChain`/`ReadAuditEntries`/metadata-only CSV, `AuditApprovalAnswer` actor attribution at web + Telegram + Discord answer sites, `hakase audit export|verify` CLI, `audit.forward` config + best-effort SIEM POST, `PolicyRule` citations flowing gate → exec entries via the sandbox seam.)
- [ ] **T3.3 [QA]** queue e2e + chain tamper + rotation compat. Spec: PM-003/004.
- [ ] **T3.4 [DOCS]** `docs/permissions/` guide + CHANGELOG + README.
- [ ] **T3.5 [QA]** Full suite green: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.
