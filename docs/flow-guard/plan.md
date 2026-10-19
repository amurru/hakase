# Execution Plan: Flow Guard (`permissions.json` v2)

Spec: [spec.md](spec.md). Inspiration only, own Go code, no sidecar. Fixtures first, then engine, sinks, remedies + audit. Security audit fixes (C1-C2/H1-H5/M1-M5) + senior review applied in spec FG-001..006.

## Phases

### Phase 0 — replay fixtures first (senior review)

0. Write 3 seed fixtures in `docs/flow-guard/fixtures/` before T1.1: read-private-then-send-public deny, webfetch-then-send ask, v1-parity unchanged. Drive `LookupWithLabel`+`Fold` with no tool exec. Catches schema mistakes in minutes. Tasks T1.6 fixture set grows from these seeds.

### Phase 1 — label + schema + shell/file sinks (FG-001/002/003 partial)

1. New `internal/permissions/flow.go`: `Trust`, `Audience` ceiling, `SessionLabel{Version}`, `StartLabel`, senior `Meet`, `Fold(idemKey)`, `*SessionLabel` nil fail-closed + zero-value trap test (`== SessionLabel{}` forbidden). `sync.RWMutex` + CAS on registry. Unit-tested pure functions. No imports beyond stdlib.
2. Extend `Rule` with `Delta` / `Requires` / `sanitizable` + `Compile` validation for `version 2` + canonical resource table (M4: shell raw, paths via `ResolveScopedPath`, URLs via `net/url` + `x/net/idna`, python `code_hash` + arg-parse best-effort, no substring deny); `PolicyRule` citation with `argsHash`. Keep `Evaluate`/`Lookup` signatures, add `LookupWithLabel`. v1 policies byte-identical. Monotonic cross-layer merge + `allowManagedOnly` strips widening (H5). `flowExperimental` gate: v2 loads but enforcement off until T2.1+T2.2 land.
3. In-memory label registry + ctx propagation (`internal/session/flow.go` or `pauses.go` pattern) + `Session.Flow` + `FlowApplied[(Sequence, tool_call_id)]` persist + absent-flow recompute (M1/H3) + one-time resume notice. Old files never default to widest.
4. Wire pre-dispatch into `EvaluateCommand` step 4b (`internal/agent/gate.go:1103-1120`) + `fileops taskResolve` + `AuditSystemCommandPaths` + `@-file` body rule (best-effort). Ship sink starter set in `config.json.example` (commented, ask-not-deny) + fixtures v1. Binding `(labelVersion, argsHash)` recorded at pre-check under mutex (H2).

### Phase 2 — MCP/channel/python/download/sidekick/skill/vision + post-admission (FG-003 rest, FG-004)

5. Wire `LookupWithLabel` at dispatch in `mcp/runserver/gates.go:23-204` (not only ask branch; allowlisted tools must still hit flow, M3) and channel push in `internal/channel/telegram/send.go` + `internal/channel/discord/push.go` + `python_interpreter` code gate + `download_file` URL gate + `save_skill` persist gate + `ask_sidekick` question gate. Unknown sinks fail-closed on narrowed label (C2). Sources (`read`, `webfetch`) apply `delta` on success AND error (C1).
6. Extend `makeHookAfterToolCallback` (`internal/agent/agent.go:163-170`) with `CheckPostFlow`: secret-redact + non-admissible withhold on success (H1), redact + conservative fold then `nil,nil` on `runErr != nil` (C1/M5), re-validate `requires` against current label before admission (H2). Label update idempotent by `(Sequence, tool_call_id)` from `AddMessageWithMetaAndAttachments`. Vision/sidekick answers pass same path (H4). Denial text redacted for model too (senior review).
7. Delegate inheritance via ctx: child copies parent label at `delegate.go:355-409`, child egress denied by parent label, child returns terminal label explicitly, parent `Meet(parent, childFinal)` (hash for audit only, H4). No new subagent UX.

### Phase 3 — remedies + audit + replay CI (FG-005/006)

8. Denial text format + `ask-human` via existing `ApproveExec` patched at `internal/agent/approval.go:50-86` to bypass `mode==allow` shortcut on flow Ask, honoring `disable_bypass` (H5 senior site); single-use `(tool, argsHash, labelVersion)`, no label reset. `sanitize` via `util.RedactSecrets` + re-check, auto-proceed only with `sanitizable:true` on narrow resources, never on `send_message *` (H1).
9. Audit `flow_blocked`/`flow_sanitized`/`flow_error_redacted`/`flow_recomputed` (`audit.go`, `audit_chain.go` CSV appended columns with version header) + actor attribution reuse (`AuditApprovalAnswer` sites). Redact reasons, hash args (M2). `HAKASE_FLOW_DEBUG=1` logs full args to stderr only. Strict-parser CSV test (L1 senior).
10. Replay fixtures + Go test driver including e2e read-then-send; compaction inherits label + redacts summary, resume keeps `Version` (H3 test: fold, compact, resume, assert unchanged); `tasks.md` ticked; CHANGELOG; `docs/permissions/guide.md` v2 experimental section; `config.json.example` starter rules. House: open governing issue with file:col evidence, branch off `develop`, one PR per phase.

## Critical path

0 → 1 → 2 → 4 → 5 → 6 → 8 → 9 sequential. 3 and 7 ride 1-2. 10 last.

## Verification baseline

- Fixtures green before engine: 3 seeds pass against stub `LookupWithLabel`+`Fold`.
- Zero-value trap test: `Session{}` without init fails closed, never `== SessionLabel{}` comparison in code (senior review).
- Fold matrix: trust monotone descent, audience ceiling `min`, start label, nil fail-closed, idempotent re-admit no-op, child merge narrowing-only.
- Precedence matrix extended: deny > ask > allow preserved, flow only escalates, v1 parity test (no delta/requires = old verdicts), layering monotonic + `allowManagedOnly` stripping.
- Unknown-sink test: narrowed label + unmapped tool asks/denies (C2).
- Exfil fixture: `read docs/internal/*` then `send_message public` denied with alternative text; `webfetch *` then `send_message` requires ask; sanitized send still asks unless `sanitizable:true` on narrow resource (H1).
- Error-path test: failed tool secret redacted + folded, error preserved (C1).
- TOCTOU test: parallel read+send in one turn denied via post re-check under mutex CAS (H2).
- Resume/compaction tests: no double-fold, no widen, old-file recompute + notice, fold-compact-resume `Version` unchanged (H3/M1).
- Delegate/sidekick/vision tests: child egress denied, answer folded suspicious, explicit terminal label merge (H4).
- Layering/mode tests: project cannot remove enterprise `requires`, `mode=allow` keeps flow Ask via patched `ApproveExec` (H5).
- Post-admission test: secret-shaped result redacted before context, failure path passes through redacted (ADK error semantics preserved).
- CSV strict test: new file opens under old `FieldsPerRecord=N` parser (senior review).
- Full suite: `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.

## Risk register

- **Partial taint false confidence:** v2 experimental gate until T2.1+T2.2; docs mark experimental; commented starter uses ask-not-deny.
- **Label drift vs store resume:** label updates exactly once per admitted `(Sequence, tool_call_id)`, same site as `tool_result` persist. Guard with applied-set + resume/compaction test (H3).
- **Over-narrowing utility loss:** coarse `webfetch * -> suspicious` may ask too often. Mitigate with per-domain starter allows in example + `ask` (not `deny`) as default flow effect, measure on real sessions before tightening.
- **PostTool override converting failure to success:** ADK drops tool error on non-nil AfterTool return. Hard rule: never override when `runErr != nil`, redact + fold then `nil,nil` (C1).
- **Sanitizer false confidence:** `RedactSecrets` is secret-shaped only. Never auto-allow on narrowed label from scan result; `sanitizable:true` only on narrow resources (H1).
- **Python/URL best-effort:** `@-file` substring and stdlib URL parse are fragile. Gate on arg-parse + `code_hash`, `x/net/idna` or deferred punycode; document limits.
- **Audit size/injection:** label snapshot is enums + hashes, no payload. Redact reasons and denial text, append CSV columns with version header, strict-parser test (M2).
- **Scope creep toward full APPA:** effects DAG, `forwards`, attest-schema, batteries explicitly deferred in spec. Any addition needs new FG spec + issue.
