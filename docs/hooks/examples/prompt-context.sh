#!/bin/sh
# prompt-context.sh — UserPromptSubmit example: when the prompt asks about
# failing tests, remind the model where the logs live. Reacts to the
# `prompt` field of the payload; silent (empty stdout) otherwise.
# Wire: config.json hooks.UserPromptSubmit (no matcher).
set -u
prompt=$(cat | python3 -c 'import json,sys; print(json.load(sys.stdin).get("prompt",""))' 2>/dev/null)
case "$prompt" in
  *[Tt]est*[Ff]ail*|*failing*test*|*[Ff]laky*)
    echo "Test-failure context: recent logs live under ./logs/ (exec-audit.jsonl); run the single-package test first before the full suite."
    ;;
esac
exit 0
