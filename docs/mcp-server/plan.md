# Plan: MCP server mode (#45)

Facade over `agentrun.Driver` served with go-sdk v1.8; the run tools, the
elicitation-backed gates, and the `--agent` bootstrap are three independently
testable seams. Spec: [spec.md](spec.md). Sequencing:

1. **runserver package first** (`internal/mcp/runserver`): tools + sink +
   gates, everything behind small seams (runner interface, gate binder) so
   the whole surface is testable with an in-memory client/server pair — no
   model, no network. This is the bulk of the value and the risk.
2. **CLI wiring second**: `--agent` flag + `cli.MCPAgentServeFn` hook +
   `cmd/hakase/mcpserver.go` bootstrap. Pure glue; keep it dumb and mirror
   `web.go`'s runServer order step for step.
3. **Docs/verification last**: example config for an MCP host, README touch,
   full CI suite, real-stdlib integration test as the acceptance gate.

Risks and their answers:

- *go-sdk elicitation requires client capability* — verified in the SDK:
  `ServerSession.Elicit` errors "client does not support elicitation" before
  any round-trip. The gate treats that as fail-closed deny/error; the
  integration test asserts both the supported and unsupported paths.
- *Blocking a tool call for minutes* — MCP clients own request timeouts;
  progress notifications keep hosts informed and the `timeout_seconds` arg
  bounds the server side. Document the client-side timeout knobs in the docs
  (e.g. Claude Code's `MCP_TOOL_TIMEOUT`).
- *Gate state across concurrent calls* — stdio is one client session; the
  binder is mutex-guarded and the spec records the limitation. Multi-session
  (HTTP) would move the binding into the request context — exactly the
  refactor to make when that transport lands.
- *`internal/mcp` must stay agent-free* — runserver is a subpackage;
  `internal/mcp`'s import set (interfaces/project/util/sandbox/config/skill/
  tracing) is unchanged.

Definition of done: acceptance criteria on #45 checked, `tasks.md` ticked in
the landing PR, `hakase mcp serve --agent` driven end-to-end by an MCP client
in tests (list → run → gate elicitation → get_session).
