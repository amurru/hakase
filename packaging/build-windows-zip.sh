#!/usr/bin/env bash
# build-windows-zip.sh - assemble the unsigned Windows release zip.
#
# Usage: build-windows-zip.sh [VERSION]
#
# Stages hakase.exe, config.json.example, README.md, and LICENSE into
# dist/hakase-<version>-windows-amd64/, writes SHA256SUMS.txt inside the
# stage, and zips it to dist/hakase-<version>-windows-amd64.zip.
#
# v1 note: the artifact is intentionally unsigned (SmartScreen may flag it);
# Authenticode code-signing is a designated follow-up. SLSA provenance still
# covers the Linux release assets.
set -euo pipefail

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
STAGE="dist/hakase-${VERSION}-windows-amd64"

test -f hakase.exe || { echo "error: hakase.exe not found (run make build-windows)" >&2; exit 1; }

rm -rf "$STAGE"
mkdir -p "$STAGE"
cp hakase.exe "$STAGE/"
cp config.json.example "$STAGE/"
cp README.md "$STAGE/"
cp LICENSE "$STAGE/"

(
  cd "$STAGE"
  sha256sum hakase.exe config.json.example README.md LICENSE > SHA256SUMS.txt
)

(
  cd dist
  if ! command -v zip >/dev/null 2>&1; then
    echo "error: zip(1) is required to assemble the Windows artifact" >&2
    exit 1
  fi
  zip -9 -r "hakase-${VERSION}-windows-amd64.zip" "hakase-${VERSION}-windows-amd64"
)

echo "built dist/hakase-${VERSION}-windows-amd64.zip"
