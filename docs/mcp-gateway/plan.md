# Execution Plan: MCP Gateway

Spec: [spec.md](spec.md). Reuse manager paths; auto-degrade over budget.

## Phases

### Phase 1 — registry + install (MG-001/002)

1. `internal/mcp/registry.go`: official registry HTTP client + `search`.
2. `internal/mcp/install.go`: manifest → validate → credential plan → static audit → `UpsertServer` → `Reconnect`; MCPB local import; `skill install` translator.
3. CLI in `internal/cli/mcp.go` (`search/install/list`) + `internal/cli/skill.go` (`install`); `config.json.example` knobs.

### Phase 2 — audit + tokens + shadow (MG-003/004/005)

4. `internal/mcp/audit.go`: 7 checks reusing `Diagnose()`; baseline `~/.hakase/mcp-audit.json`.
5. Scope picker + `mcp logout`; elicitation audit log.

### Phase 3 — gateway + budget (MG-006)

6. `internal/mcp/gateway.go`: meta-tools toolset + hot tools; count enforcement in `MCPServerManager.Tools()` (auto-degrade, never error); `delegate.go:629` sub-agent default.
7. tasks.md ticked; CHANGELOG; README/docs mention.

## Critical path

1 → 2 → 4 → 6 sequential. 3, 5 ride 1-2.

## Verification baseline

- Mock registry HTTP search/install; poisoned-tool fixtures FAIL with server+tool+snippet.
- Shadow drift (new/removed/argv change) reported; world-writable flagged.
- Over-budget (e.g. 10x30 tools) auto-degrades; sub-agent gets gateway.
- Self-contained tests (temp HOME, no network); `gofmt -l`, `go vet ./...`, `go test ./...`, `cd webui && pnpm test`.

## Risk register

- **ADK Toolset error contract**: `Tools()` never errors today (`mcp_servers.go:6-11`); gateway returns meta-tools instead of erroring to avoid aborting the turn.
- **Registry moderation permissive**: treat as untrusted; static scan + pin warnings, not enforcement.
- **MCPB scope creep**: local only v1; remote deferred.
