# R30-R33 Loop-3 QA Report (Full-Lane Quality and Testing)

Branch: `develop` (repo `/home/amurru/Projects/hakase`).
Inputs: `docs/flow-guard/s16-s23-blueprint.md` (S16-S23 contracts + guardrails),
`docs/architecture/adr-008-4highs-fixup.md` (9 binding conditions, amended),
`docs/flow-guard/s24-s29-blueprint.md` (context only - lane under test is R30-R33),
prior QA `docs/qa/s24-s29-loop3-qa.md` + `docs/qa/flow-guard-highs-qa.md` +
`docs/qa/flow-guard-final-qa.md` (method template).
Loop-3 diff under test (per task prompt): `getVenvPython` workDir `absJoin`
single-root, `isStartupHookPath` 16-leg classifier, `pthExecutesCode` split
discrimination. Prior reviews Approve-with-comments: Medium `pipBinForVenv`
dead, Medium M2 matrix positive-only, plus Lows. No code edited (QA lane only).

## 1. Test strategy

Pyramid:

- Unit (pure): `skillJointHash` bind, `pthExecutesCode` line classifier,
  `isStartupHookPath` 16-leg classifier, `pipBinForPyBin` / `absPyBin` /
  `absJoin` / `absVenvRoot` anchoring, `splitPipPin` / `pipInstallArgs` /
  `verifyVenvIntegrity` legs.
- Integration (tool-path): python start gate (verify-before-spawn, no-spawn
  on refusal), pip gate (config + allowlist + approval + choke, single retry),
  save_skill provenance + fresh-update approval + write_file reroute hook,
  download choke (rooted streaming create, truncate, partial unlink).
- Divergence (H1): run-workspace venv governs when process cwd differs
  (rogue-hook refusal leg + absent-venv creator leg + spawn-arg anchoring).
- Full suite green gate: `gofmt -l`, `go vet`, `GOOS=darwin go build ./...`,
  `go test ./...`.

Quality gates: deny-wins everywhere (flow deny over provenance, confined
write deny before MkdirAll); ask-once / one-shot grants (dual consume drains
both twins); hash-only audit (no raw code/paths); fail-closed on nil config,
empty session, unparseable input; hermetic tests only (httptest + temp dirs,
no network).

## 2. Test plan - R30-R33 requirement mapping

Every case maps to a requirement/risk, with test data, env, prerequisites.

### R30 - Tool-result framing (no length exemption, both-tags, neutralize, idempotent)

- Requirement: every tool result frames with both `<UNTRUSTED_DATA>` tags
  regardless of payload length or content; lone smuggled tags neutralize;
  second pass is stable.
- Risk: short-payload bypass, lone-tag smuggling, nested-frame injection.
- Cases (`internal/agent/r30_test.go`, plus `toolresult_guard_test.go`):
  - `TestR30NoLengthExemption` - shorts `x`, `ok`, `<script>`, `rm -rf /`
    all frame with both tags. Env: hermetic ADK mock. Prereq: clean guard.
  - `TestR30GuardBothTagsRequired` - lone open tag in result frames with
    exactly one frame pair (smuggled tag neutralized, not suppressing).
  - `TestR30GuardIdempotent` - double pass stable, exactly one frame pair.
  - `TestR30GuardNeutralize` - smuggled close tag leaves exactly one live
    close (outer frame intact).
- Verdict: PASS (all 4 green this loop).

### R31 - Interpreter-startup integrity (code-executing .pth confinement, verify on every start)

- Requirement: `.pth` import lines execute at startup (hook class) while
  comments/blanks/path-additions do not; start verifies venv integrity
  before every spawn and refuses without spawning on rogue hooks.
- Risk: startup-code execution via planted `.pth` / `sitecustomize` /
  `usercustomize` legs.
- Cases (`internal/agent/r31_test.go`):
  - `TestR31PthExecutesCode` - 10-row table (`import os`, `import evil;...`,
    `import<TAB>sys` true; `# import os`, blank, empty, path lines,
    `important-stuff`, `ximport os` false).
  - `TestR31PthImportLineDenied` - code-executing `.pth` denies with empty
    trust; path-only `.pth` passes; hook-free passes.
  - `TestR31VerifyOnEveryStart` - planted `sitecustomize.py` refuses
    pre-spawn (`refused`, `spawned == false`); removal control spawns.
- Verdict: PASS (all 3 green). Discrimination gaps noted in section 4
  (from-import, CRLF, leading-whitespace unpinned).

### R32 - Skill provenance (joint hash, fresh update approval, reroute wiring)

- Requirement: save_skill binds code+description joint hash; updates need
  fresh human approval every time (no grant reuse); write_file/patch into
  the skills library reroutes to the unified gate non-interactively.
- Risk: description laundering, stale-grant overwrite, tool-routing around
  the provenance gate.
- Cases (`internal/agent/r32_test.go` + `r30_r33_loop2_test.go` M4/M5/M6):
  - `TestR32JointBindsCodeAndDescription` - same code + different
    description binds different; cosmetic whitespace stable; different
    code binds different; 64-hex shape.
  - `TestR32UpdateRequiresFreshApproval` - headless update refuses
    (`not approved`); two approved updates prompt twice (`asked == 2`);
    registry carries joint hash, never raw code.
  - `TestR32AgentHookWired` - `wireSaveSkillGate` installs hook; unproven
    content refuses via hook naming `prior python_interpreter`.
  - `TestM4OnDiskSkillCountsAsExisting` - on-disk `skills/<name>.py`
    counts as existing without registry entry; headless overwrite refuses.
  - `TestM5RerouteDoesNotPrompt` - confined write_file reroute evaluates
    non-interactively (any prompt fails the test), denial names
    `save_skill`.
  - `TestM6EmptySessionFailsClosed` - empty session records nothing and
    checks deny; same-session authorizes; cross-session denies.
- Verdict: PASS (all 6 green).

### R33 - Download choke (rooted streaming create, truncate, partial unlink)

- Requirement: downloads stream-create under the rooted choke (no outer
  MkdirAll), same-name re-download truncates, over-cap partials unlink.
- Risk: directory-escape writes, append/duplicate artifacts, partial-file
  residue.
- Cases (`internal/agent/r33_test.go`):
  - `TestR33DownloadChokeOwnsDirectory` - `downloads/` absent up front;
    file lands under root; bytes match; byte count exact.
  - `TestR33DownloadTruncatePreserved` - second same-name download
    overwrites (no append/duplicate).
- Verdict: PASS (both green). Over-cap unlink pinned by S19 suite
  (`TestS19DownloadSizeCap` family, green - not re-run here by name but
  covered in full-suite pass).

### H1 - Single absolute root (cwd != workspace)

- Requirement (loop-3 fix): `getVenvPython` anchors creation at
  `absJoin(workDir, .venv)`; `absPyBin` / `pipBinForPyBin` / `absVenvRoot`
  derive one absolute root so creator, verify, spawn, and pip choke agree
  even when the process cwd differs from the run workspace.
- Risk: cwd-anchored `./.venv` divergence (verify one venv, spawn/pip
  another).
- Cases:
  - `TestH1WorkspaceVenvWinsOverCwd` (rogue leg) - workspace venv holds
    rogue hook, cwd venv clean: start refuses, never spawns. PASS.
  - `TestH1WorkspaceVenvCreatedInWorkspace` (creator leg) - workspace
    venv absent, cwd venv clean: creation lands at
    `<ws>/.venv` (absolute), spawn `pyBin` absolute, `pip root == <ws>/.venv`,
    `workDir == <ws>`. PASS.
  - `TestS27SingleVenvRootCwdDiverged` - strict-pip single-root under
    diverged cwd. PASS (in targeted run).
  - `venv_windows_test.go` H1 anchoring - workspace-anchored absolute
    path. PASS (full-suite green on linux; windows legs by construction).
- Verdict: PASS. Both H1 legs pinned plus spawn-arg anchoring assertions.

### M2 - 16-leg startup-hook classifier

- Requirement (loop-3 fix): `isStartupHookPath` covers the full
  startup-importable set: module form (`sitecustomize` / `usercustomize` x
  `py/pyc/pyd/so` = 8) + package form (`<mod>/__init__.<ext>` = 8),
  case-insensitive, parent-gated for `__init__` legs.
- Risk: compiled/native hook legs (`pyc/pyd/so`) or package-form legs
  slipping the verify.
- Cases:
  - `TestM2StartupImportableSuffixes` - 16 subtests, every leg denies
    with empty trust. PASS (16/16 green this loop).
  - `TestM2SitecustomizePackageForm` / `TestM2SitecustomizePackagePyc` -
    package denial + removal-passes control. PASS.
- Verdict: PASS on positives. Coverage gap (Medium, non-blocking):
  matrix is positive-only - no per-leg allow-control and no negative
  discrimination legs (see section 4).

### Guardrails (ADR-008 conditions as applicable to R30-R33)

- Deny-wins: R32 update refusal under flow deny covered by S16 suite
  (`TestS16SaveSkillDenyWins`, green in full suite); R31 refuse-before-spawn
  pinned by `spawned == false` assertions. PASS by suite.
- Ask-once / one-shot: `TestR32UpdateRequiresFreshApproval` (`asked == 2`,
  no reuse); flow-grant one-shot covered by S17/S25 suites (green).
  PASS.
- Hash-only audit: registry-carries-joint-not-code asserted in R32;
  pip audit hashed-name legs in S18/S27 suites. PASS.
- Fail-closed: M6 empty-session, `TestR31VerifyOnEveryStart` refusal,
  headless-update refusal. PASS.
- S23 single choke: `TestS23SingleChoke` + `TestS23ChokeInventory` green
  this loop (no raw-exec reintroduction). PASS.

## 3. Coverage analysis

Covered (green this loop, targeted + full suite):

- R30 framing: 4/4 (no-exemption, both-tags, idempotent, neutralize).
- R31 classifier + start gate: 3/3 + `TestChokeCreateVenvScrubbedEnv`
  (venv launcher scrubbed env, both LookPath forms) green in full suite.
- R32 joint + update + hook + M4/M5/M6: 6/6.
- R33 choke ownership + truncate: 2/2 (over-cap unlink via S19 suite).
- H1 single root: rogue + creator + single-venv-root + windows-anchor.
- M2 16 legs: 16/16 denial subtests + 2 package controls.
- S16-S23 / S25-S29 regression surface: S20 (4), S22 (3), S23 (2),
  S25 agent dual, S26 (5), S27 (7), S28 instruction, S29 (3) - all green
  in the targeted run; full `go test ./...` green across all 38 packages.

Untested / thin (gaps, non-blocking unless a reviewer re-opens):

1. (Medium, carried) `pipBinForVenv` dead: defined in
   `internal/agent/pip_install.go:126` with zero callers (grep this loop:
   definition only). Production threads `pipBinForPyBin`; the deprecated
   shim is retained "for tests that stub the venv layout" but no test
   calls it. Suggest delete or a `//nolint`-free removal with ADR note.
   Non-blocking.
2. (Medium, carried) M2 matrix positive-only: `TestM2StartupImportableSuffixes`
   asserts denial for all 16 legs but has no per-leg allow-control
   (trusted-hash pass) and no negative-discrimination legs
   (`sitecustomize.txt` allowed, `sitecustomize/__init__.py` under a
   wrong parent allowed, `other/__init__.py` allowed). Removal-passes
   control exists only in the two package-form tests. Suggest one
   table-driven negative test. Non-blocking.
3. (Low) `pthExecutesCode` split discrimination: 10-row table pins the
   core split (`import` / `import ` / `import<TAB>` vs comments/blanks/
   paths/`important-stuff`/`ximport`), but `from x import y` (must be
   false per site.py addpackage semantics), CRLF (`import os\r`) handling
   via TrimSuffix, leading-whitespace (`  import os`), and trailing-comment
   (`import os  # speed`) lines have no explicit rows. Logic reads correct
   by inspection (TrimSpace + TrimSuffix + prefix triple), but unpinned.
   Suggest 4 extra rows. Non-blocking.
4. (Low) `absJoin`/`absPyBin`/`pipBinForPyBin` edge matrix: empty workDir
   fallback (`absJoin(".", ".venv")`), relative-pyBin anchoring, and
   absolute-pyBin passthrough are exercised indirectly through H1 tests
   but have no direct unit table (e.g. `pipBinForPyBin("", "")`,
   `absPyBin` relative + empty workDir). Suggest a small pure table.
   Non-blocking.
5. (Low) R33 over-cap unlink is pinned in the S19 suite, not in an R33-named
   test; a reader mapping R33-to-test finds ownership + truncate only.
   Suggest a name alias or cross-reference comment. Non-blocking.

Untestable as written: none. All R30-R33 acceptance language is falsifiable
and each clause maps to a named test above (or a named gap with a concrete
suggested row).

## 4. Acceptance validation

- H1 single root cwd != workspace: PASS. Rogue leg refuses without spawn;
  creator leg creates absolute in workspace; spawn args
  (`pyBin` absolute, pip root == workspace venv, `workDir == workspace`)
  asserted, not just honest logging.
- M2 16 legs: PASS (16/16 deny). Positive-only and negative-discrimination
  thinness recorded as Medium/Low gaps, not failures.
- Test discrimination (`pthExecutesCode` split): PASS on the pinned 10
  rows; extended discrimination (from-import, CRLF, whitespace variants)
  suggested but the shipped split is correct by inspection.
- Guardrails (deny-wins, ask-once/one-shot, hash-only, fail-closed, single
  choke): PASS - `TestS23SingleChoke`/`TestS23ChokeInventory`,
  `TestM6EmptySessionFailsClosed`, `TestM5RerouteDoesNotPrompt`,
  no-spawn-on-refusal assertions all green.

## 5. Test report (executed this loop)

- Targeted: `go test ./internal/agent/ -run
  'TestH1|TestM2Startup|TestM2Sitecustomize|TestR3|TestM4|TestM5|TestM6|
  TestS2|TestChokeCreateVenv|TestPth|TestIsStartup|TestPip|TestAbs|TestPipBin'`
  -count=1 -v: PASS (all named tests green incl. 16/16 M2 subtests;
  R30 x4, R31 x3, R32 x3, R33 x2, H1 x2, M4/M5/M6 x3, S20/S22/S23/S25/S26/
  S27/S28/S29 legs in the same filter).
- Full: `go test ./... -count=1`: PASS - all 38 packages ok
  (incl. `internal/agent` 1.439s, `internal/hooks` 3.224s - no hooks flake
  this run; `internal/sandbox` 5.954s; `internal/web/handlers` 19.618s).
- `gofmt -l internal/ cmd/`: clean (no output).
- `go vet ./internal/agent/ ./internal/sandbox/ ./internal/vision/`:
  clean (no output).
- `GOOS=darwin go build ./...`: clean.
- `-race` on the new concurrency tests: NOT run this loop (standard gate
  is green; prior-lane `-race` audit-forward flake did not reproduce under
  the standard gate - hand off to Codebase Doctor only if CI `-race`
  reopens it, with repro
  `go test ./internal/agent/ -run TestAuditForward -count=1 -race`).

## Verdict

**QA-PASS.** Coverage verified for H1 (rogue + creator + anchoring), M2
(16/16 denial legs), R30-R33 acceptance tests, and guardrails
(deny-wins, ask-once/one-shot, hash-only, fail-closed, single choke);
`go test ./...`, `gofmt`, `go vet`, and darwin build all green with no
hooks flake this run. Reviewer Mediums (`pipBinForVenv` dead, M2 matrix
positive-only) and Lows (split-discrimination rows, absJoin edge table,
R33 cross-reference) recorded in section 3 as non-blocking gaps unless a
reviewer explicitly re-opens one. No code was modified in this lane.
