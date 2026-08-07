#!/usr/bin/env bash
# lint.sh - structural LaTeX/.bib lint used by the pre-output quality
# checklist. Checks begin/end balance, label/ref consistency hints, and
# .bib brace balance + duplicate keys.
#
# Usage:
#   bash lint.sh <file.tex> [refs.bib ...]
set -u

tex="${1:?usage: lint.sh <file.tex> [refs.bib ...]}"
shift
status=0

# --- begin/end balance -----------------------------------------------------
# Strip comments and verbatim/listing bodies first so false positives from
# example code are ignored.
body="$(sed -e 's/\\%.*//' -e '/\\begin{verbatim}/,/\\end{verbatim}/d' \
            -e '/\\begin{lstlisting}/,/\\end{lstlisting}/d' "$tex")"
begins="$(printf '%s\n' "$body" | grep -o '\\begin{[^}]*}' | wc -l)"
ends="$(printf '%s\n' "$body" | grep -o '\\end{[^}]*}' | wc -l)"
if [ "$begins" -ne "$ends" ]; then
  echo "LINT: begin/end mismatch: $begins \\begin vs $ends \\end in $tex" >&2
  status=1
else
  echo "OK: $begins begin/end pairs balanced"
fi

# Pair-by-name check (catches \begin{a}...\end{b}).
printf '%s\n' "$body" | grep -o '\\begin{[^}]*}' | sed 's/\\begin{//;s/}//' | sort > /tmp/lint-begins.$$
printf '%s\n' "$body" | grep -o '\\end{[^}]*}' | sed 's/\\end{//;s/}//' | sort > /tmp/lint-ends.$$
if ! diff -q /tmp/lint-begins.$$ /tmp/lint-ends.$$ >/dev/null; then
  echo "LINT: environment names do not pair up:" >&2
  diff /tmp/lint-begins.$$ /tmp/lint-ends.$$ | head -10 >&2
  status=1
fi
rm -f /tmp/lint-begins.$$ /tmp/lint-ends.$$

# --- label/ref hints -------------------------------------------------------
labels="$(printf '%s\n' "$body" | grep -o '\\label{[^}]*}' | sed 's/\\label{//;s/}//' | sort -u)"
refs="$(printf '%s\n' "$body" | grep -oE '\\(ref|cref|Cref|eqref|pageref)\{[^}]*\}' | sed -E 's/\\(ref|cref|Cref|eqref|pageref)\{//;s/}//' | tr ',' '\n' | sort -u)"
dangling="$(comm -23 <(printf '%s\n' "$refs" | grep -v '^$') <(printf '%s\n' "$labels"))"
if [ -n "$dangling" ]; then
  echo "LINT: referenced but no matching label:" >&2
  echo "$dangling" >&2
  status=1
else
  echo "OK: every \\ref/\\cref has a matching label"
fi

# --- .bib checks -----------------------------------------------------------
for bib in "$@"; do
  [ -f "$bib" ] || { echo "LINT: bib file not found: $bib" >&2; status=1; continue; }
  # Brace balance (excluding comment lines).
  b="$(grep -v '^%' "$bib" | tr -cd '{' | wc -c)"
  e="$(grep -v '^%' "$bib" | tr -cd '}' | wc -c)"
  if [ "$b" -ne "$e" ]; then
    echo "LINT: $bib braces unbalanced: $b '{' vs $e '}'" >&2
    status=1
  else
    echo "OK: $bib braces balanced"
  fi
  # Duplicate keys.
  dups="$(grep -oE '^@[a-zA-Z]+\{[^,]+' "$bib" | sed -E 's/^@[a-zA-Z]+\{//' | sort | uniq -d)"
  if [ -n "$dups" ]; then
    echo "LINT: duplicate bib keys in $bib:" >&2
    echo "$dups" >&2
    status=1
  else
    echo "OK: $bib no duplicate keys"
  fi
done

if [ "$status" -eq 0 ]; then
  echo "LINT: all checks passed"
fi
exit "$status"
