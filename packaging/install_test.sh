#!/usr/bin/env bash
# packaging/install_test.sh - Hermetic test harness for packaging/install.sh.
#
# Runs in CI without network calls or external binary downloads.
# Uses PATH shims to mock curl, uname, and sudo.
#
# Cases:
#   1. Linux amd64 happy path (installs, mode 0755)
#   2. Linux arm64 (aarch64 maps to -linux-arm64 asset)
#   3. --version pin skips API request
#   4. SHA256 mismatch exits non-zero and leaves no binary behind
#   5. Darwin refuses with no-darwin-build message
#   6. MINGW refuses with Windows pointer
#   7. Unknown arch refuses with supported list
#   8. Unwritable target uses sudo
#   9. Release without requested arch asset (404) exits non-zero with no-asset message
#  10. --help exits 0

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_SCRIPT="$REPO_ROOT/packaging/install.sh"

TEST_TMP="$(mktemp -d)"
trap 'rm -rf "$TEST_TMP"' EXIT

calc_sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
	echo "SKIP: neither sha256sum nor shasum is available" >&2
	exit 0
fi

# Helper to setup shims and run install.sh
run_installer_test() {
	local test_name="$1"
	shift

	echo "==> Test: $test_name"

	local case_dir
	case_dir="$(mktemp -d "$TEST_TMP/case.XXXXXX")"

	local fake_bin_dir="$case_dir/bin"
	local install_dir="$case_dir/install"
	mkdir -p "$fake_bin_dir" "$install_dir"

	# Environment override defaults for shim behavior
	local mock_os="${MOCK_OS:-Linux}"
	local mock_arch="${MOCK_ARCH:-x86_64}"
	local mock_curl_404="${MOCK_CURL_404:-0}"
	local mock_corrupt_sha="${MOCK_CORRUPT_SHA:-0}"
	local mock_target_unwritable="${MOCK_TARGET_UNWRITABLE:-0}"

	# Generate a fake executable payload
	local payload_file="$case_dir/fake_hakase_payload"
	cat <<'EOF' > "$payload_file"
#!/bin/sh
echo "hakase v0.1.0-test"
EOF
	chmod +x "$payload_file"

	# Real SHA256 calculation for payload
	local real_sha
	real_sha="$(calc_sha256 "$payload_file")"

	local manifest_sha="$real_sha"
	if [ "$mock_corrupt_sha" -eq 1 ]; then
		manifest_sha="0000000000000000000000000000000000000000000000000000000000000000"
	fi

	# Create uname shim
	cat <<EOF > "$fake_bin_dir/uname"
#!/bin/sh
if [ "\$1" = "-s" ]; then
	echo "$mock_os"
elif [ "\$1" = "-m" ]; then
	echo "$mock_arch"
else
	exec /usr/bin/uname "\$@"
fi
EOF
	chmod +x "$fake_bin_dir/uname"

	# Create curl shim
	cat <<EOF > "$fake_bin_dir/curl"
#!/bin/sh
set -e
out_file=""
url=""

while [ \$# -gt 0 ]; do
	case "\$1" in
		-o) out_file="\$2"; shift 2 ;;
		https://*) url="\$1"; shift ;;
		*) shift ;;
	esac
done

echo "\$url" >> "$case_dir/curl_calls.log"

if echo "\$url" | grep -q "api.github.com/repos"; then
	if [ -n "\$out_file" ]; then
		cat <<'JSON' > "\$out_file"
[{"tag_name": "v0.1.0-test"}]
JSON
	else
		cat <<'JSON'
[{"tag_name": "v0.1.0-test"}]
JSON
	fi
	exit 0
fi

if echo "\$url" | grep -q "SHA256SUMS.txt"; then
	if [ -n "\$out_file" ]; then
		cat <<MANIFEST > "\$out_file"
$manifest_sha  hakase-v0.1.0-test-linux-amd64
$manifest_sha  hakase-v0.1.0-test-linux-arm64
MANIFEST
	fi
	exit 0
fi

if echo "\$url" | grep -q "hakase-v0.1.0-test-linux-"; then
	if [ "$mock_curl_404" -eq 1 ]; then
		exit 22
	fi
	if [ -n "\$out_file" ]; then
		cp "$payload_file" "\$out_file"
	fi
	exit 0
fi

exit 1
EOF
	chmod +x "$fake_bin_dir/curl"

	# Create sudo shim - fixes permissions on unwritable target before running command
	cat <<EOF > "$fake_bin_dir/sudo"
#!/bin/sh
echo "sudo invoked: \$*" >> "$case_dir/sudo_calls.log"
chmod 0755 "$install_dir"
exec "\$@"
EOF
	chmod +x "$fake_bin_dir/sudo"

	if [ "$mock_target_unwritable" -eq 1 ]; then
		chmod 0555 "$install_dir"
	fi

	local output_log="$case_dir/output.log"
	local exit_code=0

	PATH="$fake_bin_dir:$PATH" "$INSTALL_SCRIPT" "$@" --to "$install_dir" > "$output_log" 2>&1 || exit_code=$?

	if [ "$mock_target_unwritable" -eq 1 ]; then
		chmod 0755 "$install_dir"
	fi

	TEST_EXIT_CODE=$exit_code
	TEST_CASE_DIR="$case_dir"
	TEST_OUTPUT_LOG="$output_log"
	TEST_INSTALL_DIR="$install_dir"
}

# --- Case 1: Linux amd64 happy path ---
run_installer_test "Linux amd64 happy path"
if [ "$TEST_EXIT_CODE" -ne 0 ]; then
	echo "FAILED: expected exit code 0, got $TEST_EXIT_CODE"
	cat "$TEST_OUTPUT_LOG"
	exit 1
fi
if [ ! -x "$TEST_INSTALL_DIR/hakase" ]; then
	echo "FAILED: binary $TEST_INSTALL_DIR/hakase was not installed"
	exit 1
fi

# --- Case 2: Linux arm64 happy path ---
MOCK_ARCH="aarch64" run_installer_test "Linux arm64 happy path"
if [ "$TEST_EXIT_CODE" -ne 0 ]; then
	echo "FAILED: expected exit code 0, got $TEST_EXIT_CODE"
	cat "$TEST_OUTPUT_LOG"
	exit 1
fi
if [ ! -x "$TEST_INSTALL_DIR/hakase" ]; then
	echo "FAILED: binary $TEST_INSTALL_DIR/hakase was not installed"
	exit 1
fi

# --- Case 3: --version pin skips API request ---
run_installer_test "--version pin skips API request" --version v0.1.0-test
if [ "$TEST_EXIT_CODE" -ne 0 ]; then
	echo "FAILED: expected exit code 0, got $TEST_EXIT_CODE"
	cat "$TEST_OUTPUT_LOG"
	exit 1
fi
if grep -q "api.github.com" "$TEST_CASE_DIR/curl_calls.log"; then
	echo "FAILED: API call was made despite --version pin"
	exit 1
fi

# --- Case 4: SHA256 mismatch exits non-zero and leaves no binary behind ---
MOCK_CORRUPT_SHA=1 run_installer_test "SHA256 checksum mismatch"
if [ "$TEST_EXIT_CODE" -eq 0 ]; then
	echo "FAILED: expected non-zero exit code for SHA mismatch"
	exit 1
fi
if [ -e "$TEST_INSTALL_DIR/hakase" ]; then
	echo "FAILED: binary was installed despite SHA mismatch"
	exit 1
fi
if ! grep -q "checksum mismatch" "$TEST_OUTPUT_LOG"; then
	echo "FAILED: output log missing mismatch error message"
	cat "$TEST_OUTPUT_LOG"
	exit 1
fi

# --- Case 5: Darwin refuses with no-darwin-build message ---
MOCK_OS="Darwin" run_installer_test "Darwin refusal"
if [ "$TEST_EXIT_CODE" -eq 0 ]; then
	echo "FAILED: expected non-zero exit code on Darwin"
	exit 1
fi
if ! grep -q "no prebuilt darwin binary exists yet" "$TEST_OUTPUT_LOG"; then
	echo "FAILED: output log missing darwin refusal message"
	cat "$TEST_OUTPUT_LOG"
	exit 1
fi

# --- Case 6: MINGW refuses with Windows pointer ---
MOCK_OS="MINGW64_NT-10.0" run_installer_test "Windows MINGW refusal"
if [ "$TEST_EXIT_CODE" -eq 0 ]; then
	echo "FAILED: expected non-zero exit code on MINGW"
	exit 1
fi
if ! grep -q "Windows installer script is not supported" "$TEST_OUTPUT_LOG"; then
	echo "FAILED: output log missing MINGW refusal message"
	cat "$TEST_OUTPUT_LOG"
	exit 1
fi

# --- Case 7: Unknown arch refuses with supported list ---
MOCK_ARCH="riscv64" run_installer_test "Unknown arch refusal"
if [ "$TEST_EXIT_CODE" -eq 0 ]; then
	echo "FAILED: expected non-zero exit code on riscv64"
	exit 1
fi
if ! grep -q "unsupported architecture" "$TEST_OUTPUT_LOG"; then
	echo "FAILED: output log missing unsupported arch message"
	cat "$TEST_OUTPUT_LOG"
	exit 1
fi

# --- Case 8: Unwritable target uses sudo (skipped as root: -w is always true for uid 0) ---
if [ "$(id -u)" -eq 0 ]; then
	echo "==> Test: Unwritable target directory uses sudo (SKIP: running as root)"
else
	MOCK_TARGET_UNWRITABLE=1 run_installer_test "Unwritable target directory uses sudo"
	if [ "$TEST_EXIT_CODE" -ne 0 ]; then
		echo "FAILED: expected exit code 0 when sudo is available"
		cat "$TEST_OUTPUT_LOG"
		exit 1
	fi
	if [ ! -f "$TEST_CASE_DIR/sudo_calls.log" ]; then
		echo "FAILED: sudo was not invoked for unwritable directory"
		exit 1
	fi
fi

# --- Case 9: Release without requested arch asset (curl 404) ---
MOCK_CURL_404=1 run_installer_test "Release without requested arch asset"
if [ "$TEST_EXIT_CODE" -eq 0 ]; then
	echo "FAILED: expected non-zero exit code when asset download 404s"
	exit 1
fi
if ! grep -q "has no linux-" "$TEST_OUTPUT_LOG"; then
	echo "FAILED: output log missing no linux asset message"
	cat "$TEST_OUTPUT_LOG"
	exit 1
fi

# --- Case 10: --help exits 0 ---
run_installer_test "--help option" --help
if [ "$TEST_EXIT_CODE" -ne 0 ]; then
	echo "FAILED: expected exit code 0 for --help"
	cat "$TEST_OUTPUT_LOG"
	exit 1
fi
if ! grep -q "Usage:" "$TEST_OUTPUT_LOG"; then
	echo "FAILED: help output missing Usage string"
	cat "$TEST_OUTPUT_LOG"
	exit 1
fi

echo "All packaging/install.sh tests passed successfully!"
