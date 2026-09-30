#!/bin/sh
# no-rm-rf.sh — PreToolUse guard example: deny `rm -rf` commands that
# escape /tmp, allow everything else.
#
# Reads the hook payload (tool_input) from stdin; uses only POSIX tools
# (sh + python3 for JSON). Exit 2 blocks the call with stderr as reason.
# Wire: config.json hooks.PreToolUse matcher "^system_exec$".
set -u
input=$(cat)
cmd=$(printf '%s' "$input" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("tool_input",{}).get("command",""))' 2>/dev/null)
case "$cmd" in
  *"rm -rf"*|*"rm -fr"*)
    case "$cmd" in
      *"/tmp/"*|*'$TMPDIR'*|*"/var/tmp/"*) exit 0 ;;
      *)
        echo "refusing recursive delete outside /tmp: $cmd" >&2
        exit 2
        ;;
    esac
    ;;
esac
exit 0
