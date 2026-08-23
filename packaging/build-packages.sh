#!/bin/sh
# Build Linux packages (.deb / .rpm) from the pre-built hakase binary using
# nfpm and packaging/nfpm.yaml.
#
# Usage: ./packaging/build-packages.sh deb [rpm]
#
# Version resolution order:
#   1. $NFPM_VERSION env var (already normalized)
#   2. $VERSION env var (set by the release workflow, e.g. v0.1.0-alpha.2)
#   3. git describe output (tag builds yield e.g. v0.1.0-alpha.2)
#
# The version is normalized for dpkg/rpm: strip the leading 'v' and turn the
# '-alpha' separator into '~alpha' (both dpkg and rpm sort '~' before the
# final release, so 0.1.0~alpha.2 upgrades cleanly to 0.1.0).
set -eu

cd "$(dirname "$0")/.."

if ! command -v nfpm >/dev/null 2>&1; then
	echo "error: nfpm not found on PATH - https://nfpm.goreleaser.com/install/" >&2
	exit 1
fi

if [ $# -eq 0 ]; then
	echo "usage: $0 deb [rpm]" >&2
	exit 2
fi

if [ ! -x hakase ]; then
	echo "error: ./hakase not found - run 'make build' first" >&2
	exit 1
fi

if [ -z "${NFPM_VERSION:-}" ]; then
	raw="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
	NFPM_VERSION="$(printf '%s' "$raw" | sed -e 's/^v//' -e 's/-alpha/~alpha/')"
fi

mkdir -p dist
for fmt in "$@"; do
	case "$fmt" in
	deb)
		NFPM_VERSION="$NFPM_VERSION" nfpm package \
			-f packaging/nfpm.yaml -p deb \
			-t "dist/hakase_${NFPM_VERSION}_amd64.deb"
		;;
	rpm)
		NFPM_VERSION="$NFPM_VERSION" nfpm package \
			-f packaging/nfpm.yaml -p rpm \
			-t "dist/hakase-${NFPM_VERSION}.x86_64.rpm"
		;;
	*)
		echo "unknown package format: $fmt" >&2
		exit 2
		;;
	esac
done
