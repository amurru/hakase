#!/bin/sh
# session-start.sh — SessionStart example: inject git branch + status so
# the first turn starts oriented. Plain stdout on exit 0 IS context.
# Wire: config.json hooks.SessionStart (no matcher).
set -u
input=$(cat)
cwd=$(printf '%s' "$input" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("cwd",""))' 2>/dev/null)
[ -n "$cwd" ] && cd "$cwd" 2>/dev/null || exit 0
branch=$(git branch --show-current 2>/dev/null) || exit 0
[ -z "$branch" ] && exit 0
echo "Working on git branch: $branch"
git status --short 2>/dev/null | head -20
exit 0
