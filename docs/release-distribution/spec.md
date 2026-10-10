# Spec: Linux arm64 Release Asset and `curl | sh` Installer

Governing issue: [#103](https://github.com/amurru/hakase/issues/103) (Closes #103).
References: `docs/ROADMAP.md:122-128`, `README.md:48-55`, `.github/workflows/release.yml:85-124`, `Makefile:105-107`.

**Scope:**
1. Cross-compilation target `make build-arm64` and release workflow support for `hakase-<version>-linux-arm64`.
2. A zero-dependency POSIX `curl | sh` installer script (`packaging/install.sh`).
3. A hermetic test harness (`packaging/install_test.sh`) wired into GitHub Actions CI (`.github/workflows/test.yml`).

Out of scope: macOS (darwin) prebuilt binaries, deb/rpm arm64 packages (deb/rpm remain amd64 for now), Dockerfile/compose, Homebrew tap, non-interactive `auth set-password`, code signing, tag pushing/release publishing.

---

## Decisions & Design Specs

### Spec RD-001: `linux/arm64` Release Asset Build
- **Makefile Target:** Add `build-arm64: build-frontend` in `Makefile` mirroring `build-windows`.
  `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags prod -ldflags "$(LDFLAGS)" -o hakase-arm64 ./cmd/hakase/`.
- Add `build-arm64` to `.PHONY` in `Makefile`.
- Add `hakase-arm64` removal to `clean` target in `Makefile`.
- Add `/hakase-arm64` to `.gitignore` alongside `/hakase`.
- **Release Workflow (`.github/workflows/release.yml`):**
  - Run `make build-arm64` after `make build`.
  - Smoke test under QEMU emulation: `sudo apt-get update && sudo apt-get install -y qemu-user-static` and `qemu-aarch64-static ./hakase-arm64 version` asserting it prints the stamped version.
  - Stage `dist-release/hakase-${TAG}-linux-arm64`.
  - Add `hakase-${TAG}-linux-arm64` to `sha256sum ... > ../SHA256SUMS.txt`, `gh release upload`, and SLSA provenance subjects.
  - Update workflow header comment.

### Spec RD-002: `packaging/install.sh` POSIX Installer Script
- **Language & Compatibility:** Strictly POSIX `sh` (no bashisms, no `set -o pipefail`, no `jq`, no prompts, never reads stdin). Requires `curl` (errors clearly if missing).
- **Default Raw URL:** `https://raw.githubusercontent.com/amurru/hakase/main/packaging/install.sh`.
- **Environment & Flags:**
  - Version: `--version <tag>` or `HAKASE_VERSION`.
  - Install Dir: `--to <dir>` or `HAKASE_INSTALL_DIR` (default `/usr/local/bin`).
  - Repository: `HAKASE_REPO` (default `amurru/hakase`).
  - Help: `--help`.
- **OS/Arch Detection:**
  - OS: `Linux` proceeds. `Darwin` refuses with message that no prebuilt darwin binary exists yet (pointing to README quick start). `MINGW*`/`MSYS*`/`CYGWIN*` refuse and point to Windows zip asset. Anything else refuses.
  - Arch: `x86_64` -> `amd64`, `aarch64` or `arm64` -> `arm64`. Anything else refuses with supported list.
- **Version Resolution:**
  - Query `https://api.github.com/repos/$HAKASE_REPO/releases?per_page=1` (do NOT use `/releases/latest` as it ignores prereleases/alphas like `v0.1.0-alpha.10`).
  - Parse JSON `tag_name` using `sed`/`grep`. Fall back gracefully with clear message if API fails (e.g. rate limit).
- **Download & Verification:**
  - Download `hakase-${VERSION}-linux-${ARCH}` and release `SHA256SUMS.txt` into a `mktemp -d` temporary directory.
  - Verify asset SHA256 against `SHA256SUMS.txt` with `sha256sum` (or `shasum -a 256`). Mismatch or failed download aborts without installing.
  - Releases lacking arm64 (e.g. `v0.1.0-alpha.10` or older) report "this release has no linux-<arch> asset" naming the version.
- **Installation & Post-Install:**
  - Install via `install -m 0755` if target directory writable, or `sudo install` if sudo exists; fail cleanly otherwise.
  - Overwrite existing install idempotently.
  - Warn if install directory is not on `$PATH`.
  - Run `hakase version` and print next steps (`hakase init`, `hakase doctor`).

### Spec RD-003: `packaging/install_test.sh` CI Test Harness
- POSIX/bash test harness using a temp directory of PATH shims (`uname`, `curl`, `sudo`).
- Canned release API JSON responses and fake asset / `SHA256SUMS.txt` generator (computing real sha256 of fake binary).
- Test cases asserted:
  1. Linux amd64 happy path (installs, mode 0755).
  2. Linux arm64 (aarch64 maps to `-linux-arm64` asset).
  3. `--version` pin skips API request.
  4. Checksum mismatch exits non-zero, leaves no binary behind.
  5. Darwin refuses with no-darwin-build message.
  6. MINGW refuses with Windows zip pointer.
  7. Unknown arch refuses with supported arch list.
  8. Unwritable target uses `sudo`.
  9. Release without requested arch asset (curl 404) exits non-zero with missing asset message.
  10. `--help` exits 0.

### Spec RD-004: CI Integration
- Add step `Run installer script test harness` executing `bash packaging/install_test.sh` in `.github/workflows/test.yml` under the `linux` job.

### Spec RD-005: Documentation Reconcile
- `README.md`: Update Install table with `Linux (any distro)` `curl | sh` row and `hakase-<version>-linux-arm64` row.
- `CHANGELOG.md`: Add entry under "Added" for linux arm64 asset and installer.
- `docs/ROADMAP.md`: Mark `next: curl | sh installer + linux arm64 asset` shipped in Tier 4 Item 2.
