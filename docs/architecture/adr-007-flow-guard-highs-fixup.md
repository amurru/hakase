# ADR-007: Flow Guard Highs Fix-up Lane (S1-S8) - Architecture Review

Status: **approve-with-notes** (conditional; notes are blocking for S5/S7 merge order, advisory otherwise)
Date: 2026-10-20
Scope: S1 channel Ask fail-closed, S2 Telegram streaming freeze, S3 typed-slice redact, S4 delegate nil fail-closed, S5 path canonicalization, S6 TOCTOU unconditional re-check, S7 FlowReason + preGrant scoping, S8 push fan-out taint.
Input: `docs/flow-guard/spec.md`, `plan.md`, `tasks.md` (Phase 3 + T3.6 complete); code as of `internal/agent/flowgate.go`, `flowremedy.go`, `approval.go`, `resume.go`, `delegate.go:464-473`, `internal/session/flow.go`, `internal/channel/telegram/run.go:203-233`, `internal/channel/discord/run.go:229`, `push.go`, `internal/permissions/flow.go`, `resources.go`.

## Context

Flow Guard is a per-session `(trust x audience)` narrowing-only label with pre-dispatch `LookupWithLabel` and post-admission `CheckPostFlow`. The S1-S8 lane closes eight highs found after Phase 3: delivery-time bypasses (S1/S8), streaming UX vs enforcement tension (S2), walker coverage (S3), nil-seeding widen (S4), resource identity (S5), TOCTOU scope (S6), approval-scope bypass via durable resume (S7). No new service, store, or protocol is proposed; all changes ride existing gates (`ApproveExec`, `CheckChannelEgress`, `MergeDelegateLabel`, `flowMapTool`).

## Decision

**Approve with notes.** No system-level conflict warrants rejection. Two items are one-way doors and must land in dependency order (S5 first with dual-match, S7 with versioned pause records); the rest are reversible and may land in any order subject to the NFR budgets below.

## Per-stream findings

### S1 - Channel Ask fail-closed (delivery gate, no approval round-trip)
- System fit: correct. `CheckChannelEgress` (`flowgate.go:740`) has no tool call to bind an approval to (telegram `run.go:475-488` and discord `run.go:229` both withhold with notice). Treating flow Ask as withhold at delivery is the only sound mapping; routing a channel reply through `ApproveExec` would create a grant with no `(tool, argsHash, labelVersion)` to consume.
- Assumption: every delivery path calls the gate. Today only answer paths do (`telegram/run.go`, `discord/run.go`); push fan-out (`S8`) does not.
- Failure if missed: narrowed session exfils via answer text while tool path correctly asks. Fail-closed default is right.
- Reversible: yes (pure policy-to-withhold mapping, no schema change).
- Note (blocking, pairs with S8): S1 without S8 leaves Cron/Task/Delegation push as the bypass. Land together or gate S1 on S8 in the same release.

### S2 - Sticky withhold + streaming freeze (Telegram `runView.flowGate`)
- System fit: sticky is correct monotonically (label only narrows via `Meet`/fold; an allow at t0 says nothing about t1 after a source fold). Code already documents the limit honestly: `run.go:209-213` "streaming creations already delivered cannot be unsent; tool-dispatch gates are primary, delivery is defense-in-depth."
- Conflict (UX/liveness, not safety): sticky `flowWithhold` suppresses answer-message creation in `pump()` (`run.go:349-373`) while `OnStream` keeps accumulating `seg`/`full`. User-visible effect is a run that looks stalled (status line ticks, no text) until `finalize` delivers `⛔`. That is a perceived-freeze NFR trade, not a deadlock, but it interacts with the 2s edit throttle and `streamFlushLen` overflow path: withheld overflow segments are dropped per-tick and only the notice renders.
- Assumption: status line keeps proving liveness. If status edits also stall (rate-limit `waitTurn`), freeze looks like a hang.
- Failure condition: mid-run narrowing after large streamed prefix leaks the prefix already sent (by design, unavoidable on Telegram); over-sticky after label reset (no reset exists, so none).
- Reversible: yes.
- Notes (advisory): (a) keep the status line authoritative during withhold (e.g. keep `statusWorking` ticks; do not add body previews); (b) `finalize` must deliver exactly one notice (current `answerID==0 ? send : edit` shape is right, keep it); (c) do not attempt unsend/delete of streamed prefix - races the edit throttle.

### S3 - Typed-slice redact walker (reflection)
- System fit: real gap. `marshalResultStrings` (`flowgate.go:510`) and `redactResultInPlace` (`flowgate.go:535`) handle `string | map[string]any | []any` only. Typed ADK/MCP payloads (`[]string`, `[]map[string]string`, structs) bypass both taint detection and redaction. Reflection walker is the right fix layer (single choke point, no per-tool edits).
- NFR (perf/alloc): the walker runs on **every** post-admission result when v2 is enabled, including large benign reads. Unbounded `reflect` recursion = CPU + alloc regression on the hot path plus panic surface (cycles, unexported fields).
- Failure conditions: cycle (self-referential map), depth blowup (nested MCP blobs), mutating unaddressable values.
- Reversible: yes (pure function, no wire change).
- Notes (blocking budgets, not design): (a) fast-path the current type-switch first, reflection only as fallback; (b) hard caps: max depth (e.g. 8), max strings scanned (e.g. 512), max bytes per string (truncate already exists at 2000 for resources - mirror it); over-budget = treat as tainted + audit (fail-closed) rather than skipping; (c) `recover()` guard inside walker; (d) never follow unexported struct fields (use `CanInterface` check); (e) benchmark before/after on a 1MB `read_file` result, budget <5% regression or gate reflection behind `IsStart` short-circuit (start label already skips taint - keep that).

### S4 - Delegate nil fail-closed
- System fit: real gap, correct direction. `delegate.go:465-470`: `parentFlow == nil` (session unregistered/unknown) is seeded to `StartLabel` (widest) for the child. Meanwhile `flowLabelForCtx` (`flowgate.go:83-103`) treats present-but-unregistered as `nil` = fail-closed. The delegate path is strictly weaker than the direct path: same unknown session allows more in the child than in the parent.
- Conflict (session-less flows): `flowSessionID()==""` legitimately uses StartLabel for v1 parity (`flowgate.go:96-98`), and standalone subagents/tests run session-less. A blanket nil-means-deny breaks them.
- Assumption to pin: nil-fail-closed applies **only when a v2 policy is installed** (`FlowEnabled()` true). Without v2, nil keeps StartLabel (v1 parity). `CheckPostFlow` already follows this discipline (`flowgate.go:602`).
- Failure if over-applied: session-less `delegate_task` in v1 deployments starts denying; unit tests without registry entries fail closed unexpectedly.
- Reversible: yes (ctx seeding only).
- Note (blocking scope): implement as `parentFlow == nil && FlowEnabled() ? nil-propagate-fail-closed : StartLabel`, and make `WithDelegateFlowState(nil)` + `WithFlowLabel(nil)` mean "uninitialized, evaluate as narrowest" rather than "widest" only under v2. Document the session-less-vs-unregistered distinction in FG-001 (empty ID = no principal, v1 parity; non-empty unregistered ID + v2 = deny).

### S5 - Path canonicalization — ONE-WAY DOOR
- System fit: real gap. `flowMapTool` (`flowgate.go:169-177`) matches `read/edit/grep/glob` on the raw `path` arg while the sandbox enforces on `ResolveScopedPath`. `../`, symlinks, double-slash, and CWD-relative forms let a rule `docs/internal/*` miss `/repo/docs/internal/../internal/secret`. Canonicalizing before match is architecturally right (single identity for policy and enforcement).
- Why one-way: it changes `permissions.json` match semantics for shipped v2 rules. A rule that today matches (or misses) a raw string will match (or miss) differently after canonicalization. There is no transparent rollback once operators author rules against canonical form; both directions break someone. Same class as a public-API change.
- Assumption: rule authors write workspace-relative globs; enforcement uses absolute scoped paths. Both sides must canonicalize identically (same `ResolveScopedPath` + same case/trailing-slash normalization), or the gate and the sandbox disagree and one of them becomes the bypass.
- Failure conditions: symlink escape evaluated at match time vs use time (TOCTOU between canonicalize and open); nonexistent paths (write targets) that cannot be `EvalSymlinks`ed; Windows vs linux separators in tests.
- Notes (blocking, merge order): (a) land S5 **before** operators write production v2 rules (v2 is still experimental - this is the window); (b) dual-match during migration: match canonical primary, fall back to raw, audit which leg fired so rule authors can tighten; (c) best-effort on unresolvable paths (keep raw + audit, never error the gate); (d) fixture matrix: `a/../b`, symlinked dir, trailing slash, absolute-vs-relative, nonexistent write target. Reversible only before v2 leaves experimental; after GA it is frozen.

### S6 - TOCTOU unconditional re-check (perf + false-withhold)
- System fit: direction right, scope needs care. Today `CheckPostFlow` re-validates `requires` only when `flowResultTainted` fired (`flowgate.go:679-685`); S6 proposes unconditional re-check so a `read`-then-`send` in one turn cannot slip between pre-check and admission after `foldFlowLabel` (`flowgate.go:658-661`, binding via `RecordFlowBinding`).
- NFR (perf): unconditional `LookupWithLabel` on **every** admission doubles policy evaluation on the hottest path (glob match + `ArgsHash` + audit hashing). Policy sets are small so absolute cost is low, but under parallel read+send fan-out it serializes on the registry mutex anyway; extra eval is pure overhead when the label did not move.
- Correctness (false-withhold): sources (`read`, `webfetch`, `grep`, `shell`) carry no `requires`; re-validating them unconditionally must not withhold benign source results. The current `isEgressAction` split (`flowgate.go:725`) exists precisely to admit redacted sources while withholding egress. An unconditional withhold-on-any-requires-failure collapses that distinction.
- Reversible: yes.
- Notes (blocking scope): implement as **conditional-unconditional**: (a) cheap pre-filter first - if `label.Version == preCheckVersion` (from the `flowBinding`), skip re-eval entirely (TOCTOU impossible, no fold happened); (b) else re-evaluate, but preserve the source/egress split: sources admit redacted + audit, egress withholds; (c) keep audit volume at one entry per withheld admission, not per re-check attempt. This gets the TOCTOU guarantee at ~one integer compare in the common case.

### S7 - FlowReason + preGrant scoping — ONE-WAY DOOR (durable-resume wire format)
- System fit: real bypass. `ApproveExec` (`approval.go:50-53`) consumes a durable-resume `preGrant` keyed `(tool, command)` (`resume.go:343`) **before** the flow check (`approval.go:70-74` keys flow on `(session, tool, argsHash, labelVersion)` via `flowAskBinding`). A pre-grant minted for a benign resume re-drive auto-approves a flow Ask it never evaluated. Fix must scope pre-grants out of flow (or bind them into the flow key).
- Why one-way: pause records (`rec.Detail{tool, command}`, `resume.go:401-404`) are persisted across restarts. Changing the grant key to include `FlowReason/argsHash/labelVersion` (or refusing pre-grants for flow) changes what old records authorize. Old records lacking flow fields must fail closed with an explicit error, not silently map onto the new key.
- Assumption: at most one pending flow grant per `(session, tool, argsHash, labelVersion)`; `flowGrants` map is in-memory only (restart clears it - correct), while `preGrants` survives via pause records (the risk).
- Failure if missed: restart-resumed approval bypasses flow Ask once per stale record.
- Notes (blocking): (a) `consumePreGrant` must never satisfy a request with `FlowReason != ""` - return false early and fall through to the human; (b) mint path `grantApproval` likewise refuses flow-scoped tools or records the flow binding alongside; (c) migration: `ResolveApprovalPause` on a record without flow fields + v2 installed = error naming the reason (operator re-prompts), never auto-allow; (d) keep the `mode==allow` bypass-disabled check (`approval.go:71`) ordered **after** pre-grant rejection so neither shortcut covers flow.

### S8 - Push fan-out taint (Cron/Task/Delegation events)
- System fit: real gap. Answer paths are gated (`telegram/run.go:226`, `discord/run.go:229`) but `CronEvent`/`TaskEvent`/`DelegationEvent` fan out to `notifyUsers()`/`runningUsers()` (`discord/push.go:136-161`, telegram symmetric) with no `CheckChannelEgress`. A narrowed session that correctly withholds its answer can still exfil via a cron summary or delegation notice to N recipients.
- Architecture tension: the session label is per-session (`Audience` ceiling), but fan-out is per-recipient. `CheckChannelEgress(sessionID, channel, recipient)` already takes a recipient - the fix is N evaluations, one per recipient, skipping non-authorized recipients rather than withholding the whole batch (else one narrowed binding silences all notify users).
- NFRs: (a) **audit volume**: N recipients x M events = N audit rows per cron tick; (b) **applied-set/push cost**: no applied-set growth (read-only check, no fold - keep it that way), but per-recipient `LookupWithLabel` on every tick; (c) fan-out failure mode: partial delivery (some recipients gated, some allowed) must be explicit in logs.
- Assumption: push content is at least as tainted as the session label at event time (no per-payload taint tracking for cron output today).
- Reversible: yes (delivery-side filter, no schema change).
- Notes: (a) gate inside the fan-out loop per recipient, not once per event; (b) audit once per withheld recipient at debug/throttled level, not per tick per user at default; (c) never fold from push (read-only); (d) Telegram push symmetric to discord push - fix both or the bypass moves transports.

## Cross-cutting NFRs

- **Perf budget**: S3 (reflection) + S6 (re-check) both tax the admission hot path. Combined budget: p50 admission overhead <5% on 1MB read fixture vs pre-lane baseline, else split S6 to version-compare fast path (recommended above) and S3 behind narrowed-label short-circuit. Measure with the existing `flow_parity_test` + a new admission microbench; do not ship without numbers.
- **Availability/UX (streaming freeze)**: S2 sticky withhold is a liveness trade. Acceptable because dispatch gates remain primary and status ticks continue, but do not extend stickiness to any new surface (S8 push stays per-evaluation, never sticky).
- **Scalability (applied-set growth)**: `FlowApplied` (`session_data.go:87`) grows per `tool_call_id`; `maxFlowBindings=4096` bounds only the advisory binding map (`flow.go:292`). Long sessions (cron loops, big delegations) grow the persisted set without eviction. Not introduced by S1-S8, but S8 per-recipient checks must not add keys. Recommend a follow-up ADR on applied-set compaction (e.g. epoch truncation on session compact, already the natural site per `AppendSummary`); out of scope for this lane, do not block.
- **Audit volume**: S6+S8 multiply `flow_blocked`/`flow_sanitized` rows. Keep: one row per withheld admission (not per re-check), throttled per-recipient push withholds, hashes never raw args (already the rule per FG-006/M2). Confirm `HAKASE_FLOW_DEBUG=1` stays stderr-only.
- **Compliance (secret handling)**: S3 reflection must not log field names/values on panic paths; S5 canonicalized paths are still secret-bearing (paths can contain tokens) so denial text stays redacted-metadata-only per FG-005.

## One-way doors (summary)

1. **S5 path canonicalization** - changes `permissions.json` match semantics. Land before v2 GA with dual-match + audit-which-leg. After GA, frozen.
2. **S7 preGrant/flow-grant key change** - changes durable-resume authorization semantics for persisted pause records. Old records fail closed with explicit error. No silent mapping.

All other streams (S1, S2, S3, S4, S6, S8) are reversible pure-code changes: no data-model, public-API, dependency, or auth change.

## Assumptions (lane-wide)

- ADK `AfterTool` `(nil, nil)` passthrough and `(nil, err)` error-carrier semantics per `docs/hooks/spec.md:128-133` hold; C1 handling unchanged.
- Label stays narrowing-only (`Meet`/fold); no widening op is added by any S-stream.
- v2 remains experimental-gated (`flowExperimental`) until T2.1+T2.2; S5 lands inside that window.
- Session-less callers keep v1 parity (StartLabel) when no v2 policy is installed; nil-fail-closed (S4) is v2-scoped.

## Conditions of approval

1. S5 ships with dual-match + fired-leg audit + canonicalization fixture matrix before any production v2 rule authoring.
2. S7 refuses `preGrant` for `FlowReason != ""` and fails closed (explicit error) on flow-less legacy pause records under v2.
3. S6 implements version-compare fast path + preserves source/egress split; no unconditional withhold of source admissions.
4. S4 scopes nil-fail-closed to `FlowEnabled()`; session-less v1 behavior byte-identical (`flow_parity_test` green).
5. S8 gates per recipient in both Telegram and Discord push loops, read-only (no fold), throttled audit.
6. S3 ships with depth/count/byte caps + `recover()` + admission microbench within budget.
7. S1+S8 land in the same release (no window where answers are gated but pushes are not).
