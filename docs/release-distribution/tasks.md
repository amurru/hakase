# Tasks: Linux arm64 Release Asset & `curl | sh` Installer

Governing issue: [#103](https://github.com/amurru/hakase/issues/103) (Closes #103).
Design docs: `docs/release-distribution/spec.md`, `docs/release-distribution/plan.md`.

## Task List

- [x] RD-001: Makefile updates (`build-arm64`, `.PHONY`, `clean`, `.gitignore`).
- [x] RD-002: Workflow updates (`.github/workflows/release.yml`: build arm64, QEMU smoke test, stage, SHA256SUMS, upload, SLSA provenance subjects, header comment).
- [x] RD-003: Implement `packaging/install.sh` POSIX installer script.
- [x] RD-004: Implement `packaging/install_test.sh` hermetic test harness and wire into `.github/workflows/test.yml`.
- [x] RD-005: Documentation updates (`README.md`, `CHANGELOG.md`, `docs/ROADMAP.md`).
