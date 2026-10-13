# Execution Plan: Linux arm64 Release Asset & `curl | sh` Installer

Governing issue: [#103](https://github.com/amurru/hakase/issues/103).
Spec: [spec.md](spec.md).

## Phases

### Phase 1 — Release Engineering & Cross-Compilation (`linux/arm64`)
1. Update `Makefile` with `build-arm64` target, update `.PHONY`, `clean` target, and `.gitignore`.
2. Update `.github/workflows/release.yml` to build arm64 asset, run QEMU user static emulation smoke test, stage asset, and include it in SHA256SUMS, GitHub release upload, and SLSA provenance subjects. Update workflow comment.

### Phase 2 — `curl | sh` Installer Script (`packaging/install.sh`)
3. Write `packaging/install.sh` POSIX shell script following header conventions.
4. Implement CLI flag and env parsing (`--version`, `--to`, `HAKASE_REPO`, `--help`).
5. Implement OS/arch detection and refusal handlers (Darwin, MINGW, unsupported archs).
6. Implement version resolution via GitHub API (`releases?per_page=1`).
7. Implement temp download, SHA256 verification against `SHA256SUMS.txt`, and clear missing-asset error handling.
8. Implement permission-aware binary installation (`install -m 0755` / `sudo install`) and post-install status/hints.

### Phase 3 — Hermetic Test Harness (`packaging/install_test.sh`) & CI Wiring
9. Write `packaging/install_test.sh` test harness with PATH shims for `uname`, `curl`, `sudo`.
10. Implement test assertions for amd64/arm64 happy path, version pin, checksum mismatch, Darwin/Windows refusal, unwritable directory / sudo fallback, 404 missing asset, and `--help`.
11. Wire `packaging/install_test.sh` into `.github/workflows/test.yml`.

### Phase 4 — Documentation & Roadmap Reconcile
12. Update `README.md` Install section.
13. Update `CHANGELOG.md` under "Added".
14. Update `docs/ROADMAP.md` Tier 4 item 2 status.

## Verification Baseline
- `make build-frontend`
- `unformatted=$(gofmt -l .); test -z "$unformatted"`
- `go vet ./...`
- `GOOS=darwin go build ./...`
- `go test ./...`
- `bash packaging/install_test.sh`
- `make build-arm64`
- `file hakase-arm64`
- `sudo apt-get update && sudo apt-get install -y qemu-user-static`
- `qemu-aarch64-static ./hakase-arm64 version`
- `yq . .github/workflows/release.yml`
