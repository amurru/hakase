# Spec: `hakase doctor` preflight diagnostic command

Governing issue: Tier 4, item 1 (Closes #100).
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
- Core required tools: `go`, `node`, `python3` (failure if missing -> exit code 1).
- Optional surface tools: `pnpm` (web UI build/test), `ffmpeg`, `whisper-cli`, `piper` (voice/media) (warning if missing -> exit code 0 or 1 depending on whether core passes; warnings print fix hints).
- Fix hints point to installation instructions (e.g. `pnpm: npm install -g pnpm`, `ffmpeg: apt/brew install ffmpeg`, `whisper-cli: build whisper.cpp or pip install`).

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
- Skip live dial if `--skip-net` is set or if MCP server is disabled.

### Spec DR-007: Credentials & JWT Presence
- Check presence and parseability of `~/.hakase/credentials.json` (required for `hakase web`).
- Check presence of `~/.hakase/jwt-secret`.
- If missing, offer hint `Run 'hakase auth set-password' or 'hakase init' to configure authentication credentials.`

### Spec DR-008: Hermetic Probing & Testability
- All network HTTP probes, PATH lookups, home directory lookups, and MCP diagnostics must be injectable via package-level or struct-level function hooks.
- Tests in `internal/cli/doctor_test.go` redirect `HOME` / `XDG_CONFIG_HOME` via `isolateHome` and stub probes to remain 100% hermetic (no network, temp dirs).
