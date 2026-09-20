# Task List: MCP client upgrade to 2026-07-28

Feature: `mcp-2026-07-28` (client upgrade + elicitation gates + CIMD OAuth
+ `skill://` serving; no sampling, no public-server auth)
Atomic, hand-offable tasks. Each references the governing spec in `spec.md`
and is sized to 1-3 tool calls + one verification step.
Date: 2026-09-20

Legend: `[BE]` Go backend, `[QA]` test/docs. Status: TODO unless marked.

---

## Phase 0 - Decisions (CLOSED 2026-09-20; see plan.md)

- [x] **T0.1 [QA]** D1: bump both (ADK v2.4.0 + go-sdk v1.7.0, MVS cascades
  genai -> v1.70.0, Go -> 1.26.6); audit found no blockers, only
  test-covered deltas (Event JSON encoding, `session.ErrNotFound`,
  llmagent per-placement mode, thought-only bound, non-text results
  reported). Governs MC-001.
- [x] **T0.2 [QA]** D2: hand-build skills on plain resources
  (`skill://` URIs + `skill://index.json`); go-sdk v1.7.0 has no skills
  helpers, ADK `skilltoolset` is client-side only. Governs MC-006.
- [x] **T0.3 [QA]** D3: URL-mode elicitation surfaces the URL as a
  clarify prompt, no blocking (user-confirmed 2026-09-20). Governs MC-003.
- [x] **T0.4 [QA]** D4: `AuthorizationCodeFetcher` per surface (localhost
  CLI/TUI, web-mediated serve, link push Telegram; user-confirmed
  2026-09-20). Governs MC-005.
- [x] **T0.5 [QA]** D5: `hakase mcp serve` stdio command (user-confirmed
  2026-09-20). Governs MC-006.

---

## Phase 1 - Dependency bump (SK: MC-001) - DONE 2026-09-20

- [x] **T1.1 [BE]** Bump `github.com/modelcontextprotocol/go-sdk` to
  `>= v1.7.0` and `google.golang.org/adk/v2` to `v2.4.0` in `go.mod`;
  fix ADK fallout.
  Verify: `go build ./...`, `go list -m` both modules. Spec: MC-001.
  Outcome: zero fallout; MVS cascaded `genai` -> v1.70.0, Go -> 1.26.6,
  `openai-go` -> v3.54.0, OTel -> v1.46.0.
- [x] **T1.2 [QA]** Smoke matrix: old/new revision x stdio/HTTP servers
  connect + list tools; `/mcp` status sane.
  Verify: `go test ./internal/mcp/...`. Spec: MC-001, MC-007.
  Outcome: new `TestMCPServerManagerProtocolRevisions` covers stateful
  (negotiated 2025-11-25) + stateless (2026-07-28) HTTP in one `Tools()`
  call. Full baseline green: `go test ./...`, `pnpm test` (99 passed).

---

## Phase 2 - Elicitation (Specs: MC-002, MC-003) - DONE 2026-09-20

- [x] **T2.1 [BE]** Per-server `mcp.Client` with decline-closed
  `ElicitationHandler` stub wired into `buildMCPServerToolset`
  (`internal/mcp/mcp_servers.go`).
  Verify: elicitation-bearing fixture tool completes with decline; legacy
  servers unaffected. Spec: MC-002.
  Outcome: `internal/mcp/elicitation.go` (`newElicitingClient`); both stdio
  and HTTP toolsets get the client. `TestElicitingServerCompletesWithDecline`
  drives a real stateless MRTR fixture server end to end.
- [x] **T2.2 [BE]** Schema -> gate mapping + session propagation
  (`ClarifyRequest` with enum choices / free text; confirm-shaped ->
  `ApprovalRequest`; `SessionID` set from asking run).
  Verify: unit tests for boolean/enum/free-text/URL shapes + session
  routing. Spec: MC-003.
  Outcome: `handleElicitation` routes via `interfaces.SessionIDFromCtx`;
  boolean single-property -> approval (`accept{prop:true}` / decline),
  enum -> clarify choices (native value echoed), other primitives -> free
  text with type coercion (SDK validates against the requested schema),
  multi/unknown-property -> decline, url mode -> non-blocking clarify with
  the link (D3). Timeout -> decline, user cancel -> cancel, headless ->
  decline.
- [x] **T2.3 [BE]** Wire handler through gates on all surfaces (web SSE +
  HTTP respond, Telegram keyboards via existing responders, TUI gates);
  headless fails closed.
  Verify: web + Telegram e2e with fixture server; timeout -> decline;
  nil-gate -> decline. Spec: MC-003.
  Outcome: `mcp.SetApprovalGate`/`SetClarifyGate` called in
  `cmd/hakase/web.go` (web gates; Telegram answers them via the existing
  first-responder-wins responders) and `cmd/hakase/main.go` (TUI model).
  Headless covered by `TestHandleElicitationConfirmDeniedOrHeadless`.

---

## Phase 3 - OAuth (Specs: MC-004, MC-005) - DONE 2026-09-20

- [x] **T3.1 [BE]** `MCPOAuthConfig` schema replacing the `oauth` stub
  (`internal/config/mcp_config.go`) + `Validate()` + env precedence +
  `config.json.example` docs.
  Verify: `go test ./internal/config/...` round-trip, validation,
  env-wins. Spec: MC-004.
  Outcome: CIMD (`client_id_url`, https + non-root path enforced) or
  pre-registered (`client_id` + optional `client_secret`) identity;
  `redirect_url`/`scopes`. Web read API redacts `client_secret`. Example
  gained an `oauth-remote` server; the strict example test now asserts it.
  (Env expansion happens at handler build via `config.ExpandEnv`, same
  as stdio argv.)
- [x] **T3.2 [BE]** OAuth runtime: `AuthorizationCodeHandler`
  (CIMD-first) + `OAuthHandler` on the HTTP transport + localhost
  redirect + `~/.hakase/mcp-tokens.json` 0600 persisted store
  (`NewTokenSource`/`InitialTokenSource`).
  Verify: mock-AS e2e connect, restart persistence, refresh,
  issuer-mismatch rejection, static-Headers regression. Spec: MC-005.
  Outcome: `internal/mcp/oauth.go` - token store (flock + 0600 rewrite,
  `channels.json` pattern), localhost redirect listener + best-effort
  browser open + RFC 9207 `iss` (5 min timeout), SEP-2352 issuer recorded
  per entry, per-server handler cache invalidated by config fingerprint on
  `reload()`. `buildMCPServerToolset` wires `transport.OAuthHandler` when
  `oauth` is configured; explicit static `headers` still win over the
  bearer (headerTransport applies after the SDK sets Authorization).
  Tests: `TestOAuthEndToEnd` (mock AS + 401-challenged stateless server,
  one exchange, authorized retry), `TestOAuthTokenPersistence` (restore
  without fetch, store mode 0600), `TestOAuthHandlerCacheInvalidation`.

---

## Phase 4 - skill:// serving (Spec: MC-006) - DONE 2026-09-20

- [x] **T4.1 [BE]** Skill resource server: `skill://<path>/SKILL.md` +
  siblings + index/list from `DiscoverMarkdownSkills`; serve path per D5.
  Verify: foreign client (python/TS) reads index + one skill
  byte-identical; collisions first-wins; invalid skipped. Spec: MC-006.
  Outcome: `internal/mcp/skills_server.go` (hand-built on
  `resources/list`/`read` per D2) + `hakase mcp serve` stdio command
  (`internal/cli/mcp.go`, registered in the dispatcher). The round-trip
  test drives a foreign in-memory MCP client through the index, a
  byte-identical SKILL.md, a supporting file, and the full resources list;
  the collision test proves discovery first-wins. Tests isolate
  HOME/XDG_CONFIG_HOME so host skill dirs never leak in.

---

## Phase 5 - Hardening + lock-in (Specs: MC-007, MC-008) - DONE 2026-09-20

- [x] **T5.1 [BE]** Header composition audit (`Mcp-Method`/`Mcp-Name`
  preserved through `headerTransport`), `server/discover` probe, TTL
  behavior, `/mcp` status across `subscriptions/listen`.
  Verify: `go test ./internal/mcp/...` + new-revision HTTP fixture.
  Spec: MC-007.
  Outcome: `TestHeaderTransportMerges` proves routing headers,
  MCP-Protocol-Version, and Authorization survive the static-header
  RoundTripper (it clones and adds, never drops). `server/discover`
  fallback, `subscriptions/listen`, and TTL are SDK-internal (verified in
  the v1.7.0 release notes); `/mcp` status logic is untouched and covered
  by the existing manager tests including the new revision matrix.
- [x] **T5.2 [QA]** Full compat matrix committed (old/new x stdio/HTTP x
  elicitation/plain), no network/config.json/live servers in tests, no
  `logs/exec-audit.jsonl` or secrets committed; CHANGELOG entries;
  `tasks.md` ticked in landing PRs.
  Verify: `gofmt -l`, `go vet ./...`, `go test ./...`,
  `cd webui && pnpm test`. Spec: MC-008.
  Outcome: matrix = `TestMCPServerManagerProtocolRevisions` (old/new x
  HTTP) + stdio path exercised by the manager suite + elicitation fixtures
  (MRTR stateless) + OAuth mock-AS e2e. All tests self-contained (temp
  dirs, in-memory transports, isolated HOME). CHANGELOG Unreleased entry
  added; this file ticked.
