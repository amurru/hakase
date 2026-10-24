# S24-S29 Choke Inventory (post-lane target)

One owner per layer. Raw file writes live only in the S24 choke
(`internal/sandbox/scopedfile.go`); process spawns stay owned per S23
(`internal/agent/exec_choke.go` + shared `sandbox.BuildExecCommand`).
Every other tool path calls one of the two layers, never raw.

## File-write inventory

| Tool path | Builder | Symlink posture | Overwrite rule | Env/notes |
|---|---|---|---|---|
| `write_file` create/overwrite | `sandbox.ScopedWriteFile` (S24) | O_NOFOLLOW + Lstat refusal | O_EXCL create-only, flag overwrite | skills `*.py` confined (S28 deny-first) |
| `patch` in-place edit | `sandbox.ScopedWriteFile` overwrite (S24) | O_NOFOLLOW + Lstat refusal | must exist (read first) | skills confined (S28) |
| `save_skill` persist | `sandbox.ScopedWriteFile` (S24) | O_NOFOLLOW | init create-only, code/registry overwrite | provenance gate first (S16), flow deny-wins |
| `python_interpreter` scratch | `sandbox.ScopedWriteFile` 0600 (S24) | O_NOFOLLOW | overwrite (fresh tmp per run) | scrubbed S20 env, loader preamble |
| `download_file` create | `sandbox.ScopedCreateFile` (S24) | O_NOFOLLOW + in-root | truncate preserved | partial unlinked on cap error |
| task checkpoint | `sandbox.ScopedWriteFile` create-only (S24) | O_NOFOLLOW | timestamped unique names | fixed paths only |
| task lock/audit/temp | raw with `// s24-allow:` | flock/append/rename discipline | fixed paths, never model-controlled | S29 scan enforces the comment |

## Grant inventory (S25)

| Path | Mint | Consume | Rule |
|---|---|---|---|
| pre-dispatch Ask approve | v1 + v2 twins (`grantFlowCall` + `grantFlowCallV2`) | dual (`consumeFlowGrantDual`) | one approval, one dispatch, both drained |
| python gate self-ask | (same twins via ApproveExec path) | dual | no double prompt |
| sandbox file/shell gates | (downstream of the same twins) | dual (`ConsumeFlowGrantDual`) | no leak into later turns |
| resume re-drive | twins (`ResolveApprovalPause` flow branch) | dual on re-drive | restart-safe, one-shot |
| StartLabel (v0) | either store | either store (dual-accept) | pre-upgrade grants keep working |
| narrowed (v>0) | v2 required | v2 required (fresh) | v1-only never authorizes |

## Pip inventory (S27, strict opt-in)

| Leg | Gate | Fail mode |
|---|---|---|
| config + allowlist base match | S18 unchanged | deny without asking |
| exact `==` pin on entry | `matchStrictAllowlist` | deny (entry must carry pin) |
| recorded hashes | `checkPipStrict` + `--require-hashes` argv | deny when configured-but-absent |
| extras/ranges/URLs/installer-self | `splitPipPin` | refuse, no exec |
| .venv integrity + hooks | `verifyVenvIntegrity` | deny, no choke |

Non-strict configs keep exact S18 behavior (S22 argv `[install pkg]`
unchanged). Config surface is additive (`pip_strict_pins`,
`pip_hashes`, `pip_trusted_sitecustomize`).

## Neutralize inventory (S26)

| Surface | Order |
|---|---|
| `flowDenialText` | redact, neutralize, (caller truncates) |
| `flowWithholdOverride` | redact, neutralize |
| `flowApprovalPreview` | redact, neutralize, truncate 400 |
| `flowBlockResult` | redact, neutralize (reason + tool name) |
| audit/trail | hashes only (`argsHash`, never raw) |

No tag span over 32 runes emits; over-long spans withhold with a
length-only marker. Span interiors escape nested brackets (loop until no
live `<`/`>` survives).

## Pip inventory (S27 strict, H1 single root)

One absolute venv root serves verify + choke: the pip binary is the
failing interpreter's own venv sibling (`pipBinForPyBin`, absolute), and
`verifyVenvIntegrity` runs on that root. The strict pin/hash gate runs
before the human prompt, so the approval shows the exact install
(`pip install <pin> (require-hashes:N)`); denials never prompt.

## Test-name index (29 + hardening extras)

S29 scans `internal/agent` + `internal/sandbox` non-test `.go` files for
raw `os.WriteFile/os.Create/os.OpenFile/os.CreateTemp` outside
`scopedfile.go` (or without a `// s24-allow:` justification) and for
`SessionLabel{` literals outside `flowgate.go`/`delegate.go`. Test-only
raw IO in `_test.go` is excluded.

```
TestS24ScopedExclusiveCreate
TestS24NoFollowSymlink
TestS24OverwriteNeedsFlag
TestS24EscapeDenied
TestS24ParentSymlinkEscapeDenied
TestS24RootlessParentSymlinkDenied
TestS24RootlessEvalSymlinksFailClosed
TestS25JointHashBindsAllLegs
TestS25DualAcceptStartLabel
TestS25NarrowedRequiresFreshV2
TestS25ExpiredGrantDenied
TestS25CrossSessionDenied
TestS25AgentDualRecords
TestS26BothTagsNeutralized
TestS26LongTagWithheld
TestS26Boundary32Allowed
TestS26NestedTag
TestS26DenialNeutralized
TestS27PinRequiresExact
TestS27HashesRequiredWhenConfigured
TestS27SingleVenvRootCwdDiverged
TestS27TransitiveExtrasDenied
TestS27SitecustomizeRogueDenied
TestS27VenvIntegrityMissingDenied
TestS27NoV1BreakBareName
TestS28DenyFirstOrdering
TestS28DenyBeforeStat
TestS28WriteFileSkillsConfined
TestS28NestedSkillsConfined
TestS28PatchSkillsConfined
TestS28NonSkillsWriteAllowed
TestS28NoBypassInstruction
TestS29ScanInventory
TestS29ChokeInventory
TestS29MeetOnlyLabels
```

(Register exception, outside the 29: `TestV1DeltaRegisterUnregisteredInert`,
`TestV1DeltaRegisteredHonored`.)
