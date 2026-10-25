# Final Lane QA — Epic #124 Flow Guard (fix-up loop 3/2)

Branch: `develop` (repo `/home/amurru/Projects/hakase`).
Input: `docs/flow-guard/spec.md`, `plan.md`, `tasks.md` (T3.6 ticked),
fix diffs from `ses_ea4cb3ccdffeGfOQBl5IdGgFlR` + `ses_ea49572b8ffeYDfLeJCsZ2yXbe`:
`internal/session/flow.go` ImportMeet single-lock + Sync union,
`internal/agent/flowgate.go` web_search_fallback mapping,
`internal/permissions/flow_enabled_test.go` matrix,
new tests TestImportMeetVerbatimThenMerge, TestRegisterLoadedVsFoldConcurrent,
TestSyncUnionsApplied, TestFlowEnabledInstallMatrix,
TestFlowMapToolWebSearchFallback, TestCheckPostFlowWebSearchFoldsSuspicious.

Execution note: this QA lane could NOT execute `go test` — the sandbox
denied `shell` (`Permission denied: shell`) and the Code Mode `execute`
runtime exposes only Lightpanda tools (no shell/go). All verdicts below
are static (code + test-source read). Counts are by static enumeration.
**CI must run the commands in Test Report before merge.**

## Test strategy

Pyramid: unit (Meet/Fold/key/codec/gate matrix) → integration
(registry/store/ADK pre+post/delegate/channel) → replay/e2e (fixtures +
read-then-send) → full suite green. Quality gate: `gofmt -l`, `go vet`,
`go test ./...`, `pnpm --dir webui test`, plus `-race` on the new
concurrency tests. Pre-existing `TestAuditForwardPostsEntries` race is a
Codebase Doctor follow-up, not blocking (see below).

## Acceptance validation (FG-001..FG-006)

- FG-001 label + fold: PASS. TestMeetMatrix (max/min/version-bump/no-double-bump),
  TestFoldLabelVersion, TestZeroValueTrap (nil fail-closed), TestFlowKeyRoundTrip
  (empty/colon/max-uint64/invalid), TestRegistryFoldIdempotent (CAS + binding),
  TestRegistryConcurrent, TestRegistryMergeAtomic, TestFlowCtx, delegate
  inherit/merge (TestDelegateFlowInheritAndMerge) all present and shape-correct.
- FG-002 schema: PASS. TestCompileV2Gate (fail-closed gate error, v1+flowkeys
  rejected), empty-requires rejected, TestV1Parity, TestLayeringFlowMonotonic +
  TestLoaderFlowV2 (enterprise requires survive, allowManagedOnly strips),
  TestNormalizeURL (fragment/case/IDNA-homograph/IPv6/ArgsHash/ShortHash),
  TestFlowEnabledInstallMatrix (nil/v1/v2 x Install/InstallLayered), canonical
  table incl. TestFlowMapTool + TestFlowMapToolWebRealNames +
  TestFlowMapToolWebSearchFallback (L5/F3) + TestFlowWebfetchRuleHit.
- FG-003 pre-dispatch: PASS. TestExfilDeny, TestWebfetchAsk,
  TestUnknownSinkFailClosed (incl. requires-self always-allow guard),
  TestCheckPreFlowExfilDenied/UnknownSinkFailClosed, gate_flow tests
  (escalates-allow-to-ask, unknown-sink, @-file, v1 parity), TestFlowParallelFoldRace
  (H2 TOCTOU), CheckChannelEgress tests, TestPythonGateFlowDeny.
- FG-004 post-admission: PASS. TestCheckPostFlowErrorRedactFold (nil override +
  redacted carrier + conservative fold, C1), TestCheckPostFlowWithholdTaintedEgress
  (H1), TestCheckPostFlowPassthroughNoPolicy + V1Parity (byte-identical, no redact),
  sidekick answer folds suspicious (in CheckPostFlow), vision/sidekick same path by
  code inspection, TestCheckPostFlowWebSearchFoldsSuspicious (both spellings fold
  to suspicious v1, F3).
- FG-005 remedies: PASS (by test-name + spot-read).
  TestFlowAskHumanApproveProceeds/DenyDenies, TestFlowAskModeAllowNeverAutoApproves,
  TestFlowAskModeAllowHeadlessFailsClosed, TestFlowApprovalSingleUse,
  TestFlowApprovalDoesNotResetLabel, TestFlowSanitizeAutoProceedNarrow,
  TestFlowSanitizeWildcardStillAsks, TestFlowSanitizeNeverRewritesCode,
  TestFlowSanitizedPayloadAsksWithoutTaintClaim, TestFlowDenialTextShape,
  TestFlowRecomputedNoticeOnFirstDenial.
- FG-006 persistence/audit/replay: PASS. TestRecomputeFlow (conservative never
  widest + notice text), TestNewSessionStartsWidest, TestSessionFlowPersist
  (omitempty + round-trip), TestRegisterLoaded, TestStoreLoadRegistersFlow,
  TestFlowResumeCompactionVersionUnchanged, TestFlowRecomputedNoticeOnce,
  TestStoreLoadPreservesLiveFold (C1: Load never clobbers live fold),
  TestFoldFlowLabelSurvivesTurnEndAndResume, TestImportMeetVerbatimThenMerge (F1:
  fresh verbatim/no-bump, merge narrowing-only + applied union),
  TestRegisterLoadedVsFoldConcurrent (F1 deterministic suspicious/internal v6 +
  full applied union), TestSyncUnionsApplied (F2 union + unknown/noop + nil-safe),
  CSV parser tests (TestAuditCSVRow/TestAuditCSVVersionAwareParser),
  TestFlowAuditDecisions, TestFlowDebugStderrOnly, fixture driver
  TestFlowReplayFixtures + TestFlowE2EReadThenSend, 6 fixtures present
  (read-private-then-send-deny, webfetch-then-send-ask, v1-parity-unchanged,
  unknown-sink-fail-closed, sanitized-send-narrow-allow, python-gated-sink-asks).

Definition-of-done 9 bullets: each maps to a test above. No bullet unmapped.

## Fix-up loop 3/2 verification (static)

- F1 ImportMeet single write lock (no check-then-act): PASS by inspection
  (`flow.go:298-323` holds `mu.Lock` across initialized-check + import/merge +
  applied union). Pinned by TestImportMeetVerbatimThenMerge (verbatim v3 fresh,
  merge-with-wider no-widen, 3-key union) and TestRegisterLoadedVsFoldConcurrent
  (32 goroutines Load-vs-Fold, deterministic trust=suspicious/audience=internal/
  version=6 + full key union; reasoning in report: max-version + one narrowing
  bump holds under every interleaving since second merges no-op).
- F2 Sync union: PASS by inspection (`Sync` unions registry-live + sess-carried
  keys; no-op on unknown/nil). Pinned by TestSyncUnionsApplied.
- M2 matrix incl InstallLayered: PASS. TestFlowEnabledInstallMatrix covers
  nil/v1/v2 x Install/InstallLayered (5 assertions + compiled-policy checks).
- L5/F3 web_search_fallback: PASS. `flowMapTool` case `"web_search",
  "web_search_fallback"` → `webfetch` + query/hash resources (spec FG-002 mapping
  note updated); pinned by TestFlowMapToolWebSearchFallback +
  TestCheckPostFlowWebSearchFoldsSuspicious (both spellings fold suspicious v1).

## Coverage gaps (minor, non-blocking)

1. Unknown `web_*` spellings (e.g. `web_search_extra`) have no explicit test
   pinning they stay unknown-sink fail-closed. Only the exact fallback spelling
   is pinned. Suggest one table case.
2. Tainted (secret-shaped) `web_search` result on a narrowed label: `webfetch`
   is not in `isEgressAction`, so the redacted source admits (send gates later).
   No test pins admit-vs-withhold for a secret in a web_search snippet. Suggest
   one post-flow case.
3. No dedicated `-race` Sync-vs-Fold stress asserting version stability;
   covered indirectly (RegisterLoadedVsFoldConcurrent, StoreLoadPreservesLiveFold).
   Optional.
4. Enterprise `disable_bypass` x flow Ask interaction has no flow-named test
   (generic `TestDisableBypass` + flow mode-allow tests exist). Optional.
5. Test execution itself: BLOCKED in this lane (no shell). CI must confirm.

## Test report (static enumeration; NOT executed here)

- `internal/session`: 19 test funcs (flow_test 15 incl. 3 new F1/F2, + store/
  service/snapshot/data/persist). Key: TestImportMeetVerbatimThenMerge,
  TestRegisterLoadedVsFoldConcurrent, TestSyncUnionsApplied,
  TestStoreLoadPreservesLiveFold, TestFlowResumeCompactionVersionUnchanged.
- `internal/permissions`: flow_test 11 + flow_enabled_test 1 (new matrix) +
  fixtures_test 3 + loader/permissions suites (layering/trust/managed-only).
- `internal/agent`: flowgate_test 10 + flow_parity_test 9 (incl. 2 new L5/F3) +
  flowremedy_test ~15 + gate_flow_test 4 + audit_chain_test incl. CSV parsers.
- `internal/agentrun`: no flow-owned tests (driver reuses agent/session gates;
  no gap — flow coverage lives at gate/registry layers).
- Required CI (not run here, shell denied):
  `go test ./internal/agent/ ./internal/session/ ./internal/agentrun/ ./internal/permissions/ -count=1`
  and `-race -run 'TestImportMeet|TestRegisterLoadedVsFold|TestSyncUnions|TestFlowEnabledInstallMatrix|TestFlowMapToolWebSearchFallback|TestCheckPostFlowWebSearchFoldsSuspicious|TestRegistryConcurrent|TestRegistryMergeAtomic|TestFlowParallelFoldRace' ./internal/session/ ./internal/permissions/ ./internal/agent/`.

## Verdict

**QA-PASS (conditional).** All FG-001..006 acceptance criteria map to present,
shape-correct tests including the six fix-up tests; F1/F2/M2/L5/F3 logic is
correct by inspection and determinism arguments hold. Merge condition: CI runs
the Test Report commands green (incl. `-race` on the new tests). Follow-up (not
blocking): pre-existing `TestAuditForwardPostsEntries` race → hand off to
Codebase Doctor with reproduction (`go test -race -run TestAuditForwardPostsEntries ./internal/agent/`).
No code was modified in this lane.
