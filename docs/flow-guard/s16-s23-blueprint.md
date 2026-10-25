# S16-S23 Blueprint (Flow Guard Full Lane, second half)

Scope: S16 through S23 only. S01-S15 are done / specified elsewhere
(`docs/flow-guard/spec.md`, `plan.md`, `tasks.md`). This document is
self-contained: each slice states objective, grounded file:line anchors
(branch `develop` at time of writing), contracts, Allowed/Forbidden,
acceptance tests (exact `go test` names), and guardrails.

Grounded anchors (verify with `grep -n` before implementing; lines drift):

- `internal/agent/agent.go:1114-1189` - `createSaveSkillTool` (save_skill
  sink, flow pre-dispatch re-check at :1121, writes `./skills/<name>.py`
  at :1136-1140, registry write at :1143-1174).
- `internal/agent/agent.go:730-944` - `createPythonTool` (gate at :735,
  `getVenvPython` at :739, `runScript` closure with raw `exec.Cmd` at
  :776-831, `PYTHONPATH=.:./skills` at :778, parentEnv merge at :782-784,
  auto-pip block at :837-928, pip gate `pipAllowed` at :845,
  `pipBin install` raw `exec.Cmd` at :884-914).
- `internal/agent/agent.go:720-724` - `pipAllowed` (nil fail-closed).
- `internal/agent/agent.go:957-1057` - `createDownloadTool` (flow re-check
  at :964, `http.DefaultClient.Do` at :974, no timeout / no size cap,
  `io.Copy` at :1041).
- `internal/agent/resume.go:390-440` - `preGrants` map + `grantApproval` /
  `grantPreApprovalByHash` / `consumePreGrant` (hash-keyed, no session,
  no TTL, never expires, never bounded).
- `internal/agent/resume.go:499-560` - `ResolveApprovalPause` (legacy +
  flow grant paths).
- `internal/agent/flowgate.go:124-155` - `flowArgsHash` /
  `canonicalFlowTool` (S14 single identity: `system_exec_start` ->
  `system_exec`, `grep` -> `search_files`).
- `internal/vision/vision.go:141-159` - shared `httpClient`
  (30s total timeout, plain `DialContext`, redirect re-check via
  single-resolve `CheckHostPublic`).
- `internal/vision/vision.go:712-771` - `CheckHostPublic` (:712, single
  `ResolveIPAddr`, first-answer only) + `GuardedDialContext` (:739,
  validates all addrs but pins `addrs[0]`).

Global guardrails (apply to every slice below):

1. No new tool names, no policy JSON schema change, no label-version bump.
2. Deny-wins: any Deny anywhere in the chain refuses; Ask never
   downgrades a Deny.
3. Ask-once: one human Ask per exact call per session; the re-drive
   consumes a grant, never re-asks.
4. No raw args in hashes, logs, or audit metadata (M2: hashes only).
5. Fail closed on nil config, unparseable input, or missing binding keys.
6. Every slice ships a stubbed-LLM hermetic test; no network in tests
   (`httptest` only, `127.0.0.1` for negative SSRF cases is loopback so
   still blocked).
7. Keep `gofmt -l`, `go vet`, `go test ./...` green.

---

## S16 - save_skill allowlist (code-source gate)

Objective: `save_skill` must only persist code that already passed the
python gate. Today `createSaveSkillTool` (`agent.go:1114`) writes
arbitrary `input.Code` to `./skills/<name>.py` with only a flow-guard
re-check; nothing proves the code was ever executed or approved. S16
adds a code-source allowlist: the exact code bytes must have a prior
`python_interpreter` execution record (success or gate-approved) in this
session, otherwise refuse.

Affected: `internal/agent/agent.go:1114-1189` (`createSaveSkillTool`
execHandler); new helper `checkSkillCodeProvenance` in `gate.go` or
`skill_types.go`; registry write path at `agent.go:1143-1174` unchanged.

Contracts:

- `checkSkillCodeProvenance(sessionID, codeHash string) bool` - true iff
  the session executed (or explicitly approved) the exact code hash via
  `python_interpreter`. Hash = `sha256(normalize(code))`, hex. Never
  stores raw code.
- `recordSkillProvenance(sessionID, codeHash)` - called on every
  `python_interpreter` success AND on every explicit approval of a
  python call (so approved-but-not-yet-run code also qualifies).
- Name validation stays strict: `^[a-z0-9_]{1,64}$`, reject path
  separators, reject overwrite of `__init__`.

Allowed:

- Saving code whose hash matches a recorded execution/approval in the
  same Hakase session.
- Updating an existing skill with re-proven code (same hash rule).

Forbidden:

- Saving unexecuted model-typed code (no provenance record -> refuse
  with `save_skill requires prior python_interpreter execution`).
- Cross-session provenance reuse (records keyed by Hakase session ID).
- Persisting when the flow verdict for `save_skill` is Deny (existing
  :1121 check stays, deny-wins over provenance).

Acceptance tests (`internal/agent/`):

- `TestS16SaveSkillRequiresProvenance` - save without prior exec ->
  error contains `prior python_interpreter`; with recorded hash ->
  writes file.
- `TestS16SaveSkillCrossSessionDenied` - provenance in session A does
  not authorize session B.
- `TestS16SaveSkillDenyWins` - flow Deny + valid provenance still
  refuses.

Guardrails: provenance store in-memory only (no disk), bounded
(`<=256` hashes/session, LRU evict); registry write keeps `0600/0644`
as today; no change to `list_skills`.

---

## S17 - preGrant session + TTL (one-shot grant hardening)

Objective: `preGrants` (`resume.go:397-400`) is a global
`map[hash]struct{}` with no session scope and no expiry: a grant minted
for session A can satisfy session B, and lives forever until consumed.
S17 scopes every grant to `(sessionID, tool, argsHash)` with a TTL and a
global cap.

Affected: `internal/agent/resume.go:390-440` (`preGrants`,
`preGrantKey`, `grantApproval`, `grantPreApprovalByHash`,
`consumePreGrant`); callers `ResolveApprovalPause` (:499+) and flow
grant path `grantFlowCall` (flowgate); `ApproveExec` consume site.

Contracts:

- Key becomes `(sessionID, tool, argsHash)`; keep `ArgsHash(tool,
  command)` as the hash leg (S10/M3 unchanged), add session leg.
- `grantApproval(tool, command, sessionID)` / flow-grant equivalent
  carries sessionID; empty sessionID never grants (fail-closed, same as
  empty tool/command today at :406-409).
- TTL: 15 min default from mint; `consumePreGrant(tool, command,
  sessionID)` returns false when expired (lazy expiry on consume +
  opportunistic sweep, no goroutine).
- Cap: `<=1024` live grants process-wide; insert past cap evicts oldest
  (fail-closed direction: evicted grant means re-ask, never allow).
- Flow grants (`grantFlowCall`) get the same `(session, tool, argsHash,
  labelVersion)` binding + TTL; legacy and flow stores share the TTL/cap
  helper, keys stay domain-separated (see ADR condition 3).

Allowed:

- Consume within TTL, same session, same hash -> approve once, delete
  on consume (one-shot preserved).
- Expired/missing/wrong-session -> `false` (caller re-asks).

Forbidden:

- Cross-session consume, even with identical command bytes.
- TTL refresh on read (no sliding window); no `grantAll` / wildcard.
- Persisting grants to disk (memory only; restart drops them, resume
  goes through `ResolveApprovalPause` re-mint).

Acceptance tests:

- `TestS17PreGrantSessionScoped` - grant in A, consume in B -> false;
  consume in A -> true, second consume -> false (one-shot).
- `TestS17PreGrantTTLExpiry` - fake clock past TTL -> false.
- `TestS17PreGrantCapEvictsOldest` - fill past cap, oldest gone,
  newest present.
- `TestS17PreGrantEmptySessionFailClosed` - empty sessionID never
  installs.

Guardrails: clock via injectable `now func() time.Time` (default
`time.Now`) for hermetic tests; no new goroutines; lock ordering
unchanged (single mutex).

---

## S18 - pip: no-stdout + allowlist + approval

Objective: the auto-pip block (`agent.go:837-928`) runs `pip install
<missingPkg>` derived from model-controlled `ModuleNotFoundError`
stdout, with no approval gate and no package allowlist, and echoes pip
progress to the side pane. S18 makes pip a gated, allowlisted,
quiet action.

Affected: `internal/agent/agent.go:837-928` (auto-pip block);
`pipAllowed` at :722; sandbox config surface `AllowPipInstall`
(+ new `PipAllowlist []string`); audit at :863-868.

Contracts:

- Package-name extraction: `^([A-Za-z0-9_.-]{1,64})$` after taking the
  first `.`-segment; anything else (flags, URLs, `;`, whitespace,
  `--index-url`) -> refuse, no exec.
- Allowlist: `PipAllowlist` in sandbox config; empty list = deny all
  (fail-closed). Matching is exact lowercase, no globs.
- Approval: pip install always raises an approval Ask (medium risk),
  even when `AllowPipInstall=true`. `AllowPipInstall=false`/nil ->
  deny without asking (preserves current :845 fail-closed).
- Quiet: remove side-pane pip progress logs (:876-883, :916-923);
  keep audit entries (allowed/denied) with hashed package name only.
- Retry-once: after a gated install, exactly one `runScript` retry
  (as today :926); a second `ModuleNotFoundError` does not trigger
  another install in the same call.

Allowed:

- `AllowPipInstall=true` + name in allowlist + human approval ->
  install, one retry.
- Deny (config or human) -> return original stdout/stderr unchanged,
  no retry.

Forbidden:

- Installing from model stdout without allowlist match + approval.
- Echoing package names / pip output to the side pane or model beyond
  the existing stdout/stderr return.
- More than one install per tool call; recursive installs.

Acceptance tests:

- `TestS18PipRejectsMalformedName` - table: `foo;rm`, `--index-url=x`,
  `a/b`, empty -> no exec.
- `TestS18PipAllowlistDenyClosed` - empty list denies; unknown pkg
  denies even with approval stub true.
- `TestS18PipRequiresApproval` - allowlisted pkg, approval=false ->
  no exec, stdout preserved; approval=true -> exec called once.
- `TestS18PipSingleRetry` - double missing-module still one install.

Guardrails: config surface additive only (`PipAllowlist`, default
empty); audit keeps `Tool:"pip"`, `Decision` allowed/denied; S22 later
moves the exec onto `BuildExecCommand` without changing this gate.

---

## S19 - download SSRF factory (30s / 50MB, leaf client)

Objective: `createDownloadTool` (`agent.go:957`) uses
`http.DefaultClient` (no timeout, no SSRF guard, unbounded `io.Copy`).
S19 routes downloads through a leaf SSRF-guarded client factory with a
30s total timeout and 50MB cap, mirroring the media proxy pattern.

Affected: `internal/agent/agent.go:967-978,1041`
(`NewRequestWithContext`, `DefaultClient.Do`, `io.Copy`); new leaf
`internal/agent/download_client.go` (or `internal/vision` export if the
ADR leaf-factory condition places it there - see
`adr-008-4highs-fixup.md` condition 2); `internal/web/handlers/file.go`
proxy client as reference template.

Contracts:

- `DownloadClient() *http.Client` (leaf, no imports from `agent`):
  `Timeout: 30s`; `Transport.DialContext = GuardedDialContext`
  (all-addrs validated, S21); `CheckRedirect`: max 5 hops, re-run
  `CheckHostPublic`-all-addrs per hop (S21), wrong-scheme -> stop.
- Scheme allowlist: `http`/`https` only; `file:`, `gopher:`,
  `ftp:` refused before dial.
- Body cap: `io.LimitReader(resp.Body, 50<<20)`; over-cap ->
  `download exceeds 50MB limit` error, partial file removed.
- Pre-flight: `CheckHostPublic`-all-addrs on the initial URL host
  (fail fast before dial; dial-time guard still authoritative).
- Sandbox path confinement at :1012-1033 stays unchanged.

Allowed:

- `https` URLs resolving to all-public IPs, `<=50MB`, `<=5` redirects
  (each hop re-checked) -> saved under `./downloads`, `0600`-safe
  create as today.
- Custom filename still `filepath.Base`-stripped (:998-999).

Forbidden:

- `http.DefaultClient` anywhere in the download path after S19.
- Following a redirect to a newly-internal host (rebind during
  redirect chain).
- Writing partial over-cap files (must unlink on cap error).
- Returning raw upstream bytes to the model beyond saved-path +
  byte-count + content-type (existing output shape unchanged).

Acceptance tests:

- `TestS19DownloadSSRFBlocked` - `httptest` URL swapped to
  `127.0.0.1` / `169.254.169.254` -> refused (table, no external net).
- `TestS19DownloadSizeCap` - serve 51MB (sparse/pattern body via
  `httptest`, not memory-heavy) -> cap error, no file left.
- `TestS19DownloadRedirectReboundBlocked` - first hop public
  (`httptest`), second hop `127.0.0.1` -> blocked.
- `TestS19DownloadTimeout` - hanging server -> error within budget
  (use short test-local timeout injection, not 30s wall).

Guardrails: factory takes no `agent` imports (leaf); timeout/cap as
named consts (`downloadTimeout = 30s`, `downloadMaxBytes = 50<<20`);
`User-Agent` header preserved; no policy/label changes.

---

## S20 - env scrub: drop PYTHONPATH, import by path

Objective: `runScript` sets `PYTHONPATH=.:./skills` (`agent.go:778`)
and merges unfiltered `parentEnv` (:782-784), so a poisoned
`PYTHONPATH` or a malicious `./skills/*.py` shadows stdlib on every
exec. S20 scrubs the child env and loads skills by file path instead of
import search path.

Affected: `internal/agent/agent.go:776-784` (`runScript` env setup);
`getVenvPython` at :541 untouched; skill loading convention (`skills/`
layout).

Contracts:

- Scrub list (exact): `PYTHONPATH`, `PYTHONHOME`, `PYTHONSTARTUP`,
  `__PYVENV_LAUNCHER__` removed from child env always. `PATH` minimal:
  `filepath.Dir(pyBin)` + `/usr/bin:/bin` (plus `;`-joined Windows
  system dir on Windows). Nothing else inherited except an explicit
  allowlist: `SYSTEMROOT` (Windows), `TMPDIR`/`TEMP`/`TMP` (temp),
  `LANG`/`LC_ALL` (encoding). `parentEnv` merge deleted; delegation
  inherits nothing ambient.
- Skill import replacement: instead of `PYTHONPATH` discovery, the
  runner injects `sys.path[0] = scriptDir` only (the sandbox tmp dir),
  and skills are imported by absolute path
  (`importlib.util.spec_from_file_location`) via a small loader
  preamble. Behavior for `import skills.foo` keeps working through the
  loader, not through search path.
- `cmd.Dir` pinning (:787-791) unchanged.

Allowed:

- Child env = scrubbed base + allowlist above + `cmd.Dir`-relative
  script dir only.
- Loader preamble importing one skill file by absolute path.

Forbidden:

- Any `PYTHONPATH`/`PYTHONHOME` value reaching the child (even empty
  string - must be absent, not empty).
- `parentEnv` / `os.Environ()` passthrough (full `os.Environ` append
  at :778 deleted).
- `sys.path` insertions of `.`, `./skills`, or workspace root.

Acceptance tests:

- `TestS20EnvScrubDropsPythonPath` - parent env with poisoned
  `PYTHONPATH=/evil:./skills` -> child env has no `PYTHONPATH` key.
- `TestS20NoParentEnvMerge` - `parentEnv=[EVIL=1]` -> child lacks
  `EVIL`.
- `TestS20SkillLoadsByPath` - script importing a skill by the new
  loader succeeds with `PYTHONPATH` absent.
- `TestS20ShadowingFails` - `./skills/os.py` (fixture) no longer
  shadows stdlib `os`.

Guardrails: Windows `PATH`/`SYSTEMROOT` handling covered by a
windows-only test or `GOOS` branch test; no change to venv layout;
preamble kept under ~20 lines and audited as code, not model input.

---

## S21 - GuardedDial all-addrs (+ CheckHostPublic fix)

Objective: `CheckHostPublic` (`vision.go:712`) resolves a single
`ResolveIPAddr` answer and checks one IP; round-robin / rebinding DNS
can hand the dialer an unvetted address. `GuardedDialContext`
(`vision.go:739`) already loops all addrs but callers (vision
`httpClient` at :144, S19 download client, media proxy) do not all use
it. S21 makes all-addrs the single choke.

Affected: `internal/vision/vision.go:712-771`; `httpClient` transport
at :141-159; consumers: S19 download factory, media proxy, vision fetch.

Contracts:

- `CheckHostPublic(host)` v2: `LookupIPAddr` all answers; block when
  ANY answer is non-public (deny-wins across the set); empty answer
  set -> error. Literal IPs validated without DNS. Host-with-port
  split preserved.
- `GuardedDialContext`: unchanged loop-all-then-pin-first shape, but
  MUST be the only `DialContext` on every SSRF-guarded transport
  (vision `httpClient`, S19 client, file proxy). Plain
  `(&net.Dialer{}).DialContext` on those transports is a bug.
- Redirect check calls the v2 `CheckHostPublic` (per-hop full-set
  check), same 5-hop cap.

Allowed:

- Hosts whose FULL answer set is public -> dial pinned first address.
- Literal public IPs -> direct dial.

Forbidden:

- Single-answer checks (`ResolveIPAddr` first-answer) anywhere after
  S21; `CheckHostPublic` keeps its name/signature, body becomes
  all-addrs.
- Splitting check-time and dial-time resolvers (both go through
  `DefaultResolver` + `GuardedDialContext` pinning).
- Caching DNS answers across calls (rebind defense needs fresh
  resolution per dial).

Acceptance tests (`internal/vision/`):

- `TestS21CheckHostPublicAllAddrs` - stub resolver returning
  `{public, 10.0.0.1}` -> blocked (needs resolver injection or
  `LookupIPAddr` indirection; no real DNS).
- `TestS21GuardedDialPinsValidated` - mixed-answer host never dials
  the internal leg (dial spy).
- `TestS21RedirectHopRechecked` - redirect to internal -> stopped by
  `CheckRedirect`.
- Existing `TestGuardedDialContextBlocksInternalLiterals`
  (`ssrf_test.go:46`) stays green.

Guardrails: resolver indirection via package-level `lookupIPAddr`
var (default `net.DefaultResolver.LookupIPAddr`) so tests inject
without interfaces; IPv6 zone IDs rejected; error strings keep
`blocked internal address` prefix for audit grep.

---

## S22 - pip via BuildExecCommand + bwrap (no raw exec)

Objective: pip install runs as a raw `exec.Cmd` (`agent.go:884-914`)
outside the sandbox exec choke (`BuildExecCommand`). S22 routes pip
through the same `BuildExecCommand` + bubblewrap confinement as
`system_exec`, with network left open for the package index but
filesystem still jailed.

Affected: `internal/agent/agent.go:884-914` (pip `exec.Cmd` block);
sandbox `BuildExecCommand` (shared with `system_exec`); S18 gate stays
in front (allowlist + approval unchanged).

Contracts:

- `installCmd` built by `BuildExecCommand(ctx, "system_exec",
  pipBin, "install", missingPkg)` (canonical tool identity per S14,
  so the flow binding hashes `system_exec`), carrying
  `ExecPreApproved` with the S18 gate decision: the choke skips only
  the `allowed_commands` leg (the example list has no pip) and the
  known-risk re-prompt, while hard denies, deny patterns,
  permissions/flow verdicts, and the path audit still apply.
- bwrap profile: read-only workspace except `.venv/` writable;
  network UNSANDBOXED for index access (document as explicit exception:
  pip needs egress; the SSRF/allowlist/approval gates above are the
  compensating controls, not the net jail).
- Bounded wait + process-tree kill identical to current :906-914
  (moved inside the choke, not reimplemented).
- Audit identity: `Tool:"pip"` log line preserved (human-readable),
  binding hashes `system_exec` (machine identity) - both emitted, same
  as S14 dual-emit pattern.

Allowed:

- Allowlisted + approved package -> choke-built bwrap pip, one retry.
- bwrap unavailable (Windows/macOS) -> explicit error, no silent
  raw-exec fallback.

Forbidden:

- Raw `exec.Cmd` for pip after S22 (grep `exec.Cmd{Path: pipBin` must
  find nothing).
- Network-jailed pip (would break index fetch; must be open-egress
  with the gates above as controls).
- Writable workspace outside `.venv/` during pip.

Acceptance tests:

- `TestS22PipUsesBuildExecCommand` - pip path asserts choke called
  with `system_exec` identity (stub builder), no direct exec.
- `TestS22PipBwrapProfile` - profile has `.venv` writable, workspace
  read-only, net open (assert flags, Linux; skip gracefully elsewhere).
- `TestS22NoRawFallback` - builder error -> install fails, no exec
  attempted.
- `TestPipChokeBuildExampleConfigRealGate` - example
  `allowed_commands` config + real gate: pip builds with no second
  prompt, a pre-approved audit entry, and a scrubbed child env.

Guardrails: no flow-binding change (canonical identity already
`system_exec`); Windows/macOS skip bwrap assertions, still assert choke
use; S18 tests keep passing unchanged (gate before exec).

---

## S23 - single choke (two layers, one owner per layer)

Objective: after S16-S22, prove there is exactly one owner per
process-spawn layer for agent-driven exec (ADR-008 condition 6,
two-layer choke): raw spawns live only in `exec_choke.go`
(`chokeRunPython` / `chokeCreateVenv`, scrubbed S20 env, no ambient
inheritance), and everything else routes via the shared
`BuildExecCommand` choke. S23 deletes or reroutes every remaining raw
`exec.Cmd` on the agent tool path outside `exec_choke.go` and adds a
regression test that fails any reintroduction.

Affected: `internal/agent/agent.go` (`runScript` at :776, pip at :884
post-S22, `getVenvPython` venv-creation exec at :541+); audit
`AuditCommandExec` sites; CI lint.

Contracts:

- Inventory (raw spawns only in `exec_choke.go`; every other tool path
  via `BuildExecCommand` or a named wrapper it owns): python
  `runScript` (raw, scrubbed), pip install (choke), venv creation
  (`python -m venv`, raw, scrubbed), and any `system_exec`
  background-start variant (choke).
- `getVenvPython` creation step stays inside `exec_choke.go`
  (`chokeCreateVenv`) with the scrubbed S20 env and no ambient
  inheritance (High 1 fix); it does not reroute through bwrap.
- Regression: `TestS23SingleChoke` - AST scan (`exec.Cmd`
  literals, `exec.Command*` calls, `StartProcess`) of
  `internal/agent` plus the `internal/sandbox` constructor files;
  fails when a spawn site appears outside the choke file / constructor
  allowlist, or without a `// choke-allow:` justification comment.
- Docs: choke inventory table in this file updated when the choke
  moves (no silent new paths).

Allowed:

- Exactly one file owning raw `exec.Cmd` construction
  (`exec_choke.go`); the shared `BuildExecCommand` constructors own
  the rest (`exec_unix.go`, `exec_windows.go`, `sandboxexec.go`).
  All tool paths call one of the two layers, never raw.
- Test-only raw exec in `_test.go` files (excluded from the check).

Forbidden:

- Any new `exec.Command*` / `exec.Cmd{` / `StartProcess` in
  non-test `internal/agent` outside the choke (CI fails).
- `//nolint` / allowlist growth without a corresponding ADR note.
- Dual paths (choke + legacy raw) coexisting "temporarily" - cut over
  atomically per call site.

Acceptance tests:

- `TestS23SingleChoke` - passes on the migrated tree; fails if a
  sentinel raw `exec.Cmd` is re-added (test inserts nothing, just
  scans).
- `TestS23ChokeInventory` - table asserts each tool (python, pip,
  venv, system_exec, system_exec_start) resolves to the choke builder
  (stub-injectable).
- Full `go test ./...` green with the two-layer choke as the only spawn paths.

Guardrails: AST scan covers `internal/agent/*.go` plus the
`internal/sandbox` constructor files, excluding `*_test.go`;
allowlist entries carry `// choke-allow: <reason>` on the same or
preceding line and the test enforces the comment format; S23 lands
last, after S16-S22 are green.

---

## Choke inventory (post-S23 target)

| Tool path | Builder | Net | FS writable | Env |
|---|---|---|---|---|
| `python_interpreter` run | `exec_choke.go` chokeRunPython (raw, scrubbed S20 env) | closed | tmp script dir only | scrubbed S20 (no ambient) |
| `pip install` (S18+S22) | `BuildExecCommand(system_exec)` + `ExecPreApproved` (only BuildExecCommand row) | open (index) | `.venv/` only | `ExecReplaceEnv` scrubbed S20 (no ambient) |
| venv create | `exec_choke.go` chokeCreateVenv (raw, scrubbed S20 env) | closed | `.venv/` only | scrubbed S20 (no ambient) |
| `system_exec` / `_start` | `BuildExecCommand(system_exec)` | per-policy | per-policy | ambient merge + scrub |
| `download_file` | no exec (HTTP client, S19) | guarded dial | `./downloads` | http client only |
| `save_skill` | no exec (file write, S16) | n/a | `./skills` | n/a |

## Test-name index (copy-paste)

```
TestS16SaveSkillRequiresProvenance
TestS16SaveSkillCrossSessionDenied
TestS16SaveSkillDenyWins
TestS17PreGrantSessionScoped
TestS17PreGrantTTLExpiry
TestS17PreGrantCapEvictsOldest
TestS17PreGrantEmptySessionFailClosed
TestS18PipRejectsMalformedName
TestS18PipAllowlistDenyClosed
TestS18PipRequiresApproval
TestS18PipSingleRetry
TestS19DownloadSSRFBlocked
TestS19DownloadSizeCap
TestS19DownloadRedirectReboundBlocked
TestS19DownloadTimeout
TestS20EnvScrubDropsPythonPath
TestS20NoParentEnvMerge
TestS20SkillLoadsByPath
TestS20ShadowingFails
TestChokeCreateVenvScrubbedEnv
TestS21CheckHostPublicAllAddrs
TestS21GuardedDialPinsValidated
TestS21RedirectHopRechecked
TestS22PipUsesBuildExecCommand
TestS22PipBwrapProfile
TestS22NoRawFallback
TestPipChokeBuildExampleConfigRealGate
TestBuildEnvListNoDuplicateKeys
TestIsPythonScrubbedKeyWindowsCase
TestFlowGrantEmptySessionFailClosed
TestS23SingleChoke
TestS23ChokeInventory
```
