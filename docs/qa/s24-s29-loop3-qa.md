# S24-S29 Loop-3 QA Report (Full-Lane Quality & Testing)

Branch: `develop` (repo `/home/amurru/Projects/hakase`).
Inputs: `docs/flow-guard/s24-s29-blueprint.md`, `docs/flow-guard/s24-s29-inventory.md`,
`docs/architecture/adr-009-s24-s29.md`; prior QA `ses_e9528190bffevioHvpl2TdRpKV`;
reviews `ses_e949605e8ffed3KUlaTxHnNgZc` (Highs), `ses_e9442856effeMkce4MJm0rJ6Hc`
(loop-2 impasse), `ses_e93f21b6bffewzZtFZy3W4N3K1` (Approve-with-comments, Mediums 1-5).
No code edited (QA lane only).

## Test strategy

Pyramid: unit (scoped-file confine/excl/nofollow, joint-hash bind, neutralize pure
function, pip pin/hash/venv, deny-first ordering) -> integration (agent dual mint/consume,
resume twins, S29 AST scan + choke round-trips) -> full `go test ./...` green.
Quality gates: `gofmt -l` clean, `go vet` clean, `GOOS=darwin go build ./...` clean,
`go test ./...` green, targeted `-v` enumeration of all 29 index names.

## Test report (executed this loop)

- `go test ./internal/sandbox/ ./internal/permissions/ ./internal/agent/ -run
  'TestS2[456789]|TestV1Delta' -count=1`: PASS (all three packages ok).
- Verbose enumeration: **38/38 PASS** - S24 x7, S25 x6 (+2 V1Delta), S26 x5,
  S27 x7, S28 x7 (incl. DenyBeforeStat, NestedSkillsConfined hardening extras),
  S29 x3. Full list in tool output; zero FAIL.
- `go test ./... -count=1`: PASS - all 38 packages ok (incl. cmd/hakase,
  internal/agent, internal/sandbox, internal/permissions).
- `gofmt -l internal/ cmd/`: clean (no output). `go vet` on the three lane
  packages: clean. `GOOS=darwin go build ./...`: clean.
- `-race` probe: `go test ./internal/agent/ -run 'TestAuditForward' -count=1 -race`
  FAILS with data race in `TestAuditForwardPostsEntries` (httptest server goroutine
  vs `ConfigureAuditForward` global). Pre-existing, out of S24-S29 lane.
  **Hand-off to Codebase Doctor** with repro command above; not caused by lane files.

## Acceptance validation (ADR-009 Conditions 1-7)

- C1 Scoped writes only (S24): PASS. 5 blueprint tests + 2 rootless hardening extras
  green; `TestS29ScanInventory` green (no unjustified raw writes).
- C2 Joint binding / dual-accept / fresh approval (S25): PASS. All 6 S25 tests +
  `TestS25AgentDualRecords` + both V1Delta register tests green.
- C3 Both tag families, no span over 32 (S26): PASS. 4 blueprint tests + nested-tag
  extra green.
- C4 Exact pins / require-hashes / transitive / .venv integrity (S27): PASS.
  All 7 tests green (incl. SingleVenvRootCwdDiverged).
- C5 Deny-first + skills confinement, :2182 closed (S28): PASS. 5 blueprint tests +
  DenyBeforeStat + NestedSkillsConfined green.
- C6 Scan + inventory lands last (S29): PASS. All 3 S29 tests green.
- C7 Lane-wide invariants: PASS by construction (Meet-only scan green, passthrough
  covered by flow_enabled/parity tests, hash-only pinned by TestS25HashOnlyNoRaw).
- Scope checks: scoped exclusive writes, joint-hash v2 dual-accept, neutralize
  both-tags, pip pin+hashes+integrity, deny-first + confinement, scan+inventory -
  all verified present and green.

## Coverage gaps (reviewer Mediums 1-5, non-blocking unless asked)

- M1 root-resolution escape downgrade: covered by `TestS24ParentSymlinkEscapeDenied`
  + `TestS24RootlessParentSymlinkDenied` (both PASS). Downgrade concern closed
  unless reviewer re-raises.
- M2 dead per-task root: covered by `TestS29ChokeInventory` row "S24 confined root
  fallback non-empty" (EffectiveWriteRoot fallback + explicit-root-wins, PASS).
- M3 helper not fixed path: `s24-allow:` justifications present in
  `internal/agent/agent.go` (lock, CreateTemp) and `internal/agent/audit.go`
  (lock, append-only audit); S29 scan accepts them and is green. No unjustified
  helper write found.
- M4 v1 requires dropped: pinned by `TestV1DeltaRegisterUnregisteredInert`
  (stock v1 byte-identical) + `TestS27NoV1BreakBareName` (both PASS).
- M5 loader branch untested: no new loader-branch test in this lane beyond
  `TestLoaderFlowV2` (prior lane); flagged as known gap, non-blocking per review.
- Extra: `audit_forward` race under `-race` (see above) - Doctor hand-off, not a
  lane test failure under the standard `go test ./...` gate.

## Verdict: QA-PASS (conditional)

All 29 inventory index names green (plus 9 hardening extras), all 7 ADR-009
conditions satisfied, `gofmt`/`vet`/`darwin-build`/`go test ./...` green.
Ship conditions: none new. Carry: (a) audit_forward `-race` to Codebase Doctor
(repro above), (b) Mediums 1-5 recorded as non-blocking gaps unless a reviewer
explicitly re-opens one. Governing issue #124 stays open per blueprint.
