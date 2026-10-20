# Tasks: Flow Guard

Spec: [spec.md](spec.md) — plan: [plan.md](plan.md). House rule: tick boxes in the same PR that lands the work. Audit fixes C1-C2/H1-H5/M1-M5 + senior review included. Branch off `develop`; open governing issue with file:col evidence first.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase 0 — fixtures first

- [x] **T0.1 [QA]** 3 seed replay fixtures before T1.1 (`docs/flow-guard/fixtures/`: read-private-then-send deny, webfetch-then-send ask, v1-parity unchanged). Spec: FG-006 senior trick. Blocks schema mistakes early.

## Phase 1 — label + schema + shell/file sinks

- [x] **T1.1 [BE]** label type + fold (`internal/permissions/flow.go`). Spec: FG-001. `Trust`/`Audience` ceiling/`SessionLabel{Version}`/senior `Meet`/`Fold(idemKey)`/`StartLabel`, `*SessionLabel` nil fail-closed, `== SessionLabel{}` forbidden, `sync.RWMutex` + CAS, stdlib only.
- [x] **T1.2 [BE]** `permissions.json` v2 schema (`Rule.Delta`/`Requires`/`sanitizable`, `Compile` version 2, canonical resource table incl. `x/net/idna` + python best-effort, monotonic layer merge, `flowExperimental` gate). Spec: FG-002 + H5/M4. v1 byte-identical, strict keys.
- [x] **T1.3 [BE]** label registry + ctx propagation + `Session.Flow` + `FlowApplied[(Sequence, tool_call_id)]` + absent-flow recompute + resume notice (`internal/session/` + `session_store.go`). Spec: FG-001/006 + H3/H4/M1. Old files never widest.
- [x] **T1.4 [BE]** pre-dispatch wiring shell/file (`gate.go` 4b, `fileops taskResolve`, `AuditSystemCommandPaths` + `@-file` best-effort rule) with `(labelVersion, argsHash)` binding under mutex. Spec: FG-003 + H2. Flow only escalates, `RiskUnknown` still asks. v2 experimental until T2.1+T2.2.
- [x] **T1.5 [QA]** fold matrix + zero-value trap + v1 parity + layering x flow matrix + idempotency. Spec: FG-001/002/003. Monotone descent, ceiling `min`, `allowManagedOnly` preserved, double-admit no-op.
- [x] **T1.6 [DOCS]** starter v2 rules as `docs/flow-guard/permissions.example.jsonc` (JSONC so comments do not break tooling; commented, ask-not-deny, no `sanitizable:true` on `send_message *`; canonical permissions-policy example path, not `config.json.example` which is the general `Config` example) + fixtures v1 set grown from T0.1.

## Phase 2 — sinks + post-admission

- [x] **T2.1 [BE]** MCP egress at dispatch (`mcp/runserver/gates.go`, not only ask branch) + channel push (`telegram/send.go`, `discord/push.go`) + python/download/skill/sidekick-question gates, unknown-sink fail-closed. Spec: FG-003 + C2/M3. Sources fold on success and error.
- [x] **T2.2 [BE]** post-admission withhold/redact + error redact-and-fold returning `(nil, redactedErr)` (`agent.go` AfterTool, `util.RedactSecrets` reuse, denial redacted for model). Spec: FG-004 + C1/H1/H2. Never return a result override when `runErr != nil`. Vision/sidekick answers same path.
- [x] **T2.3 [BE]** delegate inherit via ctx + explicit terminal label merge (`delegate.go`). Spec: FG-001/004 + H4. Child egress denied by parent label, hash for audit only, forbid narrowed delegate unless no net egress or Ask.
- [x] **T2.4 [QA]** exfil fixtures (read-private-then-send-public denied, webfetch-then-send asks, sanitized send still asks unless narrow `sanitizable:true`) + error redact/fold + parallel TOCTOU under mutex + failure-pass-through tests. Spec: FG-003/004 + H1/H2/C1.

## Phase 3 — remedies + audit + replay CI

- [ ] **T3.1 [BE]** denial text (redacted, resume notice) + ask-human reuse (patch `ApproveExec` `mode==allow` shortcut + `disable_bypass`, single-use `(tool,argsHash,labelVersion)`) + sanitize re-check. Spec: FG-005 + H1/H5.
- [ ] **T3.2 [BE]** audit `flow_blocked`/`flow_sanitized`/`flow_error_redacted`/`flow_recomputed` + appended CSV with version header + redacted reasons + args hashes + `HAKASE_FLOW_DEBUG=1` logging `argsHash` to stderr only (never full args). Spec: FG-006 + M2/M5. Metadata only, rotation unchanged.
- [ ] **T3.3 [QA]** replay fixture driver + e2e read-then-send + resume/compaction Version-unchanged + CSV parser tests (old strict parser on old-format rows, version-aware parser on both). Spec: FG-006 + H3/L3.
- [ ] **T3.4 [DOCS]** `docs/permissions/guide.md` v2 experimental section + CHANGELOG + inspiration line in touched packages.
- [ ] **T3.5 [QA]** Full suite green: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.
