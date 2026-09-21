# Execution Plan: MCP client upgrade to 2026-07-28

Feature: `mcp-2026-07-28`
Source of truth: `spec.md` (atomic specs MC-001..MC-008). This file
sequences the work into phases with exit criteria and parallelization.
Date: 2026-09-20. Scope: client upgrade + elicitation gates + CIMD OAuth +
`skill://` serving; no hakase-as-public-server auth, no sampling support,
no roots/logging migration (deprecated primitives we never used).

## Phases

### Phase 0 - Decisions (D1+D2 CLOSED 2026-09-20, D3-D5 open)

- [x] **D1** Bump both: ADK `v2.4.0` + go-sdk `v1.7.0` in one PR, letting MVS
  cascade (`genai` v1.63.0 -> v1.70.0 per ADK's `go.mod`; Go toolchain
  1.26.5 -> 1.26.6). Audit 2026-09-20 found no blockers, only
  test-covered behavior deltas: `Event` consistent JSON encoding (v2.2.0
  #1252), missing session reported as `session.ErrNotFound` (v2.4.0 #1458;
  hakase's own store in `internal/session/session_store.go` is unaffected),
  llmagent mode resolved per placement not mutated shared agent (v2.4.0
  #1267; delegation tests will catch fallout), bounded thought-only turns
  (v2.3.0 #1290; interacts with loop guards), non-text MCP results reported
  instead of dropped (v2.4.0 #1473/#1401; hakase delegates `Run`, so this
  is a fix, not breakage). New additive surface (compaction, A2A, adkrest
  auth) is unused by hakase. Recorded in spec.md MC-001. Governs MC-001.
- [x] **D2** Hand-build on plain resources: go-sdk v1.7.0 ships **no**
  skills helpers (verified: zero `skill` symbols in `mcp/`, `auth/`,
  `oauthex/`; ADK's `tool/skilltoolset` is client-side `load_skill`
  consumption, not serving). So `skill://<path>/SKILL.md` + siblings via
  `resources/list` + `resources/read`, index as `skill://index.json`
  well-known resource. No `skills/list` method exists to target. Recorded
  in spec.md MC-006. Governs MC-006.

- [x] **D3** URL-mode elicitation: surface the URL as a clarify prompt,
  user opens it out-of-band, tool continues without blocking (user-confirmed
  2026-09-20). Recorded in spec.md MC-003. Governs MC-003.
- [x] **D4** `AuthorizationCodeFetcher` per surface: localhost listener +
  auto-open browser for CLI/TUI, web-UI-mediated fetch in serve mode,
  link push for Telegram (user-confirmed 2026-09-20). Recorded in spec.md
  MC-005. Governs MC-005.
- [x] **D5** `hakase mcp serve` stdio command (user-confirmed 2026-09-20).
  Recorded in spec.md MC-006. Governs MC-006.

Exit: Phase 0 CLOSED 2026-09-20 (D1+D2 evidence-closed, D3-D5
user-confirmed). Record outcomes in spec.md.

### Phase 1 - Dependency bump (serial, after D1)

**MC-001** go-sdk v1.7.x + ADK v2.4.0.
- Exit: `go build ./...` + full baseline green; old/new x stdio/HTTP
  smoke matrix passes; `/mcp` status correct.

Sequence: D1 -> bump -> fix fallout -> smoke matrix. Nothing else starts
until this is green (everything depends on the SDK surface).

### Phase 2 - Elicitation (after Phase 1; needs D3)

Run in parallel:

- **Track A: MC-002** Per-server client + handler skeleton (decline-closed
  stub first, no gate wiring). Exit: elicitation-bearing tool reaches the
  stub and completes with decline; legacy servers unaffected.
- **Track B: MC-003 design** Schema -> gate mapping table + session
  propagation path (`agent.Context` -> hakase session). Exit: mapping
  reviewed against real elicitation payloads from a fixture server.

Then serial: wire Track A through Track B design; web + Telegram + TUI e2e.
- Exit: acceptance criteria of MC-003; headless fail-closed test.

### Phase 3 - OAuth (after Phase 1; needs D4; parallel with Phase 2)

- **Track C: MC-004** Config schema + validation + example + tests.
  Exit: `go test ./internal/config/...` green, example documented.
- **Track D: MC-005** Runtime handler + token store + localhost redirect.
  Exit: mock-AS e2e, restart persistence, issuer-mismatch rejection.

Sequence: MC-004 -> MC-005 (runtime reads the schema). Merge order with
Phase 2 is free; both touch `buildMCPServerToolset`, so coordinate the
final shape of that function (client + transport built together).

### Phase 4 - skill:// serving (after Phase 1; needs D2, D5; parallel with 2+3)

**MC-006** Server package + serve path + foreign-client round-trip.
- Exit: python/TS client reads index + one skill byte-identical; invalid
  skills skipped, collisions first-wins.

### Phase 5 - Hardening + lock-in (after Phases 2-4)

- **MC-007** Header composition audit, `server/discover` probe,
  `subscriptions/listen` status check, TTL behavior.
- **MC-008** Full compat matrix in tests, CHANGELOG entries, `tasks.md`
  ticked in the landing PRs (roadmap convention 2: no 21-box drift
  repeat), issue #17 closed only when tasks.md, docs, and code agree
  (convention 4).

## Parallelization summary

After Phase 1: Phase 2 (A+B), Phase 3 (C->D), Phase 4 proceed
independently. The only shared edit region is
`buildMCPServerToolset` (Phase 2 needs `Client:`, Phase 3 needs
`OAuthHandler:` on the transport) - land one after the other or agree the
combined signature up front.
