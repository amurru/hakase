# Spec: MCP Gateway — installer, audit, scoped tokens, 40-tool budget

Governing issue: TBD (Tier-3 proposal, P0-4). Reference: MCP 2026-07-28 (MRTR, CIMD, skill://), OWASP MCP Top 10, gateway `search+execute` pattern, MCPB, `npx skills add`.

**Scope:** registry client, install flows, audit command, scoped-token UX, shadow baseline, gateway meta-tools + budget. Non-goals: HTTP runserver transport, AIBOM signing enforcement, multi-registry federation.

## Decisions

- **Reuse `UpsertServer`/`Reconnect`.** Install writes via existing `UpsertServer` on the manager (`internal/mcp/mcp_servers.go:492`, user-registry scope today; project scope with confirm is proposal) + `Reconnect` (`:456`) + `mcp doctor` one-liner. Persistence flows through `config.UpdateMCPUserRegistry` (`mcp_persist.go`). No new persistence path (`config/mcp_config.go:LoadMCPRegistry`).
- **Audit is security/budget, doctor stays latency.** `mcp doctor` (dial timings) unchanged; new `mcp audit` covers inventory, shadow drift, poisoning scan, auth posture, sandbox/egress, budget, provenance.
- **Budget enforced by auto-degrade, never hard-fail.** `MCPServerManager.Tools()` (`internal/mcp/mcp_servers.go:344`, runs before every model call, never errors today) counts post-filter tools; over budget + gateway disabled → return gateway toolset instead of erroring (ADK aborts on Toolset error). Sub-agents (`internal/agent/delegate.go:629`) get gateway by default over budget.
- **Tokens stay env-named, never persisted.** Match `ExpandEnv` convention: declare required env/secret keys, never write secrets. Least-privilege scope picker at install + per-server `mcp logout`.
- **Defer signatures to warnings.** No hash pinning enforcement v1; rug-pull tripwire (pin `command+url+toolcount`, alert on drift) only.

## Specs

### Spec MG-001: registry client (`internal/mcp/registry.go`)

- Official registry HTTP client: `search <query>` → table name/version/transports. Smithery flag later. Exact count unversioned (say thousands).

### Spec MG-002: install flows

```
hakase mcp search <query> [--limit 20]
hakase mcp install <ref> [--stdio|--http] [--pin version] [--scope project|user] [--allow-env K=V...] [--yes]
hakase mcp list
hakase skill install <owner/repo|URL|path> [-s skill] [-g] [-y]
```

Resolve → fetch manifest → validate transports → credential plan → static audit → `UpsertServer` → `Reconnect`. MCPB: local `.mcpb` unzip → stdio entry; remote out of scope v1. `skill install` is `npx-skills`-compatible translator into discovery dirs (entry point `internal/skill/skill_discovery.go:38`); hakase CLI today has create/list/validate/evolve/evolve-md (`cli/skill.go:35-44`).

### Spec MG-003: audit (`internal/mcp/audit.go`)

Exit non-zero on FAIL. Reuses `Diagnose()` rows:
1. Inventory vs budget 40; top offenders + remediation.
2. Shadow (MCP09): hash config mcp block + `~/.hakase/mcp.json` vs `~/.hakase/mcp-audit.json` baseline; world-writable / argv drift → FAIL/WARN.
3. Poisoning static scan (MCP03): imperative regex over name/description/params; FAIL on hit with server+tool+snippet.
4. Auth (MCP01/02/07): anonymous remote WARN, empty env FAIL, OAuth issuer/scopes/expiry, broad scopes WARN, unpinned `npx -y` WARN.
5. Sandbox/egress (MCP05): `allow_network=false` + remote combos; `sh -c` WARN.
6. Provenance: registry ref + pin vs `unpinned` WARN.

### Spec MG-004: scoped tokens

- Scope picker at install, least-privilege warning, token metadata in audit (issuer/scopes/expiry), `mcp logout <name>` revokes `~/.hakase/mcp-tokens.json` entry (`oauth.go`).

### Spec MG-005: shadow baseline

- `~/.hakase/mcp-audit.json` baseline + drift report (new/removed/changed). Config-file writes unmonitored today.

### Spec MG-006: gateway (`internal/mcp/gateway.go`)

- Opt-in `mcp.gateway{enabled,budget:40,hot_tools:[]}`. New `gatewayToolset`: `mcp_search_tools{query,limit}` + `mcp_describe_tool{name}` + `mcp_call_tool{server,tool,arguments}` + hot tools with real schemas. `mcp_call_tool` routes through manager (same cooldown/cache/audit, same `mcp_<server>_<tool>` naming).

## Non-goals

- See Decisions. Plus `server/discover` probe + `ttlMs` honor (minor spec deltas, separate).

## Definition of done

- [ ] Search/install/audit/logout work against mock registry; poisoned fixture FAILs.
- [ ] Over-budget auto-degrades to gateway; sub-agents get gateway.
- [ ] Suite green.

## Validation against codebase (2026-10-08)

Already exists, do NOT rebuild: manager + stdio/HTTP transports + cooldown/cache/budget(latency)/`Diagnose`/`doctor` (`mcp_servers.go`), CIMD-first OAuth + token store 0600 (`oauth.go`, `mcp-tokens.json`), elicitation→gates fail-closed (`elicitation.go`, `SetApprovalGate/SetClarifyGate`), skill:// serve + `mcp serve` stdio (`skills_server.go`, `runserver/`, `cli/mcp.go`), tool naming `mcp_<server>_<tool>` + include/exclude (`allowsMCPTool`), runtime mutate + persist (`SetDisabled/Reconnect/UpsertServer/RemoveServer` on the manager in `mcp_servers.go:436-520`, persisted via `mcp_persist.go`), registry merge + Validate (`mcp_config.go`), TUI `/mcp` view.
Note: "do NOT rebuild" means do not add a second function doing the same thing (NO SLOP). Extending or enhancing the listed paths in place is fine and expected.
To build: registry client, `search/install/list/audit/logout`, `skill install` (0 hits), poisoning scan, shadow baseline, scope picker UX, gateway toolset + 40-tool count budget (today only latency budget), elicitation audit log, multi-property form support (declined by design).
