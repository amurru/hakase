# Tasks: `hakase doctor` preflight command

Governing issue: [#101](https://github.com/amurru/hakase/issues/101) (Closes #101).
Design docs: `docs/doctor/spec.md`, `docs/doctor/plan.md`.

## Task List

- [x] DR-001: Register `doctor` subcommand in `internal/cli/command.go:init()` and export `RunDoctorCLI`.
- [x] DR-002: Implement toolchain PATH check (`go`, `node`, `pnpm`, `python3`, `ffmpeg`, `whisper-cli`, `piper`) with required vs optional distinction and actionable fix hints. `python3` fails (skill venv runtime); `go`/`node`/`pnpm` warn as build-only; `ffmpeg`/`whisper-cli`/`piper` warn as optional surfaces.
- [x] DR-003: Implement config check via `config.LoadConfig`, reporting `config.IsNoConfig` with `hakase init` suggestion.
- [x] DR-004: Implement sandbox check via `sandbox.ValidateSandboxConfig` and `sandbox.SandboxStartupWarning`.
- [x] DR-005: Implement provider validation and reachability probe with credential masking.
- [x] DR-006: Implement MCP server diagnostics using `MCPServerManager.Diagnose` over the effective registry (`config.LoadMCPRegistry`), failing loudly when the manager or registry cannot be built instead of dropping the check.
- [x] DR-007: Implement credentials (`credentials.json`) and JWT secret (`jwt-secret`) check.
- [x] DR-008: Support `--format table|json` and `--skip-net` flags with correct exit codes (0, 1, 2).
- [x] DR-009: Add hermetic unit tests in `internal/cli/doctor_test.go` covering all check failure/success cases and flag scenarios, with no network access.
- [x] DR-010: Update `README.md`, `docs/ROADMAP.md`, and `CHANGELOG.md`.
