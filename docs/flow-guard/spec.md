# Spec: Flow Guard — session data-flow labels in `permissions.json` v2

Governing issue: #124 (phases: #125, #126, #127). Ecosystem reference: OpenAPPA APPA model (arxiv 2607.24625).

Inspiration line: Inspired by OpenAPPA APPA model (Agentic Permissions Policy Algebra), paper arxiv 2607.24625, MIT (c) 2026 Archestra Inc. No OpenAPPA code copied, no TOML, no sidecar. Clean-room Go implementation inside Hakase.

**Scope:** add a per-session `(trust x audience)` label that only narrows, plus optional `delta` / `requires` fields on `permissions.json` rules. Pre-dispatch check can escalate to ask/deny, post-admission check can withhold/redact before context injection. Sinks v1: `shell`, `read/edit/glob/grep`, `webfetch`, `git push`, `python_interpreter`, `download_file`, `ask_sidekick`, `save_skill`, `vision`, `send_message` + MCP egress. Non-goals: full IFC lattice, effects DAG, child attestation, multi-tenant deployments, batteries, sidecar.

Security audit (2026-10-19, C1-C2/H1-H5/M1-M5/L1-L3) is applied below. Previous gaps closed: error-path leak, sink coverage, sanitizer auto-allow, TOCTOU, double-fold/widen, delegate bypass, layering widen.

## Decisions

- **Own schema, not TOML.** Extend `internal/permissions/permissions.go:58-90` (`Rule`, `Policy`) with optional `delta` / `requires` (+ `sanitizable`). `Version` 0/1 unchanged behavior, `version 2` enables flow fields. Unknown keys stay strict errors (`policy_source.go` loader). No `appa.toml`, no new config file.
- **Two-level trust, three-level audience only, ceiling semantics.** `trust: trusted(0) < suspicious(1)`, combine by `max` (narrow-only, monotone descent). `audience` is an ordered ceiling (`self=0 < internal=1 < public=2`), combine by `min` (narrow-only). `requires.audience_contains: ["public"]` means `label.Audience >= Public`. One order and one combine op per dimension, matching `Meet` and the `webfetch` taint behavior. No set-intersection type; ceiling covers the Hakase exfil shape without general lattice machinery.
- **Label lives with the session, not the policy.** In-memory per-run registry with `sync.RWMutex` + CAS on `Version` (same discipline as `internal/session/pauses.go:63` `PauseRegistry`, needed because TUI+web share state and ADK can run read+send concurrently) + persisted `Session.Flow`/`FlowApplied` fields on `Session` (`internal/session/session_data.go:48-69`) so resume/durable-replay keeps restrictions. History already replays from store, label replays the same way. No Postgres, no separate event log. Label travels in `agent.Context` for delegates (H4), not only by session-ID map.
- **v2 stays experimental until all sinks land (senior review).** Partial taint is worse than no taint. When the `flowExperimental` gate is disabled, `version: 2` policies are rejected at load (fail-closed, loud error naming the gate) so no v2 rule can authorize dispatch without its flow checks. Operator visibility: startup log line + `hakase doctor` / `/api/ready` reports `flow: disabled (experimental gate)`. Until T2.1+T2.2 complete, docs mark v2 experimental.
- **Flow can only escalate, never widen.** Evaluated after hard-deny/deny-patterns/allowlist in `EvaluateCommand` (`internal/agent/gate.go:1034-1120`), same position as current step 4b. A `requires` failure returns Ask (route to existing `ApproveExec`) or Deny per rule `effect`. A plain `allow` without flow fields behaves exactly as today. `RiskUnknown` still always asks (`gate.go:1122-1126`); flow Ask is never auto-approved by `approval.mode=allow` (H5, same fail-closed precedent).
- **Pre-dispatch + post-admission, minimal.** Pre = `LookupWithLabel` before tool runs (hooks `BeforeToolCallback` in `internal/agent/agent.go:131-156` + gate + `mcp/runserver/gates.go` dispatch path, not only the ask branch). Post = `AfterToolCallback` (`agent.go:163-170`) may withhold or redact a result before it enters context. Post never returns a non-nil result override when `runErr != nil` (ADK drops the tool error when AfterTool returns non-nil result, see `docs/hooks/spec.md:128-133`): on error it still redacts + folds, then returns `(nil, redactedErr)` so the failure is preserved with a redacted carrier (C1). Verified against ADK v2.5.0 `base_flow.go:callTool`: `(nil, nil)` falls through to the original `err` and emits raw `{"error": err.Error()}` to the model; `(nil, err)` keeps `{"error": redacted}`. Reference shape:
```go
if runErr != nil {
    redactInPlace(result)
    redactedErr := errors.New(RedactSecrets(runErr.Error()))
    FoldLabel(ctx, deltaFor(t.Name()))
    return nil, redactedErr // preserve the failure with a redacted carrier
}
return CheckPostFlow(ctx, t, args, result)
```
- **Remedies reuse existing machinery.** `withhold` text names the allowed alternative, `ask` reuses `ApproveExec` (`internal/agent/approval.go:50`) + web/Telegram/Discord first-wins cards, `sanitize` reuses `internal/util/redact.go:16-91` typed secret regexes before admission. Sanitizer never auto-allows on a narrowed label (H1): sanitized send still requires Ask unless rule sets `effect:allow` + `sanitizable:true`. No new approval UX, no `attest-schema`, no fan-out spawns.
- **Audit additive, hashes not raw args.** New decisions `flow_blocked` / `flow_sanitized` / `flow_error_redacted` alongside `hook_blocked`/`budget_blocked` (`internal/agent/audit.go:28`, `:185-211`), with `PolicyRule` citation + label snapshot + `argsHash`. Audit path reuses `RedactSecrets`; new CSV columns appended at end only (M2). Metadata only, no file contents or prompts, same 0600 + 5MBx5 rotation.

## Specs

### Spec FG-001: label type + fold (`internal/permissions/flow.go`, new)

- `type Trust int` (`TrustTrusted Trust = iota`, `TrustSuspicious`), `type Audience int` (`AudienceSelf`, `AudienceInternal`, `AudiencePublic`), `type SessionLabel struct { Trust Trust; Audience Audience; Version uint64 }`. `StartLabel() = {trusted, public, 0}` explicit. Zero-value trap: Go zero `{0,0,0}` equals `{trusted,self,0}`, not start. Implementation must use `*SessionLabel` nil-check plus initialized flag everywhere, never `== SessionLabel{}`. Nil = uninitialized, fail-closed (L1). One missed comparison hard-denies all old sessions.
```go
func Meet(a, b SessionLabel) SessionLabel {
    t := max(a.Trust, b.Trust)
    au := min(a.Audience, b.Audience)
    v := max(a.Version, b.Version)
    if t != a.Trust || au != a.Audience || b.Trust != t || b.Audience != au {
        v++
    }
    return SessionLabel{t, au, v}
}
```
- `SessionLabel` (`Trust Trust`, `Audience Audience`, `Version uint64`) + `Fold(label, delta, idemKey)` (H2/H3). Version increments only on narrowing change, CAS under registry mutex (senior review: version check without CAS is a race when ADK runs read+send concurrently).
- `Fold` applies rule `delta` on tool success AND on tool error (conservative fold, C1), never on deny/skip. Unknown tools deny-by-default on narrowed label (C2): if label != start and no rule maps the action, deny explicitly with `flow: unknown action on narrowed label` (Ask/Deny per rule effect, never a `requires` condition, since `audience_contains: ["self"]` passes for every ceiling level). Start-label unknown tools behave as today to preserve v1 parity.
- Idempotency (H3): fold keyed by `(message Sequence, tool_call_id)` applied-set persisted alongside `Flow`. Re-admit of same key is no-op. Version increments only on admitted fold (CAS).
- Child runs (delegate `internal/agent/delegate.go:355-409`): child inherits copy via `agent.Context`, child `BeforeToolCallbacks` resolve label from ctx, never from fresh in-memory session. Child egress denied by parent label. Child returns its terminal `SessionLabel` explicitly via ctx; parent does `Meet(parentLabel, childFinalLabel)`. Transcript hash + `filesModified` are for audit only, never folded directly (senior review: a hash does not give you a label). Rulings never cross the boundary. Forbid `delegate_task` when label is narrowed unless child toolset excludes network egress, else require Ask (H4).

### Spec FG-002: `permissions.json` v2 schema

```jsonc
{"version": 2, "default": "ask",
 "rules": [
  {"action": "read", "resource": "docs/internal/*", "effect": "allow",
   "delta": {"audience": ["internal"]}},
  {"action": "webfetch", "resource": "*", "effect": "allow",
   "delta": {"trust": "suspicious"}},
  {"action": "send_message", "resource": "*", "effect": "allow",
   "requires": {"trust": "trusted", "audience_contains": ["public"]},
   "sanitizable": false}
 ]}
```

- `delta: {trust?, audience?[]}` taints the future. Sends are almost always `requires`, reads/webfetch are `delta`. A `delta` on `send_message` is a smell (tainting after exfil) and rejected in review. `requires: {trust?, audience_contains?[]}` gates dispatch against current label `L ^ delta_of_call`. `sanitizable: bool` (default false) marks rules where a redacted payload may auto-proceed; all other narrowed-label sends still Ask (H1). Starter rule: never set `sanitizable:true` on `send_message *`. Only on narrow resources where secret-shaped is the only risk.
- Validation in `Compile` (`permissions.go:123-149`): `version` 0/1/2 accepted, 2 enables flow keys; 0/1 with flow keys = error. `trust` enum `trusted|suspicious`, `audience` values `self|internal|public`. `requires.audience_contains` non-empty when present; empty `requires:{}` rejected (L2). `requires` union, `delta` most-narrowing union.
- Loader/layering (`policy_source.go:20-77`): user < project (trust-gated) < enterprise, deny > ask > allow. Flow fields merge monotonically (H5): any layer may add `requires`/`delta`, none may remove enterprise entries. `allowManagedOnly` also strips lower-layer rules that drop enterprise `requires`. Project trust-gate applies to flow fields same as base triples.
- Canonical resources (M4), v1 table:
  - `shell` -> raw command line (existing `EvaluateCommand` parsing).
  - `read/edit/glob/grep` -> scoped absolute path via `ResolveScopedPath`.
  - `webfetch` -> normalized URL (lowercase host, strip fragment, keep query for decision, IDNA via `golang.org/x/net/idna` already reachable through `golang.org/x/text` in `session_data.go`, or explicitly defer punycode to follow-up; stdlib `net/url` alone is not enough for homograph `evil.com` vs `evıl.com`).
  - `git push` -> remote URL + branch.
  - `python_interpreter` -> `code_hash` (sha256 of code, first 12 hex in audit) + file-operand extraction at tool-arg parse (`agent.go:608-822` site), best-effort only. Never deny on `@secret` substring alone (false-positive in string literals, false-negative on `getattr(open,...)` indirection).
  - `download_file` -> URL host + path (same normalization as `webfetch`).
  - `ask_sidekick` -> `question_hash`; question pre-checked as egress, answer folded as `suspicious` ingress (H4).
  - `save_skill` -> destination skill path; persists narrowed content, gated as sink.
  - `vision` -> image path/URL + prompt hash; description passes `CheckPostFlow`.
  - `send_message` -> recipient/channel + body hash (never raw body in audit).
- `PolicyRule` audit citation gains `flow: {requires, label, argsHash}` string for `audit.go:41`. Audit uses hashes, never raw args (M2).

### Spec FG-003: pre-dispatch check

- New `LookupWithLabel(label, action, resources...) (Effect, *Rule, string)` in `internal/permissions/`. `Lookup`/`LookupForAgent` keep current signatures and delegate with `StartLabel` so all existing call sites are byte-identical when no v2 rules exist and label is start.
- Wiring order in `EvaluateCommand` step 4b (`gate.go:1103-1120`): after deny-class checks, (a) base triple eval, (b) flow eval if rule has `requires` or label is non-start (unknown-tool fail-closed, C2). Flow Ask/Deny returns `GateDecision{Action, Reason: "flow: <detail> by permissions policy", PolicyRule}`. `RiskUnknown -> Ask` preserved after flow (L2 diagram: hard-deny > base > flow > RiskUnknown > allow).
- Binding (H2): pre-check records `(labelVersion, argsHash)` under registry mutex with CAS; `CheckPostFlow` re-validates `requires` against current label immediately before admission; on mismatch withhold. Parallel `read`+`send` in one turn denied because `send` re-check sees folded label.
- Sinks v1: `shell` via `EvaluateCommand`, `read/edit/glob/grep` via `sandbox/fileops.go` `taskResolve`, path-token audit via `sandbox/systemexec.go` `AuditSystemCommandPaths` (plus `@-file` body operand rule, M4, best-effort per above), `python_interpreter` code gate (`agent.go:608-822` site), `download_file` URL gate (`agent.go:835-927` site), MCP egress via `mcp/runserver/gates.go:23-204` at dispatch (not only inside the ask branch; allowlisted tools that never ask must still hit flow, senior review M3), channel `send_message` via `internal/channel/telegram/send.go` + `internal/channel/discord/push.go` push handlers calling the same gate function, `save_skill` persist gate, `ask_sidekick` question gate + answer fold, vision description via `CheckPostFlow`. `webfetch`/`read` are sources (apply `delta` on success AND error, rarely gate).
- Approval grant (H5): `(tool, argsHash, labelVersion)` one-shot, enforced by patching `ApproveExec` (`internal/agent/approval.go:50-86`) to bypass the `mode==allow => true` shortcut when the Ask carries a flow reason, honoring enterprise `disable_bypass`. Sanitized re-dispatch with different hash needs fresh Ask unless `sanitizable:true` + re-check passes. `approval.mode=allow` never auto-approves flow Ask.
- Nil policy or v1 policy = zero behavior change (existing `nil policy skip` guard preserved).

### Spec FG-004: post-admission withhold/redact

- Extend `makeHookAfterToolCallback` (`agent.go:163-170`) with `CheckPostFlow`: on tool success, compute `Fold(label, delta)`; if result contains secret-shaped bytes per `util.RedactSecrets`, replace with redacted copy and audit `flow_sanitized`; any `delta`-tainted content addressed to `public` without approval is non-admissible (H1, not only secret-shaped): return override `{withheld: true, reason}` so it never enters context. Vision descriptions and `ask_sidekick` answers pass the same path (H4).
- On `runErr != nil` (C1): still run `RedactSecrets` on result + error string, still `Fold` conservative delta, audit `flow_error_redacted` (debug minimum, M5), then return `(nil, redactedErr)` (never a result override, preserves ADK failure semantics). Never silently pass raw errors.
- Label update happens exactly once per admitted `(Sequence, tool_call_id)` (`session_data.go:29-43` kinds), idempotent applied-set, so replay/resume cannot double-fold (H3).

### Spec FG-005: remedies + approval reuse

- Denial text format: `flow: <action> blocked (<trust>/<audience> v<version>). Allowed: <alternative>. Offer: ask-human | sanitize | drop-result`. Shown to model and stored in audit only after `RedactSecrets`; never echo raw resource paths or arg preimages (paths can contain secrets). First `flow_recomputed` denial on a resumed old session adds one-time notice: `note: pre-v2 session resumed with conservative label`. Model sees this as the tool result (same channel as `makeHookBeforeToolCallback` block path, `agent.go:131-140`). Reason is redacted before audit (M2).
- `ask-human` routes through `ApproveExec` fail-closed headless (`approval.go:69-74`) + web pending queue + Telegram/Discord first-wins. Single-use `(tool, argsHash, labelVersion)`: approval authorizes that exact call only, does not reset label.
- `sanitize` runs `util.RedactSecrets` then re-checks `requires` against narrowed payload; auto-proceed only if rule has `effect:allow` + `sanitizable:true`, else still Ask (H1). Audit notes `flow_sanitized` with hashes.

### Spec FG-006: persistence, audit, replay fixtures

- `Session` gains `Flow` (`*SessionLabel`, nil = uninitialized, omitted when nil) plus `FlowApplied` (`map[string]bool`, omitted when empty) (`session_data.go:48-69`). Value structs cannot be nil and `omitempty` does not omit zero structs, so the field must be a pointer (or carry an explicit initialized flag). Idempotency key encoding is canonical and lossless: length-prefixed `seq:<dec>:<byte-len-of-tool-id>:<tool-id>` (tool-id is caller-supplied and may contain any bytes including `:`); `Sequence` comes from `AddMessageWithMetaAndAttachments` (`Sequence=len(Messages)`), persisted in `FlowApplied`, with round-trip encode/decode tests incl. empty tool-id and max-uint64 boundary. Test resume + compaction explicitly: fold, compact, resume, assert `Version` unchanged. `SessionStore` save/load carries both; old files without `flow` recompute by folding stored `tool_result` history, or default `suspicious/internal` + audit `flow_recomputed` + one-time denial notice when history is unavailable (M1). Never default old sessions to widest.
- `AppendSummary`/compaction inherits current label into the summary message and redacts summary text (H3). Resume from summary keeps restrictions.
- Audit: `flow_blocked` + `flow_sanitized` + `flow_error_redacted` + `flow_recomputed` decisions in `audit.go:28` enum + `AuditFlowBlock` helper mirroring `AuditHookBlock`/`AuditBudgetBlock`. CSV row (`audit_chain.go:226`) appends label columns at end only with version header (M2). Compatibility test: old strict parser (`FieldsPerRecord=N`) opens old-format rows (backward compat); new version-aware parser accepts both schemas (forward path). Metadata only. Local repro uses `HAKASE_FLOW_DEBUG=1` logging `argsHash` (never full args) to stderr only, never to file or prod logs.
- Replay fixtures: `docs/flow-guard/fixtures/*.json` scripted `{tool, args, label_before, expect}` + Go test driving `LookupWithLabel` + `Fold` without running tools (mirrors `appa replay` idea, no binary). Fixtures land before engine code (senior review): 3 seed fixtures first (read-private-then-send-public deny, webfetch-then-send ask, v1-parity unchanged). At least one end-to-end `read` then `send` test asserting denial + withhold text (L3).

## Non-goals

- Effects DAG / ordering tokens, `forwards`, gradual `Unknown` casts with ceilings, `attest-schema`, subagent confinement protocol beyond H4 ctx rule, multi-deployment revisions, Postgres event log, OTel policy metrics, marketplace batteries, file taint, `appa yell` loop. Revisit only if v1 utility drops on real sessions. If review asks for `forwards` or `Unknown` casts, point here and open a new FG spec.
- No change to sandbox modes, SSRF guard, hook trust model, channel pairing, web auth. Flow rides those gates, never replaces them. If `system_exec` is denied by sandbox, flow never runs.
- No `delta` on `send_message`. Sends are `requires`, reads/webfetch are `delta`. A `delta` on send is rejected in review.
- Starter v2 rules use ask, not deny, as default flow effect. Deny-by-default on over-narrowed `webfetch *` trains users to disable v2 entirely.

## Definition of done

- [ ] `version 2` policy with sink rule set enforces read-private-then-send-public denial in a Go test, with withhold text naming the alternative.
- [ ] Error-path test: secret in failed tool result is redacted + folded, error still surfaces (C1).
- [ ] Unknown-tool test: narrowed label + unmapped sink asks/denies, start label unchanged (C2).
- [ ] Parallel read+send in one turn denied via post re-check (H2). Resume/compaction tests show no double-fold and no widen (H3).
- [ ] Delegate/sidekick/vision tests: child egress denied, answer folded suspicious, question gated (H4).
- [ ] Layering test: project cannot remove enterprise `requires`, `mode=allow` does not auto-approve flow Ask (H5).
- [ ] No v2 fields = byte-identical behavior; full suite green (`gofmt`, `vet`, `go test ./...`, `pnpm test`).
- [ ] Blocked exfil result never reaches model context (post-admission test asserts override path).
- [ ] Audit shows `flow_blocked` with rule citation + label + argsHash; old-format rows open under old strict parser, both schemas under version-aware parser; `HAKASE_FLOW_DEBUG=1` never writes to file logs.

## Validation against codebase (2026-10-19)

- Engine: `internal/permissions/permissions.go:28-106` (Effect, Rule, Policy), `:123-229` (Compile, Evaluate deny>ask>allow); loader `policy_source.go:20-77`.
- Gate order: `internal/agent/gate.go:1020-1143` (EvaluateCommand 8 steps, 4b policy slot, RiskUnknown fail-closed).
- Callbacks: `internal/agent/agent.go:126-170` (Before/After adaptors), five `llmagent.New` sites + `delegate.go:394-409` sub-agent wiring; python `agent.go:608-822`, download `agent.go:835-927`, sidekick `agent.go:185-202`, save_skill `agent.go:984-1051`.
- Label home candidates: `internal/session/session_data.go:48-85` (Session, Message kinds), `internal/session/pauses.go:63` (registry pattern), `internal/session/session_store.go:26-109` (0600 store).
- Approvals: `internal/agent/approval.go:50-86`, `internal/agent/clarify.go:51-124`, web `handlers/approval.go:208-344`, channel callbacks/push.
- Secrets: `internal/util/redact.go:16-91`, sandbox scrub `systemexec.go:543-577`, sandbox deny lists `sandbox.go:244-285`.
- Audit: `internal/agent/audit.go:16-243`, `audit_chain.go:34-288`, decisions `hook_blocked`/`budget_blocked` precedent.
