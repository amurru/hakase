# S24-S29 Blueprint (Flow Guard Full Lane, hardening close-out)

Scope: S24 through S29 only. S01-S23 are done (`docs/flow-guard/spec.md`,
`plan.md`, `tasks.md`, `s16-s23-blueprint.md`). Human gate: APPROVED.
Branch: `develop`. Do NOT close #124.

Lane guardrails (apply to every slice):

1. Fail-closed on nil config, unparseable input, or missing binding keys.
2. Meet-only: label values combine only via `permissions.Meet`; new
   `SessionLabel{...}` literals only at sanctioned sites (S29 scans).
3. FlowEnabled passthrough: with no v2 policy installed every flow gate
   passes through unchanged (v1 parity); new flow renderers run only on
   flow paths.
4. Unknown-sink fail-closed on narrowed labels (C2, unchanged).
5. No v1 behavior break except through the V1-DELTA register (S25).
6. Hash-only audit/logs: never raw args, raw code, or raw paths beyond
   the redacted display the human already sees.

Global NFRs: no new tools, no policy JSON schema change, no label-version
bump; every slice ships a hermetic stubbed test (no network);
`gofmt -l`, `go vet ./...`, `go test ./...` green.

---

## S24 - scoped exclusive file creation (O_EXCL/O_NOFOLLOW)

Objective: tool-path file writes must not follow attacker-planted
symlinks and must not clobber on a create-only path. All writes go
through one choke with three properties: scoped confinement (Clean +
join + prefix check, EvalSymlinks re-check after MkdirAll against a
planted symlinked parent), O_EXCL on create-only writes (stat + create
are one atomic step), O_NOFOLLOW on every open (unix; Lstat pre-check
everywhere, Windows relies on the Lstat leg).

Affected: new `internal/sandbox/scopedfile.go` (+ `scopedfile_unix.go`,
`scopedfile_windows.go`); `internal/sandbox/fileops.go` write_file +
patch; `internal/agent/agent.go` save_skill writes, download create,
task checkpoint; `internal/agent/python_env.go` script write.

Contracts:

- `ScopedWriteFileInRoot(root, name, data, perm, overwrite)` - confine,
  MkdirAll parents, EvalSymlinks re-check, Lstat chain refusal, single
  O_NOFOLLOW open (O_EXCL when overwrite=false). Returns resolved abs.
- `ScopedWriteFile(path, data, perm, overwrite)` - same without root
  confinement (caller path already resolved by taskResolve/ResolveScopedPath).
- `ScopedCreateFile(root, path, perm)` - streaming-create variant for
  downloads (O_NOFOLLOW, truncate semantics preserved, caller unlinks
  partials on error as today).

Allowed: nested creates inside root; overwrite=true replacing a
regular file; checkpoint timestamped create-only names.

Forbidden: following a final-component or parent symlink (refuse);
create-only clobber (refuse); `..` escapes outside root (refuse).

Acceptance tests (`internal/sandbox/scopedfile_test.go`):

- `TestS24ScopedExclusiveCreate`
- `TestS24NoFollowSymlink`
- `TestS24OverwriteNeedsFlag`
- `TestS24EscapeDenied`
- `TestS24ParentSymlinkEscapeDenied`

---

## S25 - joint-hash v2 grants, dual-accept, fresh approval

Objective: bind the four grant legs (session, tool, argsHash,
labelVersion) into one joint hash minted as a v2 record alongside the
v1 record on every fresh approval (dual records, one human decision).
Consumption is dual-accept: StartLabel accepts v1 OR v2 (pre-upgrade
grants keep working, v1 parity); narrowed labels require the v2 joint
record (fresh approval - a v1 record minted before the upgrade, or for
any other version, never authorizes a narrowed call). One-shot across
both stores: one dispatch drains both twins.

Affected: new `internal/permissions/flow_grant_v2.go`,
`internal/permissions/v1delta.go`, `internal/agent/flow_grant_v2.go`;
wiring in `internal/agent/flowremedy.go` (ApproveFlowAsk dual mint +
dual consume/peek), `internal/agent/agent.go` python gate,
`internal/agent/resume.go` flow resume mint,
`internal/sandbox/flow.go` downstream gates.

Contracts:

- `JointGrantHash(sessionID, tool, argsHash, labelVersion)` - one
  ArgsHash over all four legs (NUL-separated, canonical tool).
- `GrantFlowBindingV2 / HasFlowGrantV2 / ConsumeFlowGrantV2` - v2 store
  (1024 cap, 15m TTL, lazy expiry, empty-session versioned never grants).
- `ConsumeFlowGrantDual / HasFlowGrantDual` - StartLabel dual-accept,
  narrowed v2-only; consume drains both twins.
- V1-DELTA register: `RegisterV1Delta/UnregisterV1Delta/
  IsV1DeltaRegistered(action)`; v0/v1 `Compile` accepts flow keys only
  for registered actions; `DeltaFor` honors deltas for registered
  actions under v1. Production registers nothing (stock v1 byte-identical).

Allowed: StartLabel v1 records keep authorizing (dual-accept); fresh
approvals mint both twins; resume approvals mint both twins.

Forbidden: v1-only record authorizing a narrowed call; cross-session
consume; TTL refresh on read; grant persistence to disk.

Acceptance tests (`internal/permissions/flow_grant_v2_test.go`,
`v1delta_test.go`, `internal/agent/s25_test.go`):

- `TestS25JointHashBindsAllLegs`
- `TestS25DualAcceptStartLabel`
- `TestS25NarrowedRequiresFreshV2`
- `TestS25ExpiredGrantDenied`
- `TestS25CrossSessionDenied`
- `TestS25AgentDualRecords`
- (`TestV1DeltaRegisterUnregisteredInert`,
  `TestV1DeltaRegisteredHonored` cover the register exception.)

---

## S26 - output tag neutralization, no tag span over 32

Objective: model- and human-visible flow text (denials, withhold
overrides, approval previews) must not carry live markup or over-long
opaque tokens. Both angle-tag families (open `<tag ...>` and close
`</tag>`) are neutralized by bracket escaping; any single tag span
longer than 32 runes is replaced with a length-only marker
(fail-closed, content hash already rides the audit argsHash).

Affected: new `internal/agent/flow_neutralize.go`; wired into
`flowDenialText`, `flowWithholdOverride`, `flowApprovalPreview`
(redact-then-neutralize-then-truncate; existing caps unchanged).

Contracts:

- `neutralizeFlowTags(s) (string, bool)` - escape `<`/`>` spans;
  >32-rune spans become `[tag-withheld:len=N]`; bare brackets escape.
- Pure function, no policy state: renderers run only on flow paths, so
  FlowEnabled passthrough holds by construction.

Allowed: clean text passes through unchanged; 32-rune spans escape
in place.

Forbidden: emitting a live `<...>` span; emitting raw span content
over 32 runes; logging raw tag content outside hashes.

Acceptance tests (`internal/agent/s26_test.go`):

- `TestS26BothTagsNeutralized`
- `TestS26LongTagWithheld`
- `TestS26Boundary32Allowed`
- `TestS26DenialNeutralized`

---

## S27 - pip exact pins, require-hashes, transitive, .venv integrity

Objective: under opt-in `PipStrictPins` (additive, default off = exact
S18 behavior), auto-pip installs only exact `name==version` pins carried
by the allowlist entry (the pin is what installs), requires recorded
sha256 hashes when `PipHashes` is configured (choke installs with
`--require-hashes` + `--hash`, so transitive swaps fail the install),
refuses extras/ranges/URLs/markers (unpinned transitive closures) and
the installer itself, and gates on .venv integrity (pyvenv.cfg present,
no unexpected sitecustomize.py/usercustomize.py import hooks unless
their content hash is trusted).

Affected: `internal/sandbox/sandbox.go` config surface
(`PipStrictPins`, `PipHashes`, `PipTrustedSitecustomize` + JSON +
loader copy); new `internal/agent/pip_pin.go`; wiring in
`internal/agent/pip_install.go` (post-S18 strict branch) and
`internal/agent/exec_choke.go` (`runPipInstallViaChokeArgs`, same
no-raw-fallback contract; non-strict argv stays exactly
`[install pkg]` per S22).

Contracts:

- `splitPipPin(entry)` - `name` or `name==version` only.
- `matchStrictAllowlist(sb, reqName)` - base-name match + exact pin
  required; returns pin + recorded hashes.
- `pipInstallArgs(pin, hashes)` - `["install", pin]` or with
  `--require-hashes` + `--hash=sha256:<h>` legs.
- `verifyVenvIntegrity(venvRoot, trusted)` - pyvenv.cfg required;
  unexpected hook files refuse unless content-trusted.
- `checkPipStrict(sb, pkg, pipBin)` - pin + hash + integrity gate;
  denials audit with hashed names, original interpreter output kept.

Allowed: pinned + hashed + approved installs under strict; everything
as before with strict off (no v1 break).

Forbidden: bare entries authorizing under strict; extras/URLs/ranges;
installing into an unverified venv; raw package names in audit (hashes).

Acceptance tests (`internal/agent/s27_test.go`):

- `TestS27PinRequiresExact`
- `TestS27HashesRequiredWhenConfigured`
- `TestS27TransitiveExtrasDenied`
- `TestS27SitecustomizeRogueDenied`
- `TestS27VenvIntegrityMissingDenied`
- `TestS27NoV1BreakBareName`

---

## S28 - deny-first + write_file skills confinement, close :2182

Objective: deny-first ordering (a deny anywhere in the chain refuses;
Ask never downgrades a Deny) is pinned for write paths; `write_file` /
`patch` into the executable Python skill library (`<any>/skills/*.py`,
`__init__.py`, `skills.json`) refuse with the save_skill alternative
(S16 provenance gate owns that domain), checked before MkdirAll so deny
always wins over the write; and the orchestrator instruction that told
the model to route around a blocked write_file via system_exec is
closed (deny-first: a blocked write stays blocked - stop and ask).

Affected: new `internal/sandbox/skillsconfine.go`
(`denySkillsWrite`); `internal/sandbox/fileops.go` write/patch gates;
`internal/agent/agent.go` `markdownSkillsInstruction` const composed
into the orchestrator prompt.

Contracts:

- `denySkillsWrite(absPath)` - confined set above; non-code files
  under skills/ and markdown discovery trees stay writable.
- Instruction carries: never bypass a write_file denial via system_exec
  (deny-first); on total blockage stop and ask.

Allowed: ordinary writes; non-code files under skills/; markdown
skills via write_file to discovery paths.

Forbidden: direct executable persistence outside save_skill; any
tool-routing around a denial.

Acceptance tests (`internal/sandbox/skillsconfine_test.go`,
`internal/agent/s28_test.go`):

- `TestS28DenyFirstOrdering`
- `TestS28WriteFileSkillsConfined`
- `TestS28PatchSkillsConfined`
- `TestS28NonSkillsWriteAllowed`
- `TestS28NoBypassInstruction`

---

## S29 - scan + inventory (lands last)

Objective: prove the lane holds - no raw model-controlled file write
bypasses the S24 scoped choke, no new label-construction site bypasses
Meet-only discipline, and every S24-S29 tool path resolves to its choke
entry point. Lands after S24-S28 are green; fails the lane on any
reintroduction.

Affected: `internal/agent/s29_test.go` (scans agent + sandbox dirs);
docs `s24-s29-inventory.md` (choke table + 29-name index);
`docs/architecture/adr-009-s24-s29.md` (7 binding conditions).

Contracts:

- `TestS29ScanInventory` - AST scan for `os.WriteFile/os.Create/
  os.OpenFile/os.CreateTemp` outside `scopedfile.go` or without a
  `// s24-allow:` justification; text scan for `SessionLabel{`
  outside `flowgate.go`/`delegate.go` (sanctioned sites).
- `TestS29ChokeInventory` - functional round-trip per slice.
- `TestS29MeetOnlyLabels` - Meet narrowing-only spot check.

Allowed: choke-owned opens; justified fixed-path lock/audit/temp
sites with `// s24-allow:`; test-only raw exec/IO in `_test.go`
(excluded from scans).

Forbidden: new raw write sites; new label literals; allowlist growth
without a matching ADR note.

Acceptance tests (`internal/agent/s29_test.go`):

- `TestS29ScanInventory`
- `TestS29ChokeInventory`
- `TestS29MeetOnlyLabels`
