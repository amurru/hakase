# Spec: Permissions Policy — `permissions.json`, mobile queue, immutable audit

Governing issue: TBD (Tier-3 proposal, P0-3). Reference: Claude `permissions.json` deny>ask>allow, opencode `{action,resource,effect}` triples, Codex sandbox x approval, gemini Trusted Folders. Trust model: Codex content-hash (already in `internal/hooks/hooks.go:291-311`), NOT gemini #27900.

**Scope:** rule engine + layering + queue endpoint + hash-chained audit export. Non-goals: full MDM infra, shell-string hooks, arg-rewrite.

## Decisions

- **Opencode triples + Claude precedence.** `{action,resource,effect}` covers shell/read/edit/glob/grep/webfetch/subagent; eval deny > ask > allow (not last-wins); multi-resource deny-wins; no-match → `default: ask`. `~`/`$HOME` expansion; canonical absolute resolve before match (reuse `ResolveScopedPath`).
- **Rule eval rides existing gates, never bypasses hooks.** Insert at top of `EvaluateCommand` (`internal/agent/gate.go:954-957`) after hard-deny, before risk threshold; also in fileops `taskResolve` (`sandbox/fileops.go:359-371`) and `AuditSystemCommandPaths` (`systemexec.go:589-801`). Evaluated regardless of hook verdict (Claude rule).
- **Layering user < project < enterprise.** `~/.hakase/permissions.json` + `<root>/.hakase/permissions.json` (content-hash trust-gated like hooks) + `/etc/hakase/enterprise.json` (+ URL poll). Enterprise deny/ask always win; `allowManagedOnly` drops lower allow; project allow waits for folder trust.
- **Honor `approval.mode`.** Type exists (`config.go:38-50`, `config.json.example:127-130`) but `ApproveExec` (`approval.go:25-31`) does not consult it. Wire `deny|allow|interactive` + `default:ask` + `ReplayNever` interaction (`replay_policy.go`).
- **Queue reuses first-responder-wins.** Pending endpoint reads `WebApprovalGate.pending` (`web/handlers/approval.go:28`) + resurrected map; answer path reuses `RespondApproval` so web + Telegram + Discord preserve first-wins (`channel/events.go:91`, `sse/bridge.go:182`).
- **Audit additive + hash-chained.** Existing JSONL (schema `agent/audit.go:13-30`, bounds `:39-42`, 0600 writes `:89-94`, rotation 5MBx5) gains `actor,policy_rule{source,action,resource,effect},prev_hash,entry_hash,trace_id`. Metadata-only export (no file contents/prompts).

## Specs

### Spec PM-001: schema + loader + layering

```jsonc
{"$schema": "https://hakase.example.com/permissions.json", "version": 1, "default": "ask",
 "rules": [{"action": "shell", "resource": "git status *", "effect": "allow"},
   {"action": "shell", "resource": "git push *", "effect": "deny"},
   {"action": "read", "resource": "*.env", "effect": "deny"},
   {"action": "read", "resource": ".env.example", "effect": "allow"}],
 "agents": {"reviewer": {"rules": [{"action": "shell", "resource": "*", "effect": "allow"}]}},
 "enterprise": {"allowManagedOnly": false, "disableBypass": true, "policyUrl": "", "pollMinutes": 60}}
```

New `internal/permissions/` (or extend `gate.go`); loader merges three tiers + `HAKASE_ENTERPRISE_POLICY_URL` poll; strict keys; `PinnedTo` project pin must not widen enterprise denies.

### Spec PM-002: engine wiring

- `Evaluate(action,resource)` called in `EvaluateCommand`, fileops resolve, path-token audit. Bare `Bash` handling (Claude parity: scoped rules only) decided in spec review.

### Spec PM-003: queue + RBAC

- `GET /api/approvals/pending` (approver role), SSE unchanged, Telegram/Discord already first-wins, batch respond. Role map admin/approver/viewer on approval + audit endpoints (allowlist IDs today only).

### Spec PM-004: audit export

- `hakase audit export --since 24h --format jsonl|csv --verify` (verify re-hashes chain); optional `audit.forward{url,format}` SIEM POST best-effort. CSV drops body/lineage.

## Non-goals

- MDM server console, per-hook `if` prefilters, `updatedInput` rewrite, Windows ACL backend.

## Definition of done

- [ ] Rule precedence + layering/lock matrix tests pass; trust-lapse on rewrite.
- [ ] Queue first-wins across web + phone; chain tamper detected by verify.
- [ ] Suite green.

## Validation against codebase (2026-10-08)

Already exists, do NOT rebuild: `ApprovalRequest` + approval/clarify config (`interfaces/interfaces.go:18-67`; `ApprovalGate :219-232`, `ApprovalResponder :258-260`), `ApproveExec` fail-closed (`approval.go`), `askClarify` (`clarify.go`), web gates + `POST /api/approvals/{id}/respond` (`handlers/approval.go`, `spa.go:57-58`, route table `spa.go:29-104` via `server.go:108-110`), 1061-line risk gate (`gate.go`), sandbox roots + implicit denies + `ResolveScopedPath` (`sandbox.go:18-241,256-579`), exec decision + env scrub (`systemexec.go:232-517`), hooks content-hash trust (`hooks.go:291-311`, `trust.go`), audit JSONL (`audit.go`, live `logs/exec-audit.jsonl`), SSE + channel re-emit + callbacks (`sse/bridge.go`, `channel/events.go`, telegram/discord `push.go`+`callbacks.go`), replay policy (`replay_policy.go`).
Note: "do NOT rebuild" means do not add a second function doing the same thing (NO SLOP). Extending or enhancing the listed paths in place is fine and expected.
To build: `permissions.json` (grep 0 hits), rule engine, enterprise layer + sync, `approval.mode` honor, pending-queue endpoint + RBAC, hash chain + export + SIEM.
