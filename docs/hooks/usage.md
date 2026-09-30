# Hooks user guide

Hooks run your own commands at four lifecycle points: before a tool runs
(`PreToolUse`, can block), after a tool runs (`PostToolUse`, observe
only), once per session (`SessionStart`, inject context), and on every
user prompt (`UserPromptSubmit`, inject per-prompt context).

Full contract: [spec.md](spec.md). This guide covers daily use.

## Configuring user hooks

User hooks live in the `hooks` block of your hakase `config.json`:

```json
{
  "hooks": {
    "enabled": true,
    "PreToolUse": [
      {
        "matcher": "^system_exec$",
        "hooks": [
          {
            "name": "no-rm-rf",
            "type": "command",
            "command": ["/home/you/.hakase/hooks/no-rm-rf.sh"],
            "timeout": 30,
            "on_failure": "allow"
          }
        ]
      }
    ],
    "PostToolUse": [],
    "SessionStart": [
      {
        "hooks": [
          {
            "name": "project-context",
            "type": "command",
            "command": ["/home/you/.hakase/hooks/session-start.sh"],
            "timeout": 30,
            "on_failure": "allow"
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "name": "prompt-context",
            "type": "command",
            "command": ["/home/you/.hakase/hooks/prompt-context.sh"],
            "timeout": 30,
            "on_failure": "allow"
          }
        ]
      }
    ],
    "project": { "enabled": true }
  }
}
```

- `command` is an **argv array** (exec form, never a shell string).
- `matcher` is an unanchored regex on the tool name; anchor with
  `^...$` for exact. `SessionStart` and `UserPromptSubmit` groups must
  leave `matcher` empty (they run unconditionally; a matcher would
  silently never fire and is a config error).
- `timeout` defaults to 30s; `on_failure` is `allow` (fail-open, the
  default) or `block` (fail-closed). `block` is only meaningful on
  `PreToolUse` and rejected elsewhere.

## The hook contract

Every hook receives a JSON payload on **stdin**:

```json
{
  "hook_event_name": "PreToolUse",
  "session_id": "sess_abc",
  "tool_name": "system_exec",
  "tool_input": { "command": "rm -rf /tmp/x" },
  "tool_response": { "ok": true },
  "prompt": "summarize the working tree",
  "cwd": "/home/you/proj",
  "timestamp": "2026-09-30T12:00:00Z"
}
```

Only the fields relevant to the event are set (`tool_response` on
`PostToolUse`, `prompt` on `UserPromptSubmit`).

How the runner interprets the run:

| Outcome | Meaning |
|---|---|
| Exit 0, empty/plain stdout | Allow (tool events) / no context (session/prompt events) |
| Exit 0, plain stdout on SessionStart/UserPromptSubmit | Injected as model-visible context |
| Exit 0, JSON with `hookSpecificOutput.additionalContext` | Context appended (all events) |
| Exit 0, JSON `permissionDecision: deny` / `decision: block` | Block (PreToolUse only) |
| Exit 2 | Block (PreToolUse) / warn-and-continue (everything else) |
| Other non-zero, timeout, missing binary | Hook error: `on_failure` decides |

`updatedInput` (arg rewriting) is **not** supported in v1: a hook that
returns it is ignored with a loud warning, so Claude-ported hooks
degrade visibly instead of silently.

## Project hooks + trust

A repo can ship `<root>/.hakase/hooks.json` with extra hooks. Project
hooks **never run until trusted per-hook**: trust is a content hash over
the resolved command plus local script bytes, stored in
`~/.hakase/hooks-trust.json`. Editing a script lapses trust until it is
re-approved. Untrusted hooks are skipped with a warning naming the fix.

```sh
hakase hooks list                  # both layers, trust status
hakase hooks trust                 # review + approve (y/N each)
hakase hooks trust <prefix> --yes  # approve by fingerprint prefix
hakase hooks untrust <prefix>      # revoke
hakase hooks test <name|prefix>    # dry-run one handler, print verdict
```

The project file can only ADD hooks: no `enabled` switch, no way to
disable your hooks or the trust gate. The agent itself cannot write
hook files (sandbox write-deny), so sandboxed code cannot buy
persistence by editing them.

Review in the UI instead: `/hooks` in the TUI (read-only browser) or
the Hooks settings page in the web UI (list, trust, revoke).

## Example scripts

`docs/hooks/examples/` holds four starters (see each file's header):

- `no-rm-rf.sh` — PreToolUse guard: denies `rm -rf` outside /tmp.
- `log-tool.sh` — PostToolUse observer: appends a JSONL audit line.
- `session-start.sh` — SessionStart: injects git branch + status.
- `prompt-context.sh` — UserPromptSubmit: reacts to the prompt text.

Copy one to `~/.hakase/hooks/`, `chmod +x` it, and point your config
at the copy.
