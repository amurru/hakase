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
back to `default` (`ask` when unset). Note: the default alone enforces
nothing at the gate/fileops/path-audit call sites (they act on matched
rules only) — for closed world, add an explicit catch-all rule
(`{"action": "*", "resource": "*", "effect": "deny"}`); a rule-less
policy with a non-`ask` default logs a startup warning.

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
- `audit.hmac_key_file` (env `HAKASE_AUDIT_HMAC_KEY_FILE`) — file whose
  bytes HMAC the chain: only a key holder can rewrite history
  undetectably. Without it the chain is self-consistency only (`verify`
  detects partial edits and corruption, not a full rewrite by someone
  with log write access); the SIEM copy is the real anchor then. The
  verify/export commands take `--hmac-key-file` for HMAC-chained logs.

Notes: command lines are redacted for secret-shaped values (`password=`,
`Bearer` tokens, `sk-`/`ghp_`/`xox-` shapes) before chaining. Operator
URLs (enterprise poll, SIEM forward) never follow redirects. Serve the
enterprise policy over an authenticated transport (HTTPS/TLS): strict
decoding and validation check the payload's shape, not its origin, so a
spoofed endpoint that answers with a well-formed policy would be
accepted. `http://` endpoints are MITM-able; a spoofed poll that fails
or misbehaves falls back to the last-good policy, but a well-formed
spoof is indistinguishable from the real server without transport
authentication.

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

## Version 2 (experimental): session data-flow labels

> **Experimental.** A `version: 2` policy only loads with
> `HAKASE_FLOW_EXPERIMENTAL=1` (or `true`). Without the gate the loader
> refuses it loudly instead of silently ignoring the flow rules - a
> partially-enforced taint policy is worse than none. Start from the
> commented starter set in
> [`../flow-guard/permissions.example.jsonc`](../flow-guard/permissions.example.jsonc).

Version 2 adds three optional keys to a rule and one per-session label:

```jsonc
{"version": 2, "default": "ask",
 "rules": [
   {"action": "read", "resource": "docs/internal/*", "effect": "allow",
    "delta": {"audience": ["internal"]}},
   {"action": "webfetch", "resource": "*", "effect": "allow",
    "delta": {"trust": "suspicious"}},
   {"action": "send_message", "resource": "*", "effect": "allow",
    "requires": {"trust": "trusted", "audience_contains": ["public"]},
    "sanitizable": false}]}
```

- **`delta`** taints the *future*: after the call, the session label
  narrows (trust goes up, the audience ceiling goes down). Sources
  (`read`, `webfetch`) carry a delta; sinks do not.
- **`requires`** gates the call against the current label. A failure
  escalates to *ask* (or *deny* when the rule says so) - it never
  widens anything.
- **`sanitizable`** marks a narrow resource where a redacted payload may
  proceed on its own. Never set it on a wildcard `send_message *`: a
  wildcard is never sanitizable, and the engine refuses the auto-proceed
  even if you try.

The session label is `(trust, audience, version)`: `trusted < suspicious`
and `self < internal < public`. It only ever narrows, is stored with the
session (so a resume keeps it), and is recomputed conservatively for
session files written before version 2 existed - an old session never
resumes widest.

**What a block looks like.** The model sees the remedy, not the payload:

```
flow: send_message blocked (trusted/internal v2).
Allowed: ask-human (approve this exact call).
Offer: ask-human | sanitize | drop-result
```

- **ask-human** routes the ask to the normal approval queue (web, TUI,
  Telegram, Discord). `approval.mode=allow` never auto-approves a flow
  ask, and the answer authorizes exactly one call - it never widens the
  label.
- **sanitize** redacts the secret-shaped parts of the payload and
  re-checks the rule. It proceeds on its own only for a narrow
  `sanitizable: true` resource; everything else asks. Message payloads
  only - a code payload (python, a saved skill) is never rewritten
  behind your back, it asks.
- **drop-result** is the model not retrying.

**Audit.** Flow decisions add four entries to `logs/exec-audit.jsonl`:
`flow_blocked`, `flow_sanitized`, `flow_error_redacted` and
`flow_recomputed`. They carry the label snapshot and the args hash -
never the raw args or the payload. The CSV export appends the
`flow_label`, `flow_args_hash` and `flow_version` columns at the end of
the row (the older 10-column export stays readable).

**Debugging.** `HAKASE_FLOW_DEBUG=1` prints the flow decisions and the
args hashes to stderr. It never writes full arguments, and it never
writes to the audit file or the logs.
