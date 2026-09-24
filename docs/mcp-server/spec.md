# Spec: MCP server mode — hakase runs as MCP tools (#45)

Expose hakase **runs** as MCP tools so another agent (Claude Code, Codex, any
MCP host) can drive hakase as a sub-agent. `codex mcp-server` is the
precedent; the issue (#20, first item) scoped it as a facade: run/session
listing + prompt + stream events + gate responses over MCP. All of the
hard parts already exist — `agentrun.Driver` is the transport-neutral turn
loop the web chat and Telegram drive, and go-sdk v1.8 (MCP 2026-07-28, #17)
provides server-initiated elicitation, which is what makes hakase's gate
system reachable from a remote driving agent.

Governing issue: amurru/hakase#45 (tier-2 backlog item 1 of #20).

## Decisions

- **Facade, not a new loop.** The `run` tool persists the user turn with
  `RecordUsageInSession` and calls `agentrun.Driver.RunTurn` exactly like the
  web chat handler (`handlers/chat.go:700`) and Telegram (`telegram/run.go:98`)
  do. No MCP-specific run logic: sessions it creates are first-class hakase
  sessions, visible/resumable from the web UI and phone.
- **Gates ride SEP-2322 input-required round trips.** Hakase's
  `ApprovalGate`/`ClarifyGate` are implemented over MCP's multi-round-trip
  mechanism: a mid-run gate ask suspends the `run` tool call with an
  `input_required` result carrying the elicitation as an InputRequest; the
  driving client fulfills it (its human answers) and retries the call, which
  resumes the suspended run. This is not a stylistic choice — protocol
  2026-07-28 (SEP-2322/SEP-2575) forbids standalone server-initiated
  elicitation outright, and go-sdk v1.8 enforces it. The SDK fulfills the
  same InputRequests transparently for pre-2026-07-28 clients, so one code
  path serves both. When the client cannot fulfill (no elicitation support)
  the SDK aborts the call and the run's gates fail closed on their expiry —
  the headless contract ("headless processes never install these and fail
  closed in internal/mcp"), now with a better error. Remote-MCP elicitation
  (hakase as *client*, `mcp.SetApprovalGate`) chains onto the same gate.
- **Stdio, same command.** `hakase mcp serve` keeps its zero-config
  skills-only default; `--agent` opts into the run tools on the same stdio
  connection (server name `hakase` instead of `hakase-skills`). One MCP
  entry for hosts: tools + `skill://` resources together. HTTP (streamable)
  transport is deliberately out of scope for v1 — it needs the auth story
  that the web server already owns; revisit when there is a multi-client use
  case.
- **The run tool is synchronous.** MCP tool calls are request/response; the
  driving agent wants hakase's answer as the tool result. Stream deltas and
  activity lines go out as MCP progress notifications (`notifications/progress`
  with the call's progress token) so hosts can show liveness and keep long
  runs from being reaped. Client cancellation cancels the run through the
  request context — the same path web SSE consumers get by cancelling the
  HTTP stream.
- **Bootstrap lives in package main.** The agent-flavored serve needs the
  full `agent.Deps` wiring (vision resolver, media setup — main-only
  helpers), so it follows the web/serve/tui pattern: `internal/cli` exposes
  an injection hook, `cmd/hakase/mcpserver.go` provides the real handler.
  `internal/mcp/runserver` itself stays a plain library (no main, no cli
  imports).
- **Trust model unchanged.** Stdio MCP is local trust (same as the skills
  server): the server runs with the user's config, model keys, sandbox, and
  home directory. No new network surface, no new auth. Sandboxing applies to
  the runs themselves exactly as on every other transport.

## Specs

### Spec MS-001: package `internal/mcp/runserver` — server surface

`runserver.Mount(srv *mcp.Server, deps Deps)` adds three tools to any
`*mcp.Server` (the skills resources stay independent of it):

- `run` — args `{prompt string (required), session_id string (optional),
  timeout_seconds int (optional)}`. Result JSON
  `{session_id, status, answer, thinking, activity, tokens, error}`.
- `list_sessions` — args `{include_archived bool, limit int}`; result
  `{sessions: [SessionSummary...]}` (`session.SessionSummary` marshals as-is).
- `get_session` — args `{session_id string, limit int}`; result
  `{id, title, project_id, project_name, updated_at, messages: [...]}` with
  each message `{sequence, role, content, thinking, kind, timestamp,
  attachments}`.

`Deps` carries a minimal runner seam (`interface{ RunTurn(ctx, sessionID,
*genai.Content, agentrun.EventSink) }` — `*agentrun.Driver` satisfies it, tests
inject a fake), the `*session.SessionService`, the gates, and a logger.

### Spec MS-002: `run` semantics

1. Resolve the session: `session_id` set → load it (unknown id ⇒ tool error,
   never a silent fork); unset → create a fresh session titled from the
   prompt (one-shot semantics; the result's `session_id` resumes it later).
2. Persist the user turn via `RecordUsageInSession(sessionID, "user", ...)`
   (web/TUI/Telegram contract — `handlers/chat.go:615`).
3. One run per session at a time: a per-session tracker refuses a second
   `run` with an actionable error (Telegram's `TryStart` model), and a
   process-wide cap (8 concurrent runs) guards the host.
4. The run goroutine executes on a context detached from the tool call
   (`context.WithoutCancel` + the timeout bound) because a gate round trip
   ends the call's request while the run continues; the driver is labeled
   `Transport: "mcp"` for tracing. Client cancellation of the *original*
   call is a no-op by then (the result already left); cancelling a retry
   leaves the run running and its output persisted — the `timeout_seconds`
   bound is the server-side stop mechanism in v1.
5. The per-call sink (`EventSink`) accumulates answer/thinking text and a
   bounded activity buffer (last 200 lines, each ≤ 500 chars); every stream
   delta and log line becomes a progress notification when the call carries a
   progress token (progress = cumulative event count; no total).
6. `timeout_seconds` bounds the run (default 900, cap 86400, negative ⇒
   input error) including suspended gate round trips; expiry ends the run
   like any other cancellation — partial output is already persisted by the
   driver.
7. Outcome mapping: a finished turn (even one whose agent errored mid-way)
   returns `status: "completed" | "error"` with the accumulated answer —
   **not** a tool error, the call itself succeeded. Handler-level failures
   (bad input, unknown session, busy, cap reached, unknown/expired state) are
   tool errors with actionable messages. `cancelled` is reported when the
   run context is cancelled by something other than its own timeout (v1:
   process shutdown edges; see item 4).

### Spec MS-003: gate delivery over SEP-2322

`runserver` builds one gates object implementing `interfaces.ApprovalGate` +
`interfaces.ClarifyGate`. The run handler attaches a per-run `gateBroker` to
the gates for the run's whole lifetime (keyed by hakase session id; a
session-less ask routes to the sole active run, anything else is
fail-closed). A gate ask hands its `ElicitParams` to the broker and blocks;
the handler's serve loop turns it into an `input_required` result
(`InputRequests: {gate-N: elicitation}`, `RequestState: <random 128-bit
token>`), and the client's retry resumes the run by delivering
`InputResponses` to the waiting query. Three properties fall out:

- **The run survives the round trip.** It executes on a detached context
  (`context.WithoutCancel` + the run timeout) so the suspended call's request
  context dying does not kill it; ctx values (tracing, sandbox pinning,
  project binding) are preserved. The per-session run slot is held across
  suspensions; finished runs stay resolvable by a late retry for 10 minutes
  (TTL), after which the retry errors with a pointer to `get_session`.
- **One code path, both protocol generations.** Pre-2026-07-28 clients are
  fulfilled by the SDK's server middleware (it performs the elicitation and
  re-invokes the handler); 2026-07-28 clients fulfill client-side and retry.
  An unfulfillable client (no elicitation support) fails the call at the
  client; the run continues and the gate's expiry denies it.
- **Fail-closed posture everywhere.** No broker for the session, expiry
  (`ApprovalExpiry`/`ClarifyExpiry`), a declined dialog, or an unfulfillable
  client all deny (approval) or report Canceled/TimedOut (clarify).

- `approval.mode`: `allow` ⇒ approve without asking; `deny` ⇒ deny without
  asking; `interactive` (default) ⇒ round-trip. Message renders tool,
  command, risk, reason; the form schema is `{approve: boolean}` required.
- Clarify: question as the elicitation message; choices become a `string`
  enum (or `array` of enum items when `multi_select`); free text when no
  choices.
- The web/Telegram gates are untouched; the bootstrap installs these gates
  on the `agent.Runtime` AND via `mcp.SetApprovalGate`/`mcp.SetClarifyGate`
  so remote-MCP elicitation rides the same path.
- go-sdk constraint worth knowing: the SDK serves requests **sequentially per
  connection**, so on stdio an in-flight `run` queues subsequent requests on
  that connection (including its own gate round trips, which is fine — the
  client retries only after the `input_required` response). The per-session
  and global run caps therefore matter for future multi-connection
  transports, not for serializing single-connection hosts.

### Spec MS-004: CLI and bootstrap

- `internal/cli/mcp.go`: `serve` gains `--agent` (flagset-parsed). With the
  flag, it delegates to an injected `MCPAgentServeFn func(args []string) int`;
  unset (tests, non-main wiring) ⇒ usage error explaining the main-binary
  wiring, exit 2.
- `cmd/hakase/mcpserver.go`: the real handler — config load, tracing install
  (defer flush), sandbox init/validate, session service (snapshot config),
  project registry, vision config hook, `agent.Deps` (same factories as
  `web.go:237-283`), media setup, runserver gates on `agent.Runtime` + the
  `mcp` package globals, `agent.SetupRunner`, background `FetchModelInfo`,
  then the `mcp.Server` (name `hakase`, version `cli.Version`) with skills
  resources + runserver tools on `StdioTransport`.
- `cmd/hakase/main.go` sets `cli.MCPAgentServeFn = runMCPAgentServe` next to
  the existing `RegisterCommand` calls. Usage text updated.

## Host configuration

Register the agent-flavored server with any MCP host (stdio):

```json
{
  "mcpServers": {
    "hakase": {
      "command": "/path/to/hakase",
      "args": ["mcp", "serve", "--agent"]
    }
  }
}
```

Claude Code: `claude mcp add hakase -- /path/to/hakase mcp serve --agent`.
The default (`mcp serve`, no flag) stays skills-only and boots no model
wiring — safe to point at the same binary from any host.

Timeouts: MCP clients own request timeouts, and a `run` call lasts as long
as hakase's turn (default bound 900s, caller-tunable per call via
`timeout_seconds`). Claude Code's default MCP tool timeout is shorter than
most agent runs — raise it (`MCP_TOOL_TIMEOUT` env, or `MCP_TIMEOUT` for
startup) or pass an explicit `timeout_seconds` smaller than the client's
ceiling. Progress notifications are sent on every stream delta and activity
line, which clients use to show liveness; some clients also treat steady
progress as a reason not to reap the call.

## Out of scope (recorded, not forgotten)

- HTTP/streamable transport + auth (the web server owns auth today).
- Canvas/graph events over MCP (activity log + progress cover v1).
- Prompt/resources surface for sessions (tools only).
- Discord/Slack transport — separate backlog item (#20), untouched.
