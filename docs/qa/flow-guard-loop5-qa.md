# QA Report - Flow Guard Loop 5 (S9-S15 fixes)

Branch: `develop` (`/home/amurru/Projects/hakase`).
Working tree at verification: 42 modified tracked files + 23 untracked (loop 5 lane), HEAD `4906121`.
Inputs named in task (Blueprint `ses_e9fcfd...`, Architecture `ses_e9fcae...`, Diffs x4, Reviews x4) are
opencode session IDs with no local reader in this environment (Code Mode `execute` exposes only
Lightpanda/Context7/DeepWiki/opencode-worktree tools, no session-content reader). Coverage below is
verified directly against the working tree diff + test sources + live `go test` execution. Reviewer
comments (Medium 1, Medium 2, Low 3-8) are recorded as gaps from code inspection, not as blocks.

## 1. Test strategy

Pyramid:

- Unit (pure): `ShellLine`/`ShellResources`/`FlowArgsHash` identity, `canonicalFlowTool`/`canonicalGrantTool`/
  `canonicalFileTool`/`canonicalShellTool` single-identity, `StringSlice` coercion, `parseClarifyChoices`
  type-switch, `validateClarifyRequest` fail-closed, `FlowEnabled` install matrix.
- Integration (registry/store/ADK): `CheckPreFlow`/`CheckPreFlowAsk` Ask + single-identity binding,
  `taskResolve`/`taskResolveWithAction`/`AuditSystemCommandPathsWithLabel` nil fail-closed matrices,
  `tryConsumeFile/ShellApproval` one-shot, `delegationCacheKey*` label binding, `MergeDelegateLabel`
  Meet-only + merge-on-hit, `grantFlowCall` canonical mirror, `flowShellAskBinding` S14.
- Transport/e2e: `TestProductionDelegateApproveAdmitsOnce` (cmd/hakase production wiring:
  `init.go FlowLabelFunc` -> narrowed delegate child -> `CheckPreFlow` Ask -> canonical grant ->
  `BuildExecCommandFor` admits once, second denies), channel S10/S12/S13 redaction (prompt + push +
  clarify + bridge), POSIX `packaging/install.sh` hermetic harness (10 cases).
- Full suite green gate: `gofmt -l` (clean), `go vet ./...` (clean), `go test ./...` (all ok),
  `-race` on concurrency/binding subset (clean), POSIX harness (10/10 pass).

Quality gates: single `ShellLine` identity shared by gate+audit+binding; nil fail-closed only under
`FlowEnabled()`; Meet-only narrowing everywhere; unknown-sink fail-closed; `mode=allow` never
auto-approves a flow Ask; redact-only never withholds (S10/S12/S13); v1 parity byte-identical.

## 2. Test plan - loop 5 requirement mapping

Every case: requirement/risk, test data, env, prereqs. All executed PASS (see section 5).

### search_files single identity (High loop 5)

- Requirement: `search_files` pre-dispatch binds `("search_files","grep",PathResources)` and the
  sandbox `taskResolveWithAction(..., "grep", ...)` consumes the same triple. Generic `grep` spelling
  hits the same grant (no divergent second key). One-shot.
- Risk: split read/grep identity lets an approval for one spelling bypass the other.
- Cases:
  - `TestApproveSearchFilesToSandbox` (`internal/agent/flow_loop4_test.go:91`): v2 `grep * requires
    public`, narrowed session, `CheckPreFlowAsk("search_files", {path:docs, pattern:foo})` ->
    `true/false/ask`; asserts `HasFlowBinding(search_files, FlowArgsHash(search_files,grep,resources))`,
    asserts `HasFlowBinding(grep, same-hash)` (single identity), consumes, asserts one-shot.
    Env: `HAKASE_FLOW_EXPERIMENTAL=1`. Prereq: clean registry + cold binding store.
  - `TestSearchFilesApprovedAskAdmits` (`internal/sandbox/flow_h1_test.go:51`): unapproved grep Ask
    denies; `GrantFlowBinding(search_files, grep-hash)` admits once; reuse denies.
  - `TestShellCandidatesBothSpellings` (sandbox): candidate list covers both exec spellings.
- Acceptance: PASS (executed).

### POSIX gate (shell identity + audit)

- Requirement: gate, path audit, and binding share one `permissions.ShellLine(command, args)` identity;
  `FlowArgsHash("system_exec","shell",ShellResources)` derived from the same decision. Bare vs
  `--flag` lines are distinct bindings. `approval.mode=allow` never auto-approves a flow Ask.
- Risk: `command` vs `command+args` divergence lets `echo hi` approval cover `echo hi --flag`.
- Cases:
  - `TestShellLine` (`internal/permissions/shell_line_test.go`): bare/explicit-args/empty/whitespace.
  - `TestShellIdentityTwoRules` (`internal/sandbox/flow_s9s14_test.go:151`): `shell:echo hi allow` vs
    `shell:echo hi --flag deny`; bare lookup allow-no-detail, flag lookup deny; flow-ask binding hash
    equals `FlowArgsHash(system_exec, shell, ShellResources(cmd,args))`; bare-vs-flag hashes differ;
    base ask binds empty (mode=allow still applies).
  - `TestFlowShellAskBinding` (same file:103): v1 binds empty; v2 base-ask binds empty; v2 flow-ask
    binds `(reason, hash, version)`; deny binds empty.
  - `TestShellAuditApprovedAskAdmits` (`internal/sandbox/flow_h1_test.go:86`): unapproved shell Ask
    denies; local-decision admits; deny stays deny even with approval.
  - POSIX harness `packaging/install_test.sh` (10 cases, all pass): amd64/arm64 happy path, --version
    pin, SHA256 mismatch, Darwin/MINGW/unknown-arch refusal, sudo fallback, 404 no-asset, --help.
- Acceptance: PASS (executed, incl. harness `All ... passed`).

### production e2e (`TestProductionDelegateApproveAdmitsOnce`)

- Requirement: production `FlowLabelFunc` (cmd/hakase `init.go` wiring) resolves the live narrowed
  delegate child; pre-dispatch sees flow Ask; canonical grant admits at the sandbox exactly once.
- Risk: unit-tested halves disagree in production (session vs delegate-state wrap order, tool spelling).
- Case (`cmd/hakase/flow_loop5_test.go:26`): v2 `shell * requires public`; narrowed parent; delegate
  child ctx + session-carrying + sandbox-pinned; asserts `FlowLabelFunc == internal`;
  `CheckPreFlow(system_exec, echo hi) == true/true/ask`; mints `GrantFlowBinding(system_exec, shell,
  ShellResources)`; `BuildExecCommandFor` first admits, second denies (one-shot consumed).
- Acceptance: PASS (executed).

### post-run merged cache (S11/M5)

- Requirement: delegation cache key binds `(goal, session, label)` under v2 (bare goal under v1);
  post-run entry lands under the merged terminal label; next call at the narrowed label hits without
  re-running and Meets the taint into the parent; cross-session isolation.
- Risk: widest-label result served to narrowed label (taint escape) or merged taint lost (re-ask loop).
- Cases:
  - `TestDelegationCacheKeyLabelBinding` + `TestDelegationCacheLabelIsolation` +
    `TestDelegationCacheCrossSessionIsolation` (`flow_s9s15_test.go`): v1 bare-goal, v2 distinct
    start/narrowed/nil keys, version move changes key, cross-session keys differ.
  - `TestDelegationCachePostRunKeyHit` (flow_s10) + `TestDelegationCacheMergedPostKeyHit`
    (flow_loop4:233) + `TestDelegationCacheMergeOnHit` (flow_s9s15:192): post-run entry under merged
    label, `MergeDelegateLabel` narrows parent, idempotent re-merge does not bump version.
  - `TestDelegateHandlerPostRunMergedEntryHits` (`delegate_loop5_test.go:26`, M3): handler with
    `delegateSubRunOverride` tainting child to suspicious; first call runs once, parent merges to
    suspicious, post-run entry under merged label carries suspicious, second call hits cache
    (`calls == 1`, same TaskID+Summary).
- Acceptance: PASS (all executed).

### clarify type-switch (M4 loop 5)

- Requirement: choices decode fail-closed with explicit type switch; every entry inspected; wrong-typed
  or empty entry fails instead of reaching the gate with a dead list; empty question and
  `multi_select`-without-choices fail.
- Risk: dead choice list reaches the human gate, or provider-serialized form crashes the tool.
- Cases:
  - `TestParseClarifyChoicesTypeSwitch` (`clarify_loop5_test.go:9`): nil->nil; `[]any{a,b}` ok;
    `[]any{a,1}` fails; `[]any{a,""}` fails; `42` fails; `"solo"`->`[solo]`; `[]string{x}` ok.
  - `TestValidateClarifyRequestFailClosed` (same file:34): empty question fails; empty choice fails;
    multi_select-without-choices fails; valid multi_select passes.
  - `TestStringSliceUnmarshal` + `TestStringSliceInvalidArrayErrors` (`util/stringslice_test.go`):
    array/empty/null/stringified-array/plain-string/commas-kept/numbers/nulls-skipped/top-number;
    malformed raw array errors.
- Acceptance: PASS as written, with two reviewer-noted gaps carried below (4-cap unenforced,
  StringSlice pre-coercion bypasses the switch in `registerClarifyTool`).

### Guardrails

| Guardrail | Verdict | Evidence (executed) |
|-----------|---------|---------------------|
| nil fail-closed | PASS | `TestTaskResolveNilFailClosedMatrix` (fileops nil denies unmapped, mapped allow passes, start passes, v1 nil passes); `TestAuditPathsNilFailClosedMatrix` (audit helper nil denies, start passes, v1 passes); `TestTaskResolveS9Matrix` (narrowed+nil deny, start passes, v1 passes); `TestTaskResolveApprovedAskAdmits` (unapproved Ask denies). Scoped to `FlowEnabled()` by construction (`flowLabelFromCtx` + `withFlowLabelFunc` v1 legs). |
| Meet-only (narrowing) | PASS | `TestDelegationCacheMergeOnHit`, `TestDelegationCacheMergedPostKeyHit`, `TestDelegationCachePostRunKeyHit` (merge narrows, idempotent re-merge no version bump); delegate inherit/merge pre-existing (`TestDelegateFlowInheritAndMerge`, `TestRegistryMergeAtomic`). No widening op observed. |
| FlowEnabled passthrough | PASS | `TestFlowEnabledInstallMatrix` (nil/v1 x Install/InstallLayered -> false; v2 x both -> true); `TestV1ParityNoFold` (v1 `CheckPreFlow` passes, version unchanged); v1 legs in every S9 matrix above; `TestPushFanoutV1Parity` x2 (both transports deliver all under v1). |
| unknown-sink fail-closed | PASS | `TestCheckPreFlowUnknownSinkFailClosed`, `TestGateFlowUnknownSink`, `TestCheckChannelEgressUnregisteredUnknownSinkWithholds` (pre-existing, green in full suite); S9 unmapped reads deny on narrowed/nil; unknown `web_*` spellings stay unknown (only exact `web_search_fallback` mapped, pinned by `TestFlowMapToolWebSearchFallback`). |

## 3. Coverage gaps (non-blocking, incl. reviewer notes)

1. **Reviewer Medium 1a - clarify 4-cap unenforced (gap, not blocking).** `ClarifyInput`/`ClarifyRequest`
   document "up to 4", but neither `validateClarifyRequest` (`clarify.go:80`) nor `parseClarifyChoices`
   (`clarify.go:100`) checks `len > 4`. A 5-choice question passes validation today. Suggest one case
   (5 choices must fail) or a documented decision that the model contract, not the gate, enforces 4.
2. **Reviewer Medium 1b - StringSlice coercion bypasses the type switch (gap, not blocking).**
   `registerClarifyTool` (`clarify.go:195`) decodes via `parseClarifyChoices([]string(input.Choices))`:
   `input.Choices` is already `util.StringSlice` (tolerant JSON coercion: stringified arrays, numbers,
   nulls-skipped), so the `[]any`-wrong-type and `string`-single legs of `parseClarifyChoices` are
   unreachable from the tool path - they are only reachable by direct unit calls. The fail-closed shape
   holds, but the "explicit type switch guards the gate" claim overstates the live path. Suggest either
   threading the raw JSON value into the switch or narrowing the comment to "defense in depth for
   direct callers".
3. **Reviewer Medium 2 - canonical identity in 4 places (gap, not blocking).** The single-identity map
   (`system_exec_start->system_exec`, `grep->search_files`) is duplicated: `canonicalFlowTool`
   (`agent/flowgate.go:136`), `canonicalGrantTool` (`agent/flowremedy.go:84`, thin wrapper today but a
   second name), `canonicalFileTool` + `canonicalShellTool` (`sandbox/flow.go:82,119`), plus
   `candidateToolsForAction` spellings. Behavior is pinned from both sides (agent grant tests +
   sandbox consume tests + `TestFlowArgsHashCanonical` + `TestShellIdentityTwoRules`), so divergence
   would be caught, but a single shared helper (e.g. in `permissions`) would remove the drift surface.
   No action this lane; note for next fix-up.
4. **Known gap (carried): `system_exec_start` shares the `system_exec` binding identity.**
   Noted in CHANGELOG S9-S14 entry; `TestFlowArgsHashCanonical` pins the current shape
   (`H1 == H2` across spellings). Any future split of the two tools needs a binding-migration test.
5. **Deferred S12 push-volume counting.** Per CHANGELOG, push-volume audit counting is deferred; current
   S12 tests pin redact-only + still-deliver, not volume. Not blocking.
6. **Perf budget numbers (advisory).** No new hot-path walker in this lane; S3/S6 budgets from prior
   lanes still want CI-recorded numbers. Not blocking for S9-S15.

Low 3-8 reviewer notes: no additional testable assertions identified in the working tree beyond the
items above; nothing in Low 3-8 contradicts the PASS verdicts. Flagged here as reviewed, no block.

## 4. Acceptance validation (FG-001..FG-006, falsifiable)

- FG-001 label + fold: PASS. `Meet`/`FoldLabel`/`FlowKey`/CAS/binding/registry-concurrent/merge-atomic/
  ctx/delegate-inherit + loop5 `MergeDelegateLabel` narrowing + post-run merged entries all pinned and
  executed green.
- FG-002 schema: PASS. `Compile` v2 gate, empty-requires rejection, v1 parity, layering monotonic,
  URL normalize, canonical table incl. `web_search_fallback`, `FlowEnabled` Install/InstallLayered
  matrix (`TestFlowEnabledInstallMatrix` executed).
- FG-003 pre-dispatch: PASS. Exfil-deny/webfetch-ask/unknown-sink, gate_flow escalation/unknown/@-file/
  v1-parity, TOCTOU parallel fold, channel egress incl. unregistered, python gate, plus loop5
  `search_files` single identity + POSIX `ShellLine` two-rule identity + production delegate e2e
  (all executed).
- FG-004 post-admission: PASS. Error redact+fold, withhold-tainted-egress, passthrough/V1 parity,
  sidekick/vision same path, web-search-folds-suspicious (prior lane, green in full suite); no
  regression observed in this lane's full run.
- FG-005 remedies: PASS. Denial text + recomputed notice, Ask-human mode-allow/headless/single-use/
  no-reset, sanitize narrow-only, persisted approval/clarify summary redaction (`flow_s10` H2/M3
  tests green in full suite), S10/S12/S13 redact-only still-deliver (channel + bridge tests green).
- FG-006 persistence/audit/replay: PASS. Flow pointer + applied-set omitempty, recompute conservative,
  persist/sync/load round-trips, CSV dual-parser, `flow_blocked/sanitized/error_redacted/recomputed`,
  `HAKASE_FLOW_DEBUG` stderr-only, 6 fixtures + replay + e2e (prior lane, green in full suite).

Definition-of-done: every bullet maps to an executed test above; no bullet unmapped.

## 5. Test report (executed in this lane)

- Targeted loop-5 filter (agent+sandbox+permissions+session+util+channel+sse+cmd): all PASS, incl.
  `TestParseClarifyChoicesTypeSwitch`, `TestValidateClarifyRequestFailClosed`, `TestStringSlice*`,
  `TestApproveSearchFilesToSandbox`, `TestGrantFlowCallCanonicalTool`,
  `TestApproveExecToSandboxBothExecTools`, `TestSearchFilesApprovedAskAdmits`,
  `TestTaskResolveNilFailClosedMatrix`, `TestTaskResolveApprovedAskAdmits`,
  `TestShellAuditApprovedAskAdmits`, `TestFlowShellAskBinding`, `TestShellIdentityTwoRules`,
  `TestTaskResolveS9Matrix`, `TestShellLine`, `TestDelegationCache*` (5), `TestV1ParityNoFold`,
  `TestEventBridge*` (16), `TestProductionDelegateApproveAdmitsOnce`,
  `TestDelegateHandlerPostRunMergedEntryHits`, `TestDelegationCacheMergedPostKeyHit`,
  `TestFlowLabelForSandboxLiveChild`, `TestDelegateSandboxApproveThreading`,
  `TestFlowCanonicalWindowsGateLevel`, `TestCheckPreFlowAskDownstreamGrant`.
- Full suite: `go test ./... -count=1` -> **all packages ok** (39 lines, zero FAIL).
- Race subset: `-race` on
  `TestImportMeet|TestRegisterLoadedVsFold|TestSyncUnions|TestFlowEnabledInstallMatrix|TestFlowMapToolWebSearchFallback|TestCheckPostFlowWebSearchFoldsSuspicious|TestRegistryConcurrent|TestRegistryMergeAtomic|TestFlowParallelFoldRace`
  across session/permissions/agent -> **ok** (3/3 packages).
- Hygiene: `gofmt -l .` clean (no output); `go vet ./...` clean (no output).
- POSIX harness: `bash packaging/install_test.sh` -> **10/10 pass** (`All ... passed`).
- Pre-existing failure (not this lane, handed to Codebase Doctor): `go test -race -run
  TestAuditForwardPostsEntries ./internal/agent/` -> **FAIL with race detected** (httptest server
  goroutine vs test runner; also fails on clean tree per prior QA lanes). Reproduction command above.
  Full suite without `-race` is green, so this does not block the loop-5 verdict.

## 6. Verdict

**QA-PASS (conditional).** All loop-5 acceptance items (search_files single identity, POSIX gate,
production e2e, post-run merged, clarify type-switch, guardrails nil-fail-closed/Meet-only/
FlowEnabled-passthrough/unknown-sink-fail-closed) map to present, shape-correct, **executed-green**
tests. Merge conditions: (a) CI runs `go test ./...` green (done locally here); (b) reviewer confirms
Medium 1 (4-cap + StringSlice) and Medium 2 (canonical x4) are accepted as documented gaps, not blocks;
(c) pre-existing `TestAuditForwardPostsEntries -race` stays a Codebase Doctor follow-up with the
reproduction in section 5. No code was modified in this lane.
