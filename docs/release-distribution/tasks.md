# Tasks: Linux arm64 Release Asset & `curl | sh` Installer

Governing issue: [#103](https://github.com/amurru/hakase/issues/103) (Closes #103).
Design docs: `docs/release-distribution/spec.md`, `docs/release-distribution/plan.md`.

## Task List

- [x] RD-001: Makefile updates (`build-arm64`, `.PHONY`, `clean`, `.gitignore`).
- [x] RD-002: Workflow updates (`.github/workflows/release.yml`: build arm64, QEMU smoke test, stage, SHA256SUMS, upload, SLSA provenance subjects, header comment).
- [x] RD-003: Implement `packaging/install.sh` POSIX installer script.
- [x] RD-004: Implement `packaging/install_test.sh` hermetic test harness and wire into `.github/workflows/test.yml`.
- [x] RD-005: Documentation updates (`README.md`, `CHANGELOG.md`, `docs/ROADMAP.md`).

- [x] **T-auth [BE]** non-interactive `auth set-password` (`--username`, `--password-stdin`) + unattended first-run provisioning (2026-10-29). `--password-stdin` creates credentials only — overwriting an existing login stays interactive behind the current-password challenge (a non-interactive reset without proving the old password is a local-privilege escalation). `hakase web` provisions from `HAKASE_ADMIN_USERNAME` + `HAKASE_ADMIN_PASSWORD` when credentials are absent (both set, else explicit error; existing credentials always win); the refusal text documents both paths. The env password is readable by same-user processes — the standard container trade-off. This was out of the original spec scope; recorded here as the landed Tier 4 slice. Prerequisite for the Docker path (still open).
