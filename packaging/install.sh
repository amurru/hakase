#!/bin/sh
# packaging/install.sh - POSIX shell installer for hakase release binaries.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/amurru/hakase/main/packaging/install.sh | sh
#   curl -fsSL ... | sh -s -- --version v0.1.0-alpha.10 --to /usr/local/bin
#
# Flags & Environment:
#   --version <tag>      | HAKASE_VERSION     Release tag to install (default: latest release tag)
#   --to <dir>           | HAKASE_INSTALL_DIR Directory to install into (default: /usr/local/bin)
#   HAKASE_REPO          GitHub repository (default: amurru/hakase)
#   --help               Show usage information
#
# Decisions:
#   - Strictly POSIX sh constructs (no bashisms, no pipefail, no jq, no prompts).
#   - Queries https://api.github.com/repos/$HAKASE_REPO/releases?per_page=1 for tag resolution
#     (avoiding /releases/latest which mishandles prereleases).
#   - Downloads release binary + SHA256SUMS.txt into a temporary directory and verifies integrity.
#   - Idempotently installs executable via install(1) or sudo install(1).

set -e

HAKASE_REPO="${HAKASE_REPO:-amurru/hakase}"
VERSION="${HAKASE_VERSION:-}"
INSTALL_DIR="${HAKASE_INSTALL_DIR:-/usr/local/bin}"

usage() {
	cat <<EOF
hakase installer

Usage:
  install.sh [options]

Options:
  --version <tag>  Specify version tag (e.g. v0.1.0-alpha.10)
  --to <dir>       Specify installation directory (default: /usr/local/bin)
  --help           Show this help message

Environment Variables:
  HAKASE_VERSION     Same as --version
  HAKASE_INSTALL_DIR Same as --to
  HAKASE_REPO        GitHub repository (default: amurru/hakase)
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		if [ -z "${2:-}" ]; then
			echo "error: --version requires a tag argument" >&2
			exit 1
		fi
		VERSION="$2"
		shift 2
		;;
	--to)
		if [ -z "${2:-}" ]; then
			echo "error: --to requires a directory argument" >&2
			exit 1
		fi
		INSTALL_DIR="$2"
		shift 2
		;;
	--help)
		usage
		exit 0
		;;
	*)
		echo "error: unknown option: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

if ! command -v curl >/dev/null 2>&1; then
	echo "error: curl is required to run this installer" >&2
	exit 1
fi

OS="$(uname -s)"
case "$OS" in
Linux)
	;;
Darwin)
	echo "error: no prebuilt darwin binary exists yet for macOS." >&2
	echo "Please build from source using the README quick start instructions:" >&2
	echo "  https://github.com/amurru/hakase#quick-start" >&2
	exit 1
	;;
MINGW* | MSYS* | CYGWIN*)
	echo "error: Windows installer script is not supported." >&2
	echo "Please download the prebuilt Windows zip asset from releases:" >&2
	echo "  https://github.com/$HAKASE_REPO/releases" >&2
	exit 1
	;;
*)
	echo "error: unsupported operating system: $OS" >&2
	exit 1
	;;
esac

RAW_ARCH="$(uname -m)"
case "$RAW_ARCH" in
x86_64 | amd64)
	ARCH="amd64"
	;;
aarch64 | arm64)
	ARCH="arm64"
	;;
*)
	echo "error: unsupported architecture: $RAW_ARCH (supported: amd64, arm64)" >&2
	exit 1
	;;
esac

if [ -z "$VERSION" ]; then
	API_URL="https://api.github.com/repos/$HAKASE_REPO/releases?per_page=1"
	API_RESP="$(curl -fsSL "$API_URL" 2>/dev/null || true)"
	if [ -z "$API_RESP" ]; then
		echo "error: failed to resolve latest release from GitHub API." >&2
		echo "Please specify a version explicitly via --version <tag> or HAKASE_VERSION." >&2
		exit 1
	fi
	VERSION="$(printf '%s' "$API_RESP" | grep -o '"tag_name": *"[^"]*"' | head -n1 | sed 's/"tag_name": *"\([^"]*\)"/\1/')"
	if [ -z "$VERSION" ]; then
		echo "error: could not parse release tag from GitHub API response." >&2
		echo "Please specify a version explicitly via --version <tag> or HAKASE_VERSION." >&2
		exit 1
	fi
fi

ASSET_NAME="hakase-${VERSION}-linux-${ARCH}"
DOWNLOAD_BASE="https://github.com/$HAKASE_REPO/releases/download/${VERSION}"
ASSET_URL="${DOWNLOAD_BASE}/${ASSET_NAME}"
SUMS_URL="${DOWNLOAD_BASE}/SHA256SUMS.txt"

TMP_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t 'hakase-install')"
cleanup() {
	rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

echo "Downloading $ASSET_NAME ($VERSION)..."

if ! curl -fsSL "$SUMS_URL" -o "$TMP_DIR/SHA256SUMS.txt" 2>/dev/null; then
	echo "error: failed to download SHA256SUMS.txt for release $VERSION." >&2
	echo "Verify that release $VERSION exists at https://github.com/$HAKASE_REPO/releases/tag/$VERSION" >&2
	exit 1
fi

if ! curl -fsSL "$ASSET_URL" -o "$TMP_DIR/$ASSET_NAME" 2>/dev/null; then
	echo "error: release $VERSION has no linux-$ARCH asset ($ASSET_NAME)." >&2
	echo "Verify asset availability at https://github.com/$HAKASE_REPO/releases/tag/$VERSION" >&2
	exit 1
fi

EXPECTED_SHA="$(grep -F "$ASSET_NAME" "$TMP_DIR/SHA256SUMS.txt" | awk '{print $1}' | head -n1)"
if [ -z "$EXPECTED_SHA" ]; then
	echo "error: release $VERSION SHA256SUMS.txt does not contain entry for $ASSET_NAME." >&2
	exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
	COMPUTED_SHA="$(sha256sum "$TMP_DIR/$ASSET_NAME" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
	COMPUTED_SHA="$(shasum -a 256 "$TMP_DIR/$ASSET_NAME" | awk '{print $1}')"
else
	echo "error: neither sha256sum nor shasum is available to verify asset checksum" >&2
	exit 1
fi

if [ "$COMPUTED_SHA" != "$EXPECTED_SHA" ]; then
	echo "error: SHA256 checksum mismatch for $ASSET_NAME!" >&2
	echo "  expected: $EXPECTED_SHA" >&2
	echo "  got:      $COMPUTED_SHA" >&2
	exit 1
fi

echo "Checksum verified ($COMPUTED_SHA)."

if [ ! -d "$INSTALL_DIR" ]; then
	if mkdir -p "$INSTALL_DIR" 2>/dev/null; then
		:
	elif command -v sudo >/dev/null 2>&1; then
		sudo mkdir -p "$INSTALL_DIR"
	else
		echo "error: installation directory $INSTALL_DIR does not exist and cannot be created." >&2
		exit 1
	fi
fi

if [ -w "$INSTALL_DIR" ]; then
	install -m 0755 "$TMP_DIR/$ASSET_NAME" "$INSTALL_DIR/hakase"
elif command -v sudo >/dev/null 2>&1; then
	echo "Installing to $INSTALL_DIR (requires sudo)..."
	sudo install -m 0755 "$TMP_DIR/$ASSET_NAME" "$INSTALL_DIR/hakase"
else
	echo "error: $INSTALL_DIR is not writable and sudo is not available." >&2
	exit 1
fi

echo "Successfully installed hakase to $INSTALL_DIR/hakase"

case ":$PATH:" in
*:"$INSTALL_DIR":*) ;;
*)
	echo "warning: $INSTALL_DIR is not in your PATH." >&2
	;;
esac

"$INSTALL_DIR/hakase" version || true

echo ""
echo "Next steps:"
echo "  1. Run 'hakase init' to set up your initial configuration."
echo "  2. Run 'hakase doctor' to check preflight system diagnostics."
