# Execution Plan: `hakase doctor` preflight command

Governing issue: [#101](https://github.com/amurru/hakase/issues/101).
Spec: [spec.md](spec.md).

## Phases

### Phase 1 — CLI Subcommand Registration & Probe Architecture
1. Register `doctor` subcommand in `internal/cli/command.go` `init()`.
2. Define injectable probe interfaces / functions in `internal/cli/doctor.go` for:
   - Toolchain PATH lookups (`exec.LookPath`)
   - Config loading (`config.LoadConfig`)
   - Sandbox config evaluation (`sandbox.ValidateSandboxConfig`)
   - Provider reachability probing (`http.Client` HEAD/GET request)
   - MCP server diagnostics (`mcp.MCPServerManager.Diagnose`)
   - Credentials file checking (`os.Stat` on `~/.hakase/credentials.json` and `jwt-secret`)

### Phase 2 — Implementation of Diagnostic Checks & Output Formats
3. Implement `RunDoctorCLI(args []string) int` handling `--format table|json` and `--skip-net`.
4. Implement formatting helpers for table (colored / tagged status lines) and JSON output structure.
5. Provide clear, actionable fix hints for every failed or warned diagnostic check.

### Phase 3 — Hermetic Unit Tests (`internal/cli/doctor_test.go`)
6. Write unit tests covering:
   - All checks passing (table & json format)
   - Core tool missing vs optional tool missing
   - Missing configuration (`hakase init` hint)
   - Landlock / invalid sandbox mode refusal
   - Provider misconfiguration / unreachable probe
   - Missing credentials (`hakase auth set-password` hint)
   - Flag misuse (exit code 2)

### Phase 4 — Documentation & Roadmap Reconcile
7. Update `README.md:316-323` to document `hakase doctor`.
8. Update `docs/ROADMAP.md` Tier 4 item 1 status.
9. Update `CHANGELOG.md`.

## Verification Baseline
- `make build-frontend`
- `unformatted=$(gofmt -l .); test -z "$unformatted"`
- `go vet ./...`
- `GOOS=darwin go build ./...`
- `go test ./...`
