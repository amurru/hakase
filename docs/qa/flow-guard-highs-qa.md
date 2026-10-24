# QA Report - Flow Guard Highs Fix-up Lane S1-S8 + Follow-ups

Branch: `develop` (`/home/amurru/Projects/hakase`).
Inputs read directly: `docs/flow-guard/spec.md`, `plan.md`, `tasks.md`,
`docs/architecture/adr-007-flow-guard-highs-fixup.md`,
`docs/qa/flow-guard-final-qa.md`, plus working-tree diff
(`git status`: 30 modified + 9 untracked, the S1-S8 lane) and new tests:
`internal/agent/flow_highs_test.go`, `internal/agent/flow_parity_test.go`,
`internal/agentrun/flow_persist_test.go`,
`internal/channel/telegram/flow_highs_test.go`,
`internal/channel/discord/flow_highs_test.go`,
`internal/permissions/flow_enabled_test.go`,
`cmd/hakase/init_flow_test.go`,
`internal/channel/flowhydrate.go` (shared helper).

Execution note: this lane could NOT execute `go test ./...`.
Both `default.shell` (`Permission denied: shell`) and the Code Mode
`execute` runtime (Lightpanda/Context7/DeepWiki/opencode only, no shell)
deny process execution in this environment. All verdicts below are
static (code + test-source read). Counts are by static enumeration.
CI must run the Test Report commands before merge.

## 1. Test strategy

Pyramid:

- Unit (pure): `Meet`/`FoldLabel`/`FlowKey` codec, `flowCanonicalPath`
  matrix, `flowDetailVersion`, walk caps (`flowWalkMaxStrings/Bytes/Depth`),
  `IsStart`/`IsNilOrStart`, `FlowEnabled` install matrix.
- Integration (registry/store/ADK): `Fold` idempotent + CAS + binding,
  `ImportMeet`/`RegisterLoaded`/`Sync`/`Store.Load` live-fold preservation,
  `CheckPreFlow`/`CheckPostFlow`/`CheckChannelEgress` under v2 vs v1,
  `MergeDelegateLabel` Meet-only, `ApproveExec`/`ApproveFlowAsk`/
  `grantFlowCall`/`consumeFlowGrant` single-use, `ResolveApprovalPause`
  flow scoping, `checkPythonGate` resume round-trip.
- Transport/e2e (channel): Telegram streaming freeze (pre-narrow +
  mid-narrow), Telegram + Discord push fan-out per-recipient, producer
  taint, topics-mode lobby + client-thread lobby, delegation watching
  thread, resume hydration (narrowed withholds + open delivers).
- Full suite green gate: `gofmt -l`, `go vet ./...`, `go test ./...`,
  `pnpm --dir webui test`, plus `-race` on concurrency tests.

Quality gates (per ADR-007 conditions of approval):

1. S5 dual-match + fired-leg audit + canonicalization fixture matrix.
2. S7 refuses `preGrant` for `FlowReason != ""`, legacy pause fail-closed.
3. S6 version-compare fast path + source/egress split preserved.
4. S4 nil fail-closed scoped to `FlowEnabled()`; v1 parity byte-identical.
5. S8 per-recipient gating in both transports, read-only, throttled audit.
6. S3 depth/count/byte caps + `recover()` + microbench within budget.
7. S1+S8 land together (answers + pushes gated in same release).

## 2. Test plan - S1-S8 requirement mapping

Every case: maps to requirement/risk, with test data, env, prereqs.

### S1 - Channel Ask fail-closed (delivery gate, no approval round-trip)

- Requirement: `CheckChannelEgress` treats flow Ask as withhold at
  delivery; channel reply has no `(tool, argsHash, labelVersion)` grant
  to consume, so Ask can never be satisfied there.
- Risk: narrowed session exfils via answer text while tool path asks.
- Implementation: `internal/agent/flowgate.go:1542 CheckChannelEgress`
  (unregistered-session branch `LookupWithLabel(nil,...)`; Ask/Deny
  withhold, bare default passes); call sites
  `telegram/run.go:226`, `discord/run.go:229`.
- Cases:
  - `TestCheckChannelEgressUnregisteredAskWithholds` (S1): v2 policy
    `send_message requires trusted+public`; session `s1-missing-session`
    (never registered). Expects `allow=false`, `reason != ""`.
    Env: `HAKASE_FLOW_EXPERIMENTAL=1`. Prereq: clean registry.
  - `TestCheckChannelEgressUnregisteredUnknownSinkWithholds` (S1):
    v2 policy with only a `read` delta; `send_message` unmapped.
    Expects withhold (nil = narrowest, unknown-sink fail-closed).
  - Guardrail (v1 parity): `TestCheckChannelEgressV1Parity` - v1 policy,
    start + narrowed + missing sessions all `allow=true`.
- Acceptance: PASS (static). Both S1 tests present, shape-correct,
  FlowEnabled-gated.

### S2 - Sticky withhold + streaming freeze

- Requirement: monotonic sticky withhold; `pump()` suppresses answer
  creation/edits while narrowed; status line keeps liveness; `finalize`
  delivers exactly one `⛔` notice; no unsend of streamed prefix.
- Risk: perceived freeze / hang; prefix leak pre-narrow (accepted).
- Implementation: `telegram/run.go` `runView.flowGate` + finalize.
- Cases (all `internal/channel/telegram/flow_highs_test.go`):
  - `TestStreamingFreezeWithholdNoAnswerNoNotice` (name in tree:
    `TestStreamingFreezeWithholdNoAnswerOneNotice`): pre-narrowed
    session, scripted `OnStream` + 120ms dwell. Expects 0 answers,
    exactly 1 `⛔` notice, status line created, `>=2` status edits
    ending `✓`, reactions `[👀 ❌]`.
  - `TestStreamingFreezeMidRunNarrow`: fold between two `OnStream`
    chunks. Expects answer created pre-narrow with `FIRST-CHUNK-AAA`,
    no `SECOND-CHUNK-BBB` in creation or any edit, final edit is `⛔`.
- Acceptance: PASS (static). Liveness + exactly-once notice pinned.

### S3 - Typed-slice redact walker (reflection)

- Requirement: `marshalResultStrings` + `redactResultInPlace` cover
  typed ADK/MCP payloads (`[]string`, `[]map[string]string`, structs,
  named `string`/`[]byte` map types); fast-path type-switch first,
  bounded reflection fallback: max depth 8, max strings 512, max bytes
  1MB; over-budget = tainted + audit (fail-closed); `recover()` guard;
  never follow unexported fields; `IsStart` short-circuit (no walk on
  widest).
- Risk: bypass via typed payloads; hot-path CPU/alloc regression;
  panic on cycles/unexported.
- Implementation: `flowgate.go:691-~1310` walker + caps + gates.
- Cases (`flow_highs_test.go`):
  - `TestMarshalTypedSlicesCollectStrings` / `TestRedactTypedSlicesAndMaps`
  - `TestReflectStructRedactSkipsUnexported` (exported redacted,
    unexported untouched, pointer struct redacted, no panic)
  - `TestWalkCycleTerminates` (self-referential map -> over-budget both legs)
  - `TestWalkOverBudgetFailClosed` (600-line bulk on narrowed label ->
    `withheld=true`)
  - `TestIsStartShortCircuitAdmits` (start admits, still redacts)
  - `TestRedactNamedMapTypesNoPanic` (M1: named types, Convert not assert)
  - `TestIncompleteRedactSourceWithholds` (M1: over-budget source on
    narrowed label withholds, never partial-admit)
  - `TestWalkCapsCountAndBytes` (M2: 600 strings -> over + truncate to
    512; 1MB+1 byte -> over both legs)
  - `TestIsStartSkipsReflection` (M2: 600-string bulk on start admits)
  - `BenchmarkCheckPostFlow1MBReadResult` (ADR perf input, ~1MB payload)
- Acceptance: PASS (static). Caps, unexported skip, cycle termination,
  IsStart gate, fail-closed over-budget all pinned. Perf budget itself
  needs CI numbers (see Gaps).

### S4 - Delegate nil fail-closed (v2-scoped)

- Requirement: `parentFlowForDelegate`: `""` -> StartLabel always
  (session-less = no principal, v1 parity); registered -> copy;
  non-empty unregistered + v2 -> nil (child evaluates nil as
  narrowest); + v1 -> StartLabel. `WithDelegateFlowState(nil)` seeds
  narrowest (`suspicious/self`) under v2, StartLabel otherwise.
  `MergeDelegateLabel` Meet-only.
- Risk: over-applying to session-less v1 breaks parity; under-applying
  widens child beyond parent.
- Implementation: `delegate.go:47`, `flowgate.go:60`, `flowgate.go:1584`.
- Cases:
  - `TestParentFlowForDelegateMatrix` (v1 both seed start; v2
    session-less start, unregistered nil, registered start copy)
  - `TestWithDelegateFlowStateNilMatrix` (v2 nil -> narrowest, else start)
  - `TestDelegateNilChildEgressDenied` (nil child `send_message` blocked;
    start child at start passes)
  - Pre-existing: `TestDelegateFlowInheritAndMerge`, `TestFlowCtx`,
    `TestRegistryMergeAtomic` (Meet-only, atomic).
- Acceptance: PASS (static). Session-less vs unregistered distinction
  pinned per ADR note.

### S5 - Path canonicalization (ONE-WAY DOOR)

- Requirement: `flowCanonicalPath`: Clean + absolute + best-effort
  `EvalSymlinks` (nearest-ancestor fallback); never errors (raw leg
  fallback); `~`/Win32-alias/ADS/trailing-dot folding; dual-match
  resources `[canonical raw]` (collapse when identical); audit which
  leg fired (debug-gated).
- Risk: `../`/symlink/CWD-relative bypass; match-semantics change is
  one-way (must land before v2 GA with dual-match).
- Implementation: `flowgate.go:132-~505` + `flowPathResources` +
  `flowAuditPathLeg`; deliberate non-reuse of
  `sandbox.ResolveScopedPath` documented in comment.
- Cases:
  - `TestFlowCanonicalPathMatrix` (dotdot cleans, trailing slash
    folded, nonexistent write target `resolved=false` + keeps tail,
    empty -> `"",false`)
  - `TestFlowCanonicalSymlink` (existing symlinked file resolves,
    `resolved=true`, exact real path)
  - `TestFlowMapToolDualMatchLegs` (relative -> 2 legs, primary abs;
    absolute clean -> 1 leg)
  - `TestFlowCanonicalDenyClosesBypass` (absolute deny rule catches
    `flowtest-dir/../flowtest-bypass.txt`)
  - `TestFlowRawLegFallback` (relative rule matches raw leg; pre-check
    does not fold by itself)
  - `TestFlowAuditPathLegNoPanic` (debug-gated, no panic incl. single-leg)
  - `TestFlowCanonicalHomeAndWinAlias` (H4: `~`, `\\?\`, ADS, trailing
    dot/space, CWD anchoring)
- Acceptance: PASS (static). Matrix + dual-match + fallback present.
  Merge-order condition (land before v2 GA) is process, not code.

### S6 - TOCTOU unconditional re-check (conditional-unconditional)

- Requirement: pre-check records `(labelVersion, argsHash)` binding;
  post-admission re-validates `requires` when label moved
  (`postEntryVersion != preCheckVersion`); version-match = fast-path
  skip (one integer compare); missing/evicted binding + narrowed =
  re-validate fail-closed; source/egress split preserved (sources
  admit redacted + audit, egress withholds); one audit row per
  withheld admission.
- Risk: false-withhold of benign sources; double policy-eval overhead.
- Implementation: `flowgate.go:1314 CheckPostFlow` (binding read
  before fold, `bindingMissing`, S6 block `1462-1475`).
- Cases:
  - `TestCheckPostFlowToctouWithholdsCleanSend` (send passes pre-check
    at start, parallel read folds, post withholds clean send)
  - `TestCheckPostFlowNoMoveAdmitsCleanSend` (no fold -> admits, fast path)
  - `TestCheckPostFlowToctouAdmitsCleanSource` (moved clean `read_file`
    admits)
  - `TestBindingMissingNarrowedWithholds` / `TestBindingMissingStartAdmits`
    (M3: missing binding fail-closed only when narrowed)
  - `TestBindingEvictedFailsClosed` (M3: fill 5000 past 4096 cap, evicted
    send withholds)
  - `TestFlowParallelFoldRace` (pre-existing H2 TOCTOU).
- Acceptance: PASS (static). Fast path + split + missing/evicted
  handling all pinned.

### S7 - FlowReason + preGrant scoping (ONE-WAY DOOR, durable-resume wire)

- Requirement: `consumePreGrant` never satisfies `FlowReason != ""`;
  flow pause detail carries `flowReason/flowArgsHash/flowLabelVersion`
  under v2 (empty keys for non-flow v2 pauses to mark newness; absent
  keys = legacy); legacy record + v2 -> explicit fail-closed error,
  record kept; flow record installs exact `(session, tool, argsHash,
  labelVersion)` one-shot flow grant; `mode==allow` bypass disabled
  for flow (ordered after pre-grant rejection); `grantFlowCall`/
  `consumeFlowGrant` one-shot; `flowDetailVersion` tolerant decode.
- Risk: restart-resumed approval bypasses flow Ask once per stale record.
- Implementation: `approval.go:50-115`, `resume.go:398-508`,
  `flowremedy.go:72-189`, `agent.go:664` single consume, `init.go:82`
  + `sandboxApprovalRequest` forwarding, `tui/gates.go:43` binding.
- Cases:
  - `TestApproveExecFlowSkipsPreGrant` (pre-grant present, flow ask ->
    headless error, grant NOT consumed)
  - `TestResolveApprovalPauseLegacyFailsClosedUnderV2` (legacy detail
    -> error naming `flow`, record kept)
  - `TestResolveApprovalPauseFlowInstallsFlowGrant` (flow detail ->
    drive=true, grant installed, one-shot consume, second has=false)
  - `TestApprovalPauseDetailKeys` (v1 no keys; v2 flow keys exact; v2
    non-flow carries empty keys as newness marker)
  - `TestFlowDetailVersion` (uint64/int/float64/string/nil/bogus/neg)
  - `TestFlowPythonAskResumeRoundTrip` (H2: ask carries binding, pause
    round-trip installs grant, headless re-drive consumes, repeat asks)
  - `TestSandboxApprovalRequestForwardsFlow` (H3 init bridge)
  - `TestFlowAskHumanApproveProceeds/Denies`,
    `TestFlowAskModeAllowNeverAutoApproves`,
    `TestFlowApprovalSingleUse`, `TestFlowApprovalDoesNotResetLabel`
    (pre-existing FG-005).
- Acceptance: PASS (static). Both one-way conditions pinned.

### S8 - Push fan-out taint (Cron/Task/Delegation per-recipient)

- Requirement: gate inside fan-out loop per recipient (both transports),
  read-only (no fold, never sticky); producer taint (payload rides
  producer session) AND recipient taint both withhold; lobby inherits
  narrowest thread binding (topics + client-created threads);
  delegation fans out per watching thread; hydration on cold registry
  (one bounded load per session per process); throttled audit.
- Risk: narrowed answer correctly withheld but cron/delegation notice
  exfils to N recipients.
- Implementation: shared `channel/flowhydrate.go:23 HydrateFlowLabel`,
  `telegram/push.go:237 flowEgress`, `discord/push.go:175 flowEgress`,
  per-recipient + producer + thread-scan call sites.
- Cases (Telegram 10 + Discord 3):
  - `TestPushFanoutPerRecipientGating` (both transports: narrowed
    skips, open delivers 2)
  - `TestPushFanoutV1Parity` (both: no v2 -> all deliver)
  - `TestPushFanoutProducerNarrowedWithholds` (both + Telegram open
    producer control `TestPushFanoutProducerOpenRecipientNarrowed`)
  - `TestPushFanoutTopicsModeNarrowedThread` + causality (unbind ->
    delivers 1)
  - `TestPushFanoutLobbyClientThreadNarrowedWithholds` + causality
  - `TestRunningDestinationsKeepsThreads` (collapse fix)
  - `TestDelegationEventNarrowedWatchingThread` (per-thread fan-out)
  - `TestPushFanoutResumeHydratesNarrowed` (cold registry withholds,
    ledger once, history intact) + `TestPushFanoutResumeHydratesOpenDelivers`
    (control: open delivers, proves hydration runs; boundedness)
  - Discord: read-only assertion (push never folds narrowed label).
- Acceptance: PASS (static). Both transports + producer + thread +
  delegation + hydration + boundedness pinned.

## 3. Follow-ups H-1/H-2/M-1 + findings 1-5

Task names `H-1/H-2/M-1 + findings 1-5` without blueprint text in-repo.
Mapping below is best-effort from test/code comments; ambiguous items
are flagged untestable-as-written per QA rules.

- H-1 (replay-check gates both grant paths): `resume.go:486` comment
  `H-1: gate both grant paths on assertResumable`; covered by
  `TestFlowPythonAskResumeRoundTrip` shape (pause -> grant -> re-drive)
  but no dedicated never-replay-blocked test naming H-1 in this lane.
  Status: PARTIAL - logic present by inspection, no H-1-named test.
- H-2 (python flow ask resume binding): `TestFlowPythonAskResumeRoundTrip`
  pins argsHash + labelVersion capture, pause round-trip, one-shot
  consume. Status: PASS.
- H-3 (sandbox approval forwards flow binding): `init.go:82-84`,
  `sandboxApprovalRequest`, `tui/gates.go:43`; pinned by
  `TestSandboxApprovalRequestForwardsFlow`. Status: PASS.
- H-4 (canonicalizer aliases): `TestFlowCanonicalHomeAndWinAlias`
  pins `~`, `\\?\`, ADS, trailing dot/space, CWD anchoring. Status: PASS.
- M-1 (named-type redact + incomplete-redact withhold):
  `TestRedactNamedMapTypesNoPanic` + `TestIncompleteRedactSourceWithholds`.
  Status: PASS.
- M-2 (walk caps during-walk + IsStart skip): `TestWalkCapsCountAndBytes`
  + `TestIsStartSkipsReflection`. Status: PASS.
- M-3 (binding missing/evicted): three tests listed in S6. Status: PASS.
- Findings 1-5 (inferred from final diff summary
  `channel/flowhydrate.go shared helper, telegram/discord flowEgress,
  comment fix, init.go + sandbox/flow.go nil fail-closed, agent.go
  single consume, tests OpenDelivers + LobbyClientThread + boundedness`):
  1. Shared hydrate helper (one implementation, both transports) -
     PASS (`flowhydrate.go` + both `flowEgress` call it).
  2. Nil fail-closed (`init.go:61-75` + `sandbox/flow.go:26-48` mirror
     `flowLabelForCtx`) - PASS (both return nil on
     present-but-unregistered + FlowEnabled).
  3. Single consume (`agent.go:664` python path consumes flow grant
     once like `ApproveFlowAsk`) - PASS by inspection + H2 test.
  4. OpenDelivers control test - PASS
     (`TestPushFanoutResumeHydratesOpenDelivers`).
  5. LobbyClientThread + boundedness - PASS
     (`TestPushFanoutLobbyClientThreadNarrowedWithholds` +
     `flowHydrated.done` once-per-session assertions in both resume tests).
  - UNTESTABLE-AS-WRITTEN: the numeric `findings 1-5` list has no
    in-repo definition (no blueprint session text). The mapping above
    is inferred from the diff summary in the task prompt; CI reviewers
    should confirm numbering against the blueprint sessions
    `ses_ea1e6142...`, `ses_ea0e652d...`, `ses_ea07d0af...`,
    `ses_ea052944...`.

## 4. Acceptance validation FG-001..FG-006

| FG | Verdict | Evidence |
|----|---------|----------|
| FG-001 label + fold | PASS | `Meet` max/min/version, `FoldLabel`, zero-value/nil fail-closed (`IsNilOrStart`), `FlowKey` round-trip (empty/colon/max-uint64), `Fold` CAS + binding, `RegistryConcurrent/MergeAtomic`, `FlowCtx`, delegate inherit/merge, `ImportMeet/Sync/RegisterLoaded` (F1/F2/C1) |
| FG-002 schema | PASS | `Compile` v2 gate, empty `requires:{}` rejected, `IsStart`/v1 parity (`flow_parity V1Parity` x4), layering monotonic + `allowManagedOnly`, URL normalize, canonical table incl. `web_search_fallback`, `FlowEnabled` Install/InstallLayered matrix |
| FG-003 pre-dispatch | PASS | `CheckPreFlow` escalation-only, `ExfilDeny/WebfetchAsk/UnknownSink` (requires-self guard), `CheckPreFlowV1Parity/AskV1Headless`, `Toctou` trio + M3 trio, `CheckChannelEgress` incl. unregistered, `checkPythonGate` flow deny/ask + H2 round-trip |
| FG-004 post-admission | PASS | `CheckPostFlow` error redact+fold `(nil, redactedErr)` (C1), withhold tainted egress (H1), passthrough nil-policy/v1, sidekick/vision same path, `WebSearchFoldsSuspicious`, S3 walker caps + IsStart gate, M1 incomplete-redact withhold |
| FG-005 remedies | PASS | `flowDenialText` shape + redacted-metadata-only, recomputed notice once, `ApproveExec` pre-grant skip + mode-allow never auto-approves + headless fail-closed, single-use grants, label never reset, sanitize narrow-only |
| FG-006 persistence/audit/replay | PASS | `Flow` pointer + `FlowApplied` omitempty, `RecomputeFlow` conservative + notice, persist/sync/load round-trips, `FoldFlowLabelSurvivesTurnEndAndResume`, `PersistAgentResponsePreservesFlowLabel`, `StoreLoadPreservesLiveFold`, CSV dual-parser, `flow_blocked/sanitized/error_redacted/recomputed`, `HAKASE_FLOW_DEBUG` stderr-only, 6 fixtures + `TestFlowReplayFixtures` + `TestFlowE2EReadThenSend` |

Definition-of-done 9 bullets: each maps to a test above. No bullet unmapped.

## 5. Guardrails

| Guardrail | Verdict | Evidence |
|-----------|---------|----------|
| Nil fail-closed | PASS | `flowLabelForCtx` nil on present-but-unregistered; `CheckPostFlow` nil-label branch (tainted-only withhold); `CheckChannelEgress` nil branch (non-allow withholds); `sandbox/flow.go flowLabelFromCtx` nil under FlowEnabled; `init.go FlowLabelFunc` nil likewise; `WithDelegateFlowState(nil)` narrowest under v2 |
| Meet-only (narrowing) | PASS | `Meet` max-trust/min-audience; `Merge` + `ImportMeet` + `MergeDelegateLabel` all via `Meet`; fresh-import verbatim / merge never wider pinned |
| FlowEnabled passthrough | PASS | `CheckPreFlow`, `CheckPostFlow`, `CheckChannelEgress`, `flowLabelFromCtx`/`flowLabelForCtx`, `parentFlowForDelegate`, `WithDelegateFlowState` all branch on `FlowEnabled()`; v1 parity tests x4 (`PreFlowV1Parity`, `PostFlowV1Parity`, `ChannelEgressV1Parity`, `PushFanoutV1Parity` x2) |
| Unknown-sink fail-closed | PASS | `LookupWithLabel(nil,...)` + narrowed-label unknown action denies; `TestUnknownSinkFailClosed` + `TestCheckPreFlowUnknownSink` + `TestCheckChannelEgressUnregisteredUnknownSinkWithholds`; unknown `web_*` spellings stay unknown (only exact `web_search_fallback` mapped) |

## 6. Coverage gaps (minor, non-blocking except execution)

1. Perf budget numbers missing: `BenchmarkCheckPostFlow1MBReadResult`
   exists but no recorded p50 vs pre-lane baseline; ADR budget (<5%
   on 1MB read) needs CI numbers before merge.
2. Unknown `web_*` spellings (e.g. `web_search_extra`) have no explicit
   test pinning they stay unknown-sink fail-closed; only the exact
   fallback spelling is pinned.
3. Secret-shaped `web_search` snippet on narrowed label admit-vs-withhold
   has no dedicated post-flow case (source admits redacted; send gates
   later - by design, unpinned).
4. H-1 (assertResumable on both grant paths) has logic + comment but no
   H-1-named never-replay-blocked test in this lane.
5. Audit-volume behavior (one row per withheld admission, throttled
   per-recipient push withholds) is asserted by code shape, not by a
   counting test.
6. TEST EXECUTION BLOCKED (carried from prior lane): shell denied, so
   `go test ./...` could not run here. Pre-existing
   `internal/hooks TestAuditForwardPostsEntries` race noted in prior QA
   remains a Codebase Doctor follow-up, not blocking.

## 7. Test report (static enumeration; NOT executed here)

- `internal/agent`: `flow_highs_test.go` 25 tests + 1 benchmark,
  `flow_parity_test.go` 8, `flowgate_test.go` ~10, `flowremedy_test.go`
  ~15, `gate_flow_test.go` 4, `resume_driver_test.go` + 3 new S7 cases,
  `audit_chain_test.go` CSV parsers.
- `internal/session`: `flow_test.go` 16 (incl. F1/F2/C1 trio),
  store/service/snapshot/data/persist suites.
- `internal/permissions`: `flow_test.go` 11 + `flow_enabled_test.go` 1
  (Install/InstallLayered matrix) + `fixtures_test.go` 3 + loader suites.
- `internal/channel`: telegram 10 S2/S8 tests, discord 3 S8 tests,
  `flowhydrate.go` shared (no direct unit; exercised via both resume tests).
- `internal/agentrun`: `flow_persist_test.go` 1 (C1 turn boundary).
- `cmd/hakase`: `init_flow_test.go` 1 (H3 forwarding).
- Required CI (not run here, shell denied):
  `gofmt -l . ; go vet ./... ; go test ./... ; pnpm --dir webui test`
  and `-race` on
  `TestImportMeet|TestRegisterLoadedVsFold|TestSyncUnions|TestFlowEnabledInstallMatrix|TestFlowMapToolWebSearchFallback|TestCheckPostFlowWebSearchFoldsSuspicious|TestRegistryConcurrent|TestRegistryMergeAtomic|TestFlowParallelFoldRace|TestPushFanout|TestStreamingFreeze|TestDelegationEvent|TestBinding|TestWalk|TestRedact|TestMarshal|TestParentFlow|TestWithDelegate|TestApproveExecFlow|TestResolveApprovalPause`.

## 8. Verdict

**QA-PASS (conditional).** All S1-S8 acceptance criteria map to present,
shape-correct tests including the follow-up hardening (shared hydrate
helper, both-transport `flowEgress`, nil fail-closed in `init.go` +
`sandbox/flow.go`, single consume in `agent.go`, OpenDelivers +
LobbyClientThread + boundedness tests); FG-001..006 and the four
guardrails hold by inspection with deterministic arguments intact
(version +6 merge, union applied sets, one-shot grants, per-recipient
gating). Merge conditions: (a) CI runs the Test Report commands green
incl. `-race` on the concurrency/binding/push tests; (b) reviewer
confirms the `findings 1-5` numbering against the blueprint sessions
(see section 3 untestable-as-written note); (c) record the S3 1MB
microbench numbers against the 5% budget. Follow-up (not blocking):
pre-existing `TestAuditForwardPostsEntries` race -> hand off to
Codebase Doctor with reproduction
(`go test -race -run TestAuditForwardPostsEntries ./internal/hooks/`).
No code was modified in this lane.
