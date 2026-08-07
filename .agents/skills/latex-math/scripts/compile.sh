#!/usr/bin/env bash
# compile.sh - compile a standalone or full LaTeX document to PDF and a
# transparent PNG (for standalone equation documents).
#
# Usage:
#   bash compile.sh <file.tex> [dpi]
#
# Outputs next to <file>: <file>.pdf and <file>.png (or <file>-1.png when
# falling back to pdftoppm). Requires tectonic + poppler (see
# references/toolchain.md). Errors print the first LaTeX error line.
set -u

tex="${1:?usage: compile.sh <file.tex> [dpi]}"
dpi="${2:-300}"

if ! command -v tectonic >/dev/null 2>&1; then
  echo "ERROR: tectonic not found. See .agents/skills/latex-math/references/toolchain.md" >&2
  exit 1
fi
if ! command -v pdftocairo >/dev/null 2>&1 && ! command -v pdftoppm >/dev/null 2>&1; then
  echo "ERROR: no poppler converter (pdftocairo/pdftoppm). See references/toolchain.md" >&2
  exit 1
fi

base="${tex%.tex}"
dir="$(dirname "$base")"
outdir="${TECTONIC_OUTDIR:-$dir}"
mkdir -p "$outdir"

echo "==> tectonic: $tex -> $outdir/$base.pdf"
tectonic -X compile --outdir "$outdir" "$tex" 2>&1 | tail -20
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "ERROR: LaTeX compilation failed (first error above). See references/error-playbook.md" >&2
  exit 1
fi

pdf="$outdir/$(basename "$base").pdf"
png="$outdir/$(basename "$base").png"

if command -v pdftocairo >/dev/null 2>&1; then
  echo "==> pdftocairo: transparent PNG @ ${dpi} DPI"
  pdftocairo -png -singlefile -transp -r "$dpi" "$pdf" "${png%.png}"
  if [ ! -f "$png" ]; then
    echo "ERROR: pdftocairo produced no output" >&2
    exit 1
  fi
else
  echo "==> pdftoppm: transparent PNG @ ${dpi} DPI (legacy -transp)"
  pdftoppm -png -r "$dpi" -transp "$pdf" "${png%.png}"
  png="$outdir/$(basename "$base")-1.png"
  if [ ! -f "$png" ]; then
    echo "ERROR: pdftoppm produced no output" >&2
    exit 1
  fi
fi

echo "==> OK: $pdf"
echo "    PNG: $png"
