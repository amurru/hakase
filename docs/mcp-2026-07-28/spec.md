# Spec: MCP client upgrade to 2026-07-28 (go-sdk v1.7+)

Feature: `mcp-2026-07-28`. Source of truth for scope. Companion: `plan.md`
(phases), `tasks.md` (atomic tasks). Specs MC-001..MC-008.
Tracks GitHub issue #17.

Verification baseline: `gofmt -l`, `go vet ./...`, `go test ./...`,
`cd webui && pnpm test`. Remember `make build-frontend` on fresh clones
before Go commands (`internal/web/dist/` is gitignored but required by
`//go:embed`).

---

## MC-001: Dependency bump (go-sdk v1.7.x + ADK v2.4.0)

- **Objective**: Move the MCP wire protocol from spec 2025-11-25 to
  2026-07-28 by bumping `github.com/modelcontextprotocol/go-sdk` from
  `v1.4.1` (`go.mod:20`) to `>= v1.7.0`, and `google.golang.org/adk/v2`
  from `v2.1.0` to `v2.4.0` (the first ADK whose `go.mod` pins go-sdk
  v1.7.0; verified in the local module cache).
- **Affected components**: `go.mod`, `go.sum`, anything that breaks under
  the ADK 2.1.0 -> 2.4.0 delta (audit first; the delta is the main hidden
  cost of this feature).
- **Contracts**: none (dependency-only). Client negotiates the highest
  mutually-supported revision at connect time, so old-revision servers keep
  working via the SDK fallback handshake.
- **Acceptance criteria**:
  - `go list -m github.com/modelcontextprotocol/go-sdk` reports `>= v1.7.0`;
  - stdio + streamable HTTP servers on both old (2025-11-25) and new
    (2026-07-28) revisions connect and list tools;
  - full verification baseline green.
- **Guardrails**: no config-surface change in this spec; no behavior change
  for servers without elicitation/OAuth.
- **Dependencies**: none (enables MC-002..MC-007).

## MC-002: Per-server MCP client with elicitation handler

- **Objective**: Give each managed server its own `mcp.Client` carrying an
  `ElicitationHandler`, instead of letting ADK build a default client from
  a bare transport. This is what makes MRTR elicitation reachable: under
  2026-07-28 a tool call returns `InputRequiredResult` and the SDK fulfills
  `inputRequests` through the client's handler, then retries with
  `inputResponses` + opaque `requestState`.
- **Affected components**: `internal/mcp/mcp_servers.go`
  (`buildMCPServerToolset`, `managedServer`), `internal/agent` gate wiring.
- **Contracts**:
  ```go
  // One client per server, built alongside the toolset.
  client := mcp.NewClient(&mcp.Implementation{Name: "hakase", Version: <ver>}, &mcp.ClientOptions{
      ElicitationHandler: elicitationRouter{server: name}.Handle,
  })
  cfg := mcptoolset.Config{Client: client, Transport: transport}
  ```
  `requestState` is opaque and attacker-controlled per spec: echo
  byte-for-byte, never parse, never let it influence authz logic.
- **Acceptance criteria**:
  - An elicitation-bearing tool on a 2026-07-28 server completes end to end
    (legacy `elicitation/create` stream path keeps working against
    2025-11-25 servers via the SDK compat shim);
  - dead-server cooldown (`failureCooldownBase`/`failureCooldownMax`) and
    per-turn `Tools()` re-evaluation behave as before (no dial storms).
- **Guardrails**: handler runs on the tool-call path - it must block on the
  gate with the gate's own expiry, never indefinitely; nil gate (headless)
  fails closed (decline/cancel, never accept).
- **Dependencies**: MC-001.

## MC-003: Elicitation -> approval/clarify gate routing

- **Objective**: Route `ElicitRequest`s into the existing gate machinery
  (`interfaces.ApprovalGate` / `interfaces.ClarifyGate`) so a remote server
  asking "confirm this delete?" lands in the web UI and Telegram inline
  keyboards with first-responder-wins semantics.
- **Affected components**: new router in `internal/mcp` (or `internal/agent`),
  `internal/web/handlers/approval.go`, `internal/web/handlers/clarify.go`,
  `internal/channel/telegram` (no protocol change - existing
  `RespondApproval`/`RespondClarify` paths), `internal/tui/gates.go`.
- **Contracts**:
  ```go
  // Form elicitation: message + requestedSchema -> clarify.
  interfaces.ClarifyRequest{Question: params.Message, Choices: enumChoices(schema)}
  // Boolean-confirm elicitation (single boolean property, or confirm-shaped
  // message) -> approval.
  interfaces.ApprovalRequest{Tool: "mcp_<server>_<tool>", Command: params.Message, Risk: "unknown", Reason: "mcp elicitation"}
  // ElicitResult{Action: "accept"|"decline"|"cancel", Content: ...}
  ```
  Session routing: propagate the asking run's session (`ApprovalRequest.SessionID`
  / `ClarifyRequest.SessionID`, honoring `WebApprovalGate.promptSession`)
  so channel transports deliver to the bound conversation.
- **Acceptance criteria**:
  - Form elicitation surfaces answerable choices (JSON-schema `enum` ->
    clarify choices; otherwise free text) in web + Telegram;
  - confirm-shaped elicitation surfaces approve/deny in web + Telegram;
  - timeout maps to gate expiry (`ApprovalExpiry` default 60s,
    `ClarifyExpiry` default 120s) and yields decline/timed-out, never accept;
  - TUI surfaces the same prompts via its gates.
- **Guardrails**: elicitation only ever arrives inside a call the agent
  started (spec SEP-2260 forbids unsolicited pushes) - no background
  listener; URL-mode elicitation (D3 closed 2026-09-20) is surfaced as a
  non-blocking clarify prompt containing the URL and logged - never
  blocks the tool call, never errors.
- **Dependencies**: MC-002.

## MC-004: OAuth config schema (replace the phase-3 stub)

- **Objective**: Replace `MCPServerConfig.OAuth map[string]string //
  reserved (phase 3)` (`internal/config/mcp_config.go:21`) with a real CIMD
  client schema.
- **Affected components**: `internal/config/mcp_config.go`,
  `config.json.example`, `internal/config/mcp_config_test.go`.
- **Contracts**:
  ```go
  type MCPOAuthConfig struct {
      ClientIDURL  string   `json:"client_id_url,omitempty"`  // CIMD document URL (preferred)
      RedirectURL  string   `json:"redirect_url,omitempty"`   // default http://localhost:<port>/callback
      Scopes       []string `json:"scopes,omitempty"`
      // Pre-registered fallback (client_id/secret); DCR only when the AS
      // lacks CIMD support.
      ClientID     string `json:"client_id,omitempty"`
      ClientSecret string `json:"client_secret,omitempty"`
  }
  ```
  Env wins over file per house convention (`HAKASE_MCP_OAUTH_*` or
  per-server `${VAR}` expansion via existing `ExpandEnv`).
- **Acceptance criteria**:
  - `Validate()` rejects unknown shapes with actionable errors; absent block
    = unchanged anonymous behavior (byte-identical effective config);
  - JSON round-trip + env-precedence tests mirror existing MCP config tests.
- **Guardrails**: secrets never logged; token material never lands in
  `config.json` (tokens live under `~/.hakase/`, MC-005).
- **Dependencies**: MC-001 (for `auth.AuthorizationCodeHandlerConfig` shape).

## MC-005: OAuth runtime (CIMD end to end)

- **Objective**: Make an OAuth-protected remote server (Slack/Atlassian/
  GitHub-shaped) connectable: `StreamableClientTransport.OAuthHandler`
  backed by `auth.AuthorizationCodeHandler` with CIMD-first registration
  order, RFC 9207 `iss` validation, per-issuer credential binding, and token
  persistence across restarts.
- **Affected components**: `internal/mcp/mcp_servers.go` (transport build),
  new `internal/mcp/oauth.go` (handler factory, localhost redirect,
  `~/.hakase/mcp-tokens.json` 0600 store using `NewTokenSource` /
  `InitialTokenSource` persistence pattern from v1.7.0-pre.3).
- **Contracts**:
  ```go
  handler, _ := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
      ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: cfg.OAuth.ClientIDURL},
      RedirectURL: ..., RequestRefreshToken: true,
      AuthorizationCodeFetcher: <localhost-callback or web-UI-mediated fetch>,
      NewTokenSource: <persist on refresh>, InitialTokenSource: <restore>,
  })
  transport := &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: c, OAuthHandler: handler}
  ```
  The transport retries once after `Authorize()` on 401/403 (SDK contract).
  `AuthorizationCodeFetcher` is per surface (D4 closed 2026-09-20):
  localhost listener + auto-open browser for CLI/TUI, web-UI-mediated
  fetch in serve mode, link push for Telegram.
- **Acceptance criteria**:
  - OAuth-protected server connects end to end against a mock AS in tests
    and a real provider manually;
  - restart reuses persisted tokens (no re-auth); refresh works;
    issuer mismatch is rejected;
  - static `Headers` auth keeps working (no regression for token-in-header
    servers).
- **Guardrails**: token file 0600, flocked like `channels.json`; redirect
  handler binds localhost only; `application_type` set so desktop/CLI
  localhost redirects are not rejected (SEP-837).
- **Dependencies**: MC-001, MC-004. UX for `AuthorizationCodeFetcher`
  (browser open vs web-UI-mediated) is decision D4.

## MC-006: Serve local skills over MCP (`skill://`)

- **Objective**: Expose hakase's discovered markdown skills
  (`internal/skill/skill_discovery.go`: `.agents/.claude/.opencode/.gemini/
  skills`, `skills/`, `~/.hakase/skills`) as MCP resources so another MCP
  host can list and read them (SEP-2640).
- **Affected components**: new `hakase mcp serve` stdio command (D5 closed
  2026-09-20) backed by a new package (e.g. `internal/mcpskills` or
  `internal/skill/mcp_server.go`) reusing `DiscoverMarkdownSkills`.
- **Contracts** (D2 closed 2026-09-20: go-sdk v1.7.0 has no skills
  helpers, so hand-built on `resources/list` + `resources/read`):
  ```text
  skill://<skill-path>/SKILL.md        # entry point, mime text/markdown
  skill://<skill-path>/<file-path>     # sibling files, relative resolution
  skill://index.json                   # JSON index [{name, type, description, url}]
  ```
  Final URI segment equals frontmatter `name`; supporting files are
  siblings; index entries mirror frontmatter `description`.
- **Acceptance criteria**:
  - `skill://index.json` lists every discovered skill; each entry's URL is
    `resources/read`-able by a foreign MCP client (python/TS) with
    byte-identical content to the on-disk `SKILL.md`;
  - name collisions follow discovery first-wins; invalid skills are skipped
    with a warning, never fatal.
- **Guardrails**: read-only surface (no writes to skill dirs); symlinked
  skill dirs resolved as in discovery (no escape outside candidates);
  large bodies (>100KB) warned as today.
- **Dependencies**: MC-001 (server-side resource API). No gate interaction.

## MC-007: Transport hardening for the stateless core

- **Objective**: Adopt the mechanical 2026-07-28 requirements that are not
  covered above: `Mcp-Method`/`Mcp-Name` headers with body-match rejection
  awareness (`-32020`), `server/discover` capability probe, cacheable-list
  `ttlMs` hints honored (fewer re-lists), and migration off
  `subscriptions/listen` predecessors (`tools/list_changed` etc. now ride
  one multiplexed stream the SDK opens on `Connect`).
- **Affected components**: `internal/mcp/mcp_servers.go` (header transport
  composes with routing headers, not clobbers), `/mcp` TUI status view.
- **Acceptance criteria**:
  - New-revision HTTP servers work behind the header contract;
  - `/mcp` status still reports idle/connected/failed/disabled correctly
    across the `subscriptions/listen` change;
  - no per-call re-list storms (TTL honored where the SDK exposes it).
- **Guardrails**: `headerTransport.RoundTrip` must merge headers, never drop
  SDK-set routing headers.
- **Dependencies**: MC-001.

## MC-008: Tests, docs, CHANGELOG

- **Objective**: Lock the feature with a compat matrix, e2e elicitation and
  OAuth tests, skill round-trip interop, and truthful docs.
- **Affected components**: `internal/mcp/*_test.go`,
  `internal/config/mcp_config_test.go`, `config.json.example`,
  `CHANGELOG.md` ("Planned" deferrals if any), this directory's
  `tasks.md` ticked in the landing PRs (roadmap convention 2).
- **Acceptance criteria**:
  - Matrix: old/new revision x stdio/HTTP x elicitation/plain - all green,
    self-contained (temp dirs, no network per testing quirks);
  - no `logs/exec-audit.jsonl` committed; no secrets in fixtures.
- **Guardrails**: tests need no `config.json` or live MCP servers.
- **Dependencies**: MC-002..MC-007.
