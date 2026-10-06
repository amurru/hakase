#!/bin/sh
# log-tool.sh — PostToolUse observer example: append one JSONL line per
# tool call to ~/.hakase/hooks-tool-log.jsonl (observability only; exit
# status is ignored on PostToolUse beyond warn-and-continue).
# Wire: config.json hooks.PostToolUse with an empty matcher (all tools).
set -u
log="${HAKASE_HOME:-$HOME/.hakase}/hooks-tool-log.jsonl"
mkdir -p "$(dirname "$log")"
cat | python3 -c '
import datetime, json, sys
d = json.load(sys.stdin)
line = {"ts": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "event": d.get("hook_event_name"), "tool": d.get("tool_name"),
        "session": d.get("session_id")}
print(json.dumps(line))
' >> "$log" 2>/dev/null
exit 0
