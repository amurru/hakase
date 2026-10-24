# ADR-008: Flow Guard 4-Highs Fixup - Approve with Notes (9 conditions)

Status: Approved with notes. The 4-Highs fixup plan (S16-S23, see
`docs/flow-guard/s16-s23-blueprint.md`) is approved for implementation
on branch `develop` subject to the 9 binding conditions below. Each
condition names the exact code it constrains; a slice is not done until
its condition's acceptance test is green.

Context: four High findings remain after the S01-S15 lane -
unproven `save_skill` persistence, ambient pre-grants, model-driven pip,
and unguarded download/python-env/dial/exec paths. The fixup closes
them without new tools, schema, or label changes. Grounded anchors:
`internal/agent/agent.go:1114` (save_skill), `:730-944` (python+pip),
`:957` (download), `:776-784` (env), `:884-914` (pip exec);
`internal/agent/resume.go:390-440` (preGrants);
`internal/agent/flowgate.go:124-155` (canonical identity);
`internal/vision/vision.go:141-159` (httpClient), `:712-771`
(CheckHostPublic/GuardedDial).

## Condition 1 - CheckHostPublic becomes all-addrs (S21)

`internal/vision/vision.go:712-730` currently checks a single
`ResolveIPAddr` answer. It must resolve the FULL answer set
(`LookupIPAddr`) and block when ANY answer is non-public; empty set
fails closed. Signature unchanged. Test:
`TestS21CheckHostPublicAllAddrs` with an injected resolver returning a
mixed set. No DNS caching across calls.

## Condition 2 - SSRF leaf factory, no agent imports (S19)

The download client factory (30s timeout, 50MB cap, `GuardedDialContext`
transport, 5-hop per-hop re-check, `http`/`https` only) must live in a
leaf package (new `internal/agent/download_client.go` with zero
`agent` imports, or `internal/vision` export). It must not import
`internal/agent`, `internal/sandbox` policy, or flow state. Rationale:
the guard must stay usable by `internal/web/handlers/file.go` and media
without creating an import cycle. Test: `go list` import-cycle check /
`TestS19DownloadSSRFBlocked`.

## Condition 3 - Grant domain separation + cap (S17)

Legacy `(session, tool, argsHash)` grants (`resume.go:397-440`) and
flow `(session, tool, argsHash, labelVersion)` grants share TTL (15m)
and a process-wide cap (`<=1024`, evict-oldest) but MUST use separate
key domains: a legacy key never satisfies a flow ask and vice versa
(the S7 one-way-door rule in `ResolveApprovalPause`, `resume.go:522-528`
stays). Empty sessionID never grants, except the StartLabel carve-out:
a flow grant with an empty session is valid only when it binds the
StartLabel (session-less v1-parity callers: `flowSessionID` "" with
`StartLabel`, labelVersion 0). Any non-start flow binding with an empty
session never grants, and legacy grants never grant on an empty session
(no exception). Tests:
`TestS17PreGrantSessionScoped`, `TestS17PreGrantCapEvictsOldest`, plus
a cross-domain non-match test (legacy grant vs flow consume -> false).

## Condition 4 - S16 deny-wins, ask-once preserved

The save_skill provenance gate (`agent.go:1114`, new
`checkSkillCodeProvenance`) sits UNDER the existing flow re-check
(`:1121`): flow Deny refuses regardless of provenance (deny-wins), and a
flow Ask for the exact call is raised at most once per session (the
re-drive consumes the grant, never re-asks). Provenance records are
in-memory, per-session, `<=256` hashes, hash-only (no raw code).
Tests: `TestS16SaveSkillDenyWins`, `TestS16SaveSkillCrossSessionDenied`.

## Condition 5 - pip audit identity + bwrap net posture (S18+S22)

Pip keeps a dual identity: audit/human lines stay `Tool:"pip"`
(`agent.go:850-868` shape preserved, package name hashed), while the
flow/exec binding hashes canonical `system_exec` (S14,
`flowgate.go:136-155`). The bwrap profile for pip (S22) is
workspace-read-only except `.venv/` writable, with network explicitly
OPEN for index egress; the compensating controls are the S18
allowlist + approval + name-shape gates, documented at the build site.
No silent raw-exec fallback on non-Linux: builder error fails the
install. Tests: `TestS22PipUsesBuildExecCommand`,
`TestS22PipBwrapProfile`, `TestS18PipRequiresApproval`.

## Condition 6 - runScript decision (S20+S22 pair, two-layer choke)

`runScript` (`agent.go:776-831`) is decided as a two-layer choke with an
explicit unsandboxed-python exception: S20 scrubs the env (delete
`PYTHONPATH`, `PYTHONHOME`, `PYTHONSTARTUP`,
`__PYVENV_LAUNCHER__`; drop `parentEnv` merge at `:782-784`; minimal
`PATH` + explicit allowlist) AND the python run and venv creation stay
as raw spawns inside `exec_choke.go` (`chokeRunPython` /
`chokeCreateVenv`) with that scrubbed env and no ambient inheritance -
they do NOT reroute through `BuildExecCommand`. Only pip (S22) moves
onto `BuildExecCommand(system_exec)` via `pipChokeBuild` with the pip
profile (net open for the index, `.venv/` writable) plus
`ExecReplaceEnv(pipEnvMap)` so the scrubbed S20 env replaces the
ambient process env after the merge scrub. Neither half alone is
sufficient: scrub without the choke leaves an unowned raw exec path;
choke without scrub jails a poisoned env. Both land together or the
pair is not marked done. S23 scans both layers: raw spawn literals are
allowed only in `exec_choke.go` (each with a `// choke-allow:`
justification), every other agent tool path goes through the shared
choke. Tests: `TestS20EnvScrubDropsPythonPath`,
`TestS20NoParentEnvMerge`, `TestS23ChokeInventory` (python/venv rows ->
`exec_choke.go` raw scrubbed, pip row -> `BuildExecCommand`).

## Condition 7 - S20 import replacement (loader, not path)

Removing `PYTHONPATH=.:./skills` (`agent.go:778`) must ship its
replacement in the same change: a loader preamble that imports skills
by absolute path (`importlib.util.spec_from_file_location`) with only
the script dir on `sys.path[0]`. `import skills.foo` keeps working
through the loader; `.`, `./skills`, and workspace-root `sys.path`
insertions are forbidden. A `./skills/os.py` fixture must NOT shadow
stdlib. Tests: `TestS20SkillLoadsByPath`, `TestS20ShadowingFails`.

## Condition 8 - S18 config surface (additive, deny-by-default)

`PipAllowlist []string` is additive on the sandbox config; default
empty means deny-all. `AllowPipInstall=false`/nil denies without asking
(preserves `pipAllowed` fail-closed, `agent.go:720-724`). Name-shape
regex `^([A-Za-z0-9_.-]{1,64})$` runs before allowlist lookup; pip
progress logs leave the side pane (audit lines stay). Tests:
`TestS18PipRejectsMalformedName`, `TestS18PipAllowlistDenyClosed`,
`TestS18PipSingleRetry`.

## Condition 9 - Registry tolerance (S16 registry writes)

`save_skill` registry handling (`agent.go:1143-1174`: read-tolerant on
missing/corrupt `skills.json`, `MarshalIndent` write-back) keeps its
current tolerant-read / atomic-write behavior under S16: provenance
refusals happen BEFORE any file or registry mutation, and a corrupt
registry still yields an explicit error (not a silent overwrite) when
provenance passes. No `0600/0644` permission change in this lane.
Tests: `TestS16SaveSkillRequiresProvenance` (refusal leaves no file and
no registry delta) + existing `agent_skills_test.go` suite green.

---

## Acceptance bar

All 9 conditions' named tests green, plus `gofmt -l` clean,
`go vet ./...`, `GOOS=darwin go build ./...`, `go test ./...`. S23
(`TestS23SingleChoke`) lands last and fails the lane if any raw
`exec.Cmd` remains on the agent tool path outside the choke.
