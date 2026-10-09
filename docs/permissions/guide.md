# Permissions Policy — operator guide

Reference: [spec.md](spec.md), [plan.md](plan.md), [tasks.md](tasks.md).

## Files

| Layer | Path | Trust |
|---|---|---|
| User | `~/.hakase/permissions.json` (`HAKASE_HOME` honored) | yours, always applied |
| Project | `<root>/.hakase/permissions.json` | content-hash must be trusted, else dropped |
| Enterprise | `/etc/hakase/enterprise.json` (+ URL poll) | host admin, deny/ask always win |

Minimal example:

```jsonc
{"version": 1, "default": "ask",
 "rules": [{"action": "shell", "resource": "git status *", "effect": "allow"},
           {"action": "shell", "resource": "git push *", "effect": "deny"},
           {"action": "read", "resource": "*.env", "effect": "deny"}]}
```

Evaluation is deny > ask > allow across every matching rule (never
last-wins); several paths at once deny when any path denies. `*` spans
directories, `?` is one character, `~`/`$HOME` expand. No match falls
back to `default` (`ask` when unset).

## Trusting a project file

A project file applies only after its exact bytes are trusted (same
content-hash model as hooks). Rewriting the file lapses trust until it
is re-approved. Trust lives in the shared trust store
(`~/.hakase/hooks-trust.json`, fingerprints prefixed `perm:`).

## Approval queue (mobile)

- `GET /api/approvals/pending` — live prompts (tool, risk, reason,
  command, session, age) plus resurrected post-restart pauses.
- `POST /api/approvals/{id}/respond` and batch
  `POST /api/approvals/respond` (`{ids, approved}`) — first response
  wins per prompt across web, Telegram, and Discord.
- Tiers via `auth.web_roles` (`viewer|approver|admin`): the queue needs
  viewer, answering needs approver. No map (or no config) = fully open,
  today's single-user behavior.

## Audit trail

Every execution decision appends to `logs/exec-audit.jsonl` (0600,
5MB x5 rotation), linked by `prev_hash`/`entry_hash`. Policy decisions
cite their rule (`policy_rule.source/action/resource/effect`); prompt
answers record the actor (web username or `telegram:<id>` /
`discord:<id>`) under `trace_id` = approval ID.

- `hakase audit verify [--dir logs]` — re-hash the chain (tamper fails).
- `hakase audit export [--since 24h] [--format jsonl|csv] [--verify]` —
  stream entries oldest-first (CSV is metadata-only: no commands, args,
  or reasons).
- `audit.forward_url` (+ `forward_format: jsonl|json`, env
  `HAKASE_AUDIT_FORWARD_URL`) — best-effort POST per entry.

## Config

```jsonc
{"approval": {"mode": "interactive", "expiry_seconds": 60}, // deny|allow automate the gate
 "permissions": {"enterprise_path": "", "enterprise_url": "", "poll_minutes": 0},
 "auth": {"web_roles": {"amy": "admin"}},
 "audit": {"forward_url": "", "forward_format": "jsonl"}}
```

Enterprise `allow_managed_only` strips allow rules from the user and
project layers; `disable_bypass` turns `approval.mode=allow` back into
interactive. Per-agent overlays (`agents.<name>.rules`) merge under the
same precedence.
