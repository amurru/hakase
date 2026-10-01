#!/bin/sh
# no-rm-rf.sh — PreToolUse guard example: deny recursive deletes.
#
# Conservative by design: ANY `rm -rf` / `rm -fr` blocks (exit 2), because
# validating "only deletes under /tmp" requires parsing every deletion
# target — compound commands (`rm -rf /tmp/x /home/you/data`), traversal
# (`/tmp/../home/you/data`), and flags make substring checks unsound. (An
# earlier version of this example allowed commands merely containing
# "/tmp/" and was wrong for exactly those cases.) Narrow the allow rule
# only once you parse targets properly; until then, fail closed.
#
# Reads the hook payload (tool_input) from stdin; uses only POSIX tools
# (sh + python3 for JSON). Exit 2 blocks the call with stderr as reason.
# Wire: config.json hooks.PreToolUse matcher "^system_exec$".
set -u
input=$(cat)
cmd=$(printf '%s' "$input" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("tool_input",{}).get("command",""))' 2>/dev/null)
case "$cmd" in
  *"rm -rf"*|*"rm -fr"*)
    echo "refusing recursive delete (narrow this guard before allowing any): $cmd" >&2
    exit 2
    ;;
esac
exit 0
