# Spec: `hakase doctor` preflight diagnostic command

Governing issue: [#101](https://github.com/amurru/hakase/issues/101) (Closes #101).
References: `docs/ROADMAP.md:116-120`, `README.md:316-323`, `internal/cli/command.go`, `internal/cli/stats.go`.

**Scope:** One command (`hakase doctor`) that checks toolchain dependencies, configuration validity, active sandbox mode, provider reachability, MCP server status, and credential presence, printing an actionable fix hint for every failure or warning.

---

## Decisions & Design Specs

### Spec DR-001: Subcommand & Flags
- Register `doctor` in `internal/cli/command.go:init()` next to `stats`.
- Export `RunDoctorCLI(args []string) int` in `internal/cli/doctor.go`.
- Flags:
  - `--format table|json` (default `table`)
  - `--skip-net` (default `false`: skips provider and MCP network probes)
- Exit code conventions (matching `internal/cli/command.go`):
  - `0`: All checks passed (or `--help` displayed)
  - `1`: One or more checks failed
  - `2`: Usage / flag error

### Spec DR-002: Toolchain Checks
Check binary availability on `$PATH`:
- Runtime required: `python3` (the bundled Python skill runtime creates a venv with
  it - `internal/agent/agent.go:483`). Missing -> `fail`, exit code 1.
- Build-only, warning if missing: `go` (build hakase from source), `node`, `pnpm`
  (build/test the web UI). A release-binary user with no toolchain still has a
  working install, so these must not fail the run.
- Optional surface tools: `ffmpeg`, `whisper-cli`, `piper` (voice/media). Missing ->
  `warn`, exit code stays 0.
- Fix hints point to installation instructions (e.g. `pnpm: npm install -g pnpm`,
  `ffmpeg: apt/brew install ffmpeg`, `whisper-cli: build whisper.cpp`).

### Spec DR-003: Configuration Validation
- Resolve path via `config.ResolveConfigPath("config.json")` and call `config.LoadConfig(path)`.
- If `config.IsNoConfig(err)`, report failure with actionable hint: `Run 'hakase init' to set up a configuration.`
- For invalid config schema / values, report error returned by `config.LoadConfig`.
- Reuses `internal/config` without duplicating validation logic.

### Spec DR-004: Sandbox Mode Validation
- Load and validate sandbox config using `sandbox.LoadSandboxConfig` and `sandbox.ValidateSandboxConfig`.
- Check startup warnings via `sandbox.SandboxStartupWarning`.
- Refuse unsupported modes (e.g. `landlock`) with clear error and fix hint to change `sandbox.mode` in `config.json`.

### Spec DR-005: Provider Validation & Reachability Probe
- Validate `provider`, `base_url`, and presence of `api_key` using `internal/config`.
- Conduct a short HTTP reachability probe against the provider base URL / API endpoint (with a ~3s timeout) unless `--skip-net` is set.
- Never output raw credentials or API keys in report or error output.

### Spec DR-006: MCP Diagnostics
- Use `MCPServerManager.Diagnose` (`internal/mcp/mcp_servers.go`) per configured MCP server.
- Report dial times, toolset build status, and reachability.
- Server counts and the live probe both read the *effective* registry (project
  config merged with `~/.hakase/mcp.json` via `config.LoadMCPRegistry`), so servers
  added or removed through the web/TUI are reported correctly.
- If the manager cannot be constructed (corrupt user registry, invalid server
  block) or the registry fails to load, emit a `fail` check with the error - never
  drop the MCP section from the report and never pass silently.
- Skip the live dial when `--skip-net` is set or the server is disabled.

### Spec DR-007: Credentials & JWT Presence
- Check presence and parseability of `~/.hakase/credentials.json` (required for `hakase web`).
- Check presence of `~/.hakase/jwt-secret`.
- If missing, offer hint `Run 'hakase auth set-password' or 'hakase init' to configure authentication credentials.`

### Spec DR-008: Hermetic Probing & Testability
- All network HTTP probes, PATH lookups, home directory lookups, and MCP diagnostics must be injectable via package-level or struct-level function hooks.
- Tests in `internal/cli/doctor_test.go` redirect `HOME` / `XDG_CONFIG_HOME` via `isolateHome` and stub probes to remain 100% hermetic (no network, temp dirs).

### Spec DR-009: Test Matrix
Beyond the happy paths, the suite must cover every failure mode that can flip the
exit code:
- MCP diagnostics unavailable (manager construction failure) -> `fail`, never a
  silently passing report.
- Corrupt user MCP registry under `--skip-net` -> `fail`.
- Provider missing `api_key` -> `fail` with the `HAKASE_API_KEY` hint.
- Build-only tools (`go`, `node`) missing -> `warn` only.
- Flag misuse -> exit 2; missing config -> `fail` with the `hakase init` hint;
  invalid sandbox mode -> `fail`.

### Spec DR-010: Documentation
- README troubleshooting block points at `hakase doctor` and keeps the existing
  per-error bullets.
- `docs/ROADMAP.md` Tier 4 item 1 marked shipped with the design pointer; CHANGELOG
  "Added" entry in the same PR.
