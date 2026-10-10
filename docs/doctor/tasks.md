# Tasks: `hakase doctor` preflight command

Governing issue: Closes #100.
Design docs: `docs/doctor/spec.md`, `docs/doctor/plan.md`.

## Task List

- [ ] DR-001: Register `doctor` subcommand in `internal/cli/command.go:init()` and export `RunDoctorCLI`.
- [ ] DR-002: Implement toolchain PATH check (`go`, `node`, `pnpm`, `python3`, `ffmpeg`, `whisper-cli`, `piper`) with required vs optional distinction and actionable fix hints.
- [ ] DR-003: Implement config check via `config.LoadConfig`, reporting `config.IsNoConfig` with `hakase init` suggestion.
- [ ] DR-004: Implement sandbox check via `sandbox.ValidateSandboxConfig` and `sandbox.SandboxStartupWarning`.
- [ ] DR-005: Implement provider validation and reachability probe with credential masking.
- [ ] DR-006: Implement MCP server diagnostics using `MCPServerManager.Diagnose`.
- [ ] DR-007: Implement credentials (`credentials.json`) and JWT secret (`jwt-secret`) check.
- [ ] DR-008: Support `--format table|json` and `--skip-net` flags with correct exit codes (0, 1, 2).
- [ ] DR-009: Add hermetic unit tests in `internal/cli/doctor_test.go` covering all check failure/success cases and flag scenarios.
- [ ] DR-010: Update `README.md`, `docs/ROADMAP.md`, and `CHANGELOG.md`.
