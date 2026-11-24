# Gate param-expansion fail-closed - QA validation (docs/agent-workflow, uncommitted hardening)

**Date:** 2026-11-24 (UTC), originally 2026-11-23; 54th loop update
**Repo:** `/home/amurru/Projects/hakase`, branch `docs/agent-workflow`
**HEAD:** `02761b2` + staged + unstaged hardening (gate.go + systemexec.go + test files)
**Inputs:** implementation `ses_df6b7574` (tarFamilyBases+makeFamilyBases carrier + tail fail-closed, flipped trailing Ask) + reviewer `ses_df644ed3` Approve (F1 High carrier sudo -s/su -c payload Ask missing to file as own issue, F2 Low makefile boundary, F3 Low benign tar $VAR Ask by design, F4 Info make -f Deny policy) + reviewer `ses_df585914` (F5 High command/exec/builtin/!/if/then/do/{/}/( prefix opacity hiding hard-deny carrier/-c legs, Medium duplicate prefix walks, Low this stale doc, Info depth Ask/Deny asymmetry) + reviewer `ses_df544c83` (High attached opener on carrier: `(sudo`/`{sudo`/`(chmod` classify as unknown while `( sudo` denies; Medium eval not in shared prefix / not re-lexed like shell -c; Lows QA GOOS inconsistency, perf test discarded verdict)
**Method:** read-only verification, then the 53rd-loop implementation of the F5 reachability fix in `gate.go` + the sandbox `StripCdGroupOpeners` accessor, then the 54th-loop attached-opener + eval fix in the same shared walk. No commits or pushes in this validation pass; no ruleset touches.

## Suite results (reproduced)

- Targeted: `go test ./internal/agent/ -run 'TestGateFailClosedParamExpansion|TestParamExpansionAskReasonTable|TestGateShellPrefixWordsTransparent|TestShellPrefixWalkAgreement|TestSudoShellPayloadChainLinear' -v` - **PASS** (all subtests, including the new attached-opener and eval rows).
- Full: `go test ./internal/agent/ ./internal/sandbox/` - **PASS** (`internal/agent` 1.422s ok, `internal/sandbox` 3.773s ok).
- `go vet ./internal/agent/ ./internal/sandbox/` - **PASS** (exit 0, no output).
- `gofmt -l internal/agent/ internal/sandbox/` - **clean** (no output).
- `GOOS=windows go build ./...` - **PASS** (exit 0; package cross-compile only, `_test.go` files are not built by `go build`).
- `GOOS=darwin go build ./...` - **PASS** (exit 0; package cross-compile only, `_test.go` files are not built by `go build`).
- Probe (temp `TestZZProbe`, removed after run), under `round2AllowSB`:
  - `make $1`, `gtar $1`, `tar -xf a.tar $1`, `tar -xf ${PROBE_UNSET_XYZ}` -> **Ask** (reason `make/tar/gtar argument with parameter expansion requires user approval`).
  - `echo tar ${PROBE_UNSET_XYZ}`, `echo make ${PROBE_UNSET_XYZ}`, `echo hi` -> **Allow** (greens preserved).
  - `tar ${1:-cI} prog -f a.tar` -> **Deny** (`tar -I/-F program executes commands`); `rm -rf /` -> **Deny** (no deny->allow regression).
  - Attached opener: `(sudo -s 'rm -rf /')`, `{sudo -s 'rm -rf /' ; }`, `(bash -c 'rm -rf /')`, `(su -c 'rm -rf /')`, `(chmod -R 777 /)`, `{chmod -R 777 / ; }` -> **Deny** (was Ask as unknown command); `( sudo -s ...)` still Deny (no change).
  - eval: `eval sudo -s 'rm -rf /'`, `eval 'rm -rf /'`, `eval bash -c 'rm -rf /'`, `(eval 'rm -rf /')` -> **Deny** (was Ask); `eval sudo -s 'tar $1'` -> **Ask** (tar parameter-expansion reason); `echo eval 'rm -rf /'`, `echo eval sudo -s 'rm -rf /'` -> **Allow** (greens preserved).

## Acceptance (falsifiable, explicit)

| # | Requirement | Test / evidence | Result |
|---|-------------|-----------------|--------|
| 1 | `make $1` fails closed to Ask under allow | `gate_failclosed_cover_test.go:TestGateFailClosedParamExpansion` ask list includes ``make $1``, ``make ${HK_GATE_T_UNSET}``, ``gmake $@``, ``make -C /tmp $1``, ``make all ${HK_GATE_T_UNSET}``; probe confirms `make $1` -> Ask | **YES** |
| 2 | `gtar $1` fails closed to Ask (tar-family alias) | Same test ask list includes ``gtar $1``, ``bsdtar ${HK_GATE_T_UNSET}``, ``sudo gtar ${HK_GATE_T_UNSET}``, ``gtar -xf ${HK_GATE_T_UNSET}``; probe confirms | **YES** |
| 3 | `tar -xf ${X}` trailing expansion fails closed to Ask (flipped trailing Ask) | Same test ask list includes ``tar -xf archive.tar --to-${HK_GATE_T_UNSET}md prog``, ``tar -xf a.tar $1``, ``tar -xvf $@``, ``tar -t $1``; `TestParamExpansionAskReasonTable` pins `tar following operand`, `tar list trailing positional`, `tar bundle trailing at` wantAsk; probe confirms `tar -xf ${PROBE_UNSET_XYZ}` -> Ask | **YES** |
| 4 | `echo` greens preserved (no over-Ask) | `TestGateFailClosedParamExpansion` allow-prose block (``echo "${HK_GATE_T_X}"``, ``echo tar ${HK_GATE_T_UNSET}``, ``echo make ${HK_GATE_T_UNSET}``) + `TestGateFailClosedTarDashlessParamExpansion` green block (`echo tar cI`, `echo tar ${X}`, ...); probe confirms `echo tar/make ${...}` -> Allow | **YES** |
| 5 | No deny->allow regression | Deny pins still Deny under allow: ``tar ${1:-cI} prog -f a.tar`` family (`TestGateFailClosedTarDashlessParamExpansion` deny block), ``sort --c${H}...``, ``rm -rf ${X}`` (`TestGateFailClosedParamExpansion` deny block), plus full `internal/agent` suite green; probe confirms `tar ${1:-cI}` and `rm -rf /` still Deny | **YES** |
| 6 | tarFamilyBases+makeFamilyBases carrier + tail fail-closed present | `gate.go:paramExpansionAskReason` (tar/make-family tail opaque: `commandPosition && (tarFamilyBases[bin] \|\| makeFamilyBases[bin])` -> Ask), `tarFamilyBases={tar,gtar,bsdtar}`, `makeFamilyBases={make,gmake}`; `TestParamExpansionAskReasonTable` pins all carrier positions | **YES** |
| 7 | `make -f` Deny policy preserved (F4 Info) | `TestCandidateFormsAgreement` deny block includes ``sudo -s 'make -f evil.mk'``, ``su -c 'make -f evil.mk'``, ``echo "$(make -f evil.mk)"`` via per-view + string-window make legs; full suite green | **YES** (policy, no change) |
| 8 | Attached subshell/group opener on a carrier classifies like its bare form | `TestGateShellPrefixWordsTransparent` deny rows ``(sudo -s 'rm -rf /')``, ``{sudo -s 'rm -rf /' ; }``, ``(bash -c 'rm -rf /')``, ``(su -c 'rm -rf /')``, ``(chmod -R 777 /)``, ``{chmod -R 777 / ; }``; hard-deny segments are normalized once with `normalizeHardDenySegment` (sandbox `StripCdGroupOpeners`) and the token matchers compare `resolveBinaryBase(shellPrefixClassToken(tok))` | **YES** |
| 9 | `eval` is a command-position carrier and re-lexes its quoted operand | `TestGateShellPrefixWordsTransparent` deny rows ``eval sudo -s 'rm -rf /'``, ``eval 'rm -rf /'``, ``eval bash -c 'rm -rf /'``; Ask rows ``eval sudo -s 'python3 evil.py'``, ``eval 'tar $1'``; greens ``echo eval 'rm -rf /'``, ``echo eval sudo -s 'rm -rf /'`` | **YES** |

## Coverage gaps (untested paths / ambiguous requirements)

1. **F1 - packed carrier payload Ask missing (High) - CLOSED 2026-11-24.** `paramExpansionAskReason` runs on top-level segments; packed/quoted `sudo -s` / `su -c` payloads are now also run through `carrierPayloadAskEscalation` (from `carrierInterpreterEscalation`), which calls the shared `shellAskLegs`. Falsifiable case: `EvaluateCommand(allow, "sudo -s 'tar $1'", nil)` and `("su -c 'make $1'")` now **Ask**, never Allow; `echo sudo -s 'tar $1'` stays green.
2. **F2 - makefile boundary (Low, accepted).** `make ${ZZ_EVAL}` (unset, substitutes to empty) stays out of Deny by design (`TestCandidateFormsAgreement` asserts not-Deny); the boundary between "empty expansion, make sees no flag" vs "unresolvable tail, fail closed to Ask" is pinned only for that one variable name. Other spellings (`make $1` with `$1` empty at runtime) still Ask by the tail rule - intentional fail-closed. No further test needed unless the boundary is redefined; note as **testable but by-design**.
3. **F3 - benign `tar $VAR` Ask (Low, by design).** Any unresolvable expansion in tar/make tail Asks, even when the runtime value would be benign. Pinned by `tar bare positional`, `gtar bare positional`, `make bare positional` rows. **Accepted tradeoff, explicitly pinned** - do not "fix" without changing the fail-closed contract.
4. **Tail vs flag-position distinction for non-tar/make.** `paramExpansionAskReason` only Asks on `-`/ `=` tokens for sort/git/chmod/chown/rm/dd; a bare `${X}` after those binaries (e.g. `sort ${X}`) stays out unless it is flag-shaped. Pinned by `sort flag` vs no bare-sort-Ask row. If the contract ever extends the tail rule beyond tar/make, add rows; currently **by design, covered**.
5. **`commandPositionFor` prose guard.** `echo tar ${X}` stays green because `tar` is not in command position. Pinned by `tar argument in prose` / `make argument in prose` rows. Edge: `command tar ${X}` (explicit `command` wrapper) IS in command position and Denies/asks via the dashless walk - pinned in the tar-dashless test. No gap, but any change to `commandPositionFor` must update both tables together.

## Residuals (reviewer F1-F4 disposition)

- **F1 High - carrier `sudo -s` / `su -c` payload Ask missing: CLOSED (2026-11-24).** The Ask half now runs on re-lexed carrier payloads via `carrierInterpreterEscalation` -> `carrierPayloadAskEscalation` (shared `shellAskLegs` + nested carrier depth budget). Reproduced: `sudo -s 'tar $1'`, `su -c 'tar $1'`, `sudo -s 'make $1'`, `su -c 'make $1'` all Ask with the tar/make parameter-expansion reason; `echo sudo -s 'tar $1'` stays green.
- **F2 Low - makefile boundary: ACCEPTED (by design).** `make ${ZZ_EVAL}` empty-expansion stays out of Deny; `$1`-style tails Ask. No code change.
- **F3 Low - benign `tar $VAR` Ask: ACCEPTED (by design).** Fail-closed tail rule intentionally over-Asks; pinned by tests. No code change.
- **F4 Info - `make -f` Deny policy: PRESERVED.** Carrier + substitution + `$IFS` make legs Deny under allow; verified by `TestCandidateFormsAgreement` + full suite. No change.
- **F5 High (reviewer `ses_df585914`) - shell command-opening prefix words hid carrier/`-c` reachability: CLOSED (2026-11-24).** `command`, `exec`, `builtin`, `!`, `if`, `then`, `else`, `elif`, `while`, `until`, `do`, `{`, `}`, `(`, `)` and attached openers (`(sudo`) are now transparent inside the shared prefix walk, so `command sudo -s 'rm -rf /'`, `command bash -c 'rm -rf /'`, `( sudo -s 'rm -rf /' )`, `(sudo bash -c 'rm -rf /')`, and `find . -exec command sudo -s 'rm -rf /' \;` all Deny under allow instead of degrading to Ask; `echo command sudo -s 'rm -rf /'` stays green. Pinned by `TestGateShellPrefixWordsTransparent`; `TestShellPrefixWalkAgreement` pins the single shared walk.

## Follow-up F1 (High) - CLOSED 2026-11-24

**Title:** Gate: packed `sudo -s` / `su -c` payload with tar/make param-expansion tail Allows instead of Ask

**Status:** Closed. `carrierPayloadAskEscalation` runs the shared Ask legs over re-lexed carrier payloads (and nested carriers within the depth budget), so the repro below now Asks. The same circuit is what routes `command sudo -s ...` Deny content after the F5 prefix fix.

**Original repro (under `round2AllowSB`, system_exec=allow) - now all Ask:**
- `EvaluateCommand(allow, "sudo -s 'tar $1'", nil)` -> Ask (`tar argument with parameter expansion requires user approval`)
- `EvaluateCommand(allow, "su -c 'tar $1'", nil)` -> Ask
- `EvaluateCommand(allow, "sudo -s 'make $1'", nil)` -> Ask
- `EvaluateCommand(allow, "su -c 'make $1'", nil)` -> Ask
- Control (still correct): `tar $1`, `make $1`, `gtar $1`, `tar -xf a.tar $1`, `sudo -s tar $1` (split) -> Ask; `sudo -s 'make -f evil.mk'`, `su -c 'make -f evil.mk'` -> Deny.

**Cause (as originally diagnosed):** `paramExpansionAskReason` is evaluated on top-level `shellSegmentArgvs` segments only; packed carrier payloads were re-entered via `checkSudoShellPayload` / su `-c` legs and `carrierInterpreterEscalation`, which ran the hard-deny circuit + interpreter escalation but never the Ask leg.

**Resolution:** `carrierPayloadAskEscalation` now runs the shared `shellAskLegs` over the re-lexed carrier payload command line (and follows nested carriers within `maxCarrierPayloadDepth`), so the Ask-only tail has a payload counterpart. `echo sudo -s 'tar $1'` stays green (unreachable carrier) and `make ${ZZ_EVAL}` empty-expansion semantics are unchanged.

**Test data / env:** linux, `round2AllowSB()` (`SandboxModePaths`, `system_exec: allow`), unset vars (`os.Unsetenv`), positional/special params (`$1`, `$@`, `$!`). Full `internal/agent` + `internal/sandbox` suites green at validation; `go vet`, `gofmt`, `GOOS=windows go build ./...` and `GOOS=darwin go build ./...` clean.

## Follow-up F5 (High/Medium) - CLOSED 2026-11-24 (54th loop)

Reviewer `ses_df544c83` found that the F5 prefix fix handled a standalone opener (`( sudo -s ...` denies) but not an attached one: `(sudo -s 'rm -rf /')`, `{sudo -s ... ; }`, `(bash -c ...)`, `(su -c ...)`, `(chmod -R 777 /)` degraded to Ask ("unknown command") because the token matchers compared `resolveBinaryBase(tok)` on `(sudo`/`{sudo`/`(chmod` and the grouped closer stayed attached to the target (`/)`). `eval` was also missing from the shared command-position walk and was not re-lexed, so `eval sudo -s 'rm -rf /'` Asked instead of Denying.

**Resolution:** (1) hard-deny segments are normalized once with `normalizeHardDenySegment` (the shared `sandbox.StripCdGroupOpeners`) in `checkArgvHardDenyScan` and both per-view scanner loops, which resolves the attached opener and trims the grouped closer; (2) `checkShellCPayload`, `checkSudoShellPayloadDepth`, `hasSudoShellCarrier`, `checkChmodTokenScan`, `codeToolTails`, `codeToolAskReason`, `interpreterTails`, and `shellCarrierPayloadsAt` compare `resolveBinaryBase(shellPrefixClassToken(tok))`, so a grouped token classifies like its bare form even when passed directly; (3) `eval` is in `shellCommandPrefixWords` and owns a re-lexed quoted payload (`checkEvalPayloadDepth`, shared `maxCarrierPayloadDepth`), with `shellCarrierPayloadsAt` supplying the Ask-side payload. `echo` greens are unchanged.

**Pinned by:** `TestGateShellPrefixWordsTransparent` (new deny/ask/green rows above) and `TestSudoShellPayloadChainLinear` now asserts the hostile payload verdict instead of discarding it. `coproc`/`case` are the same pre-existing prefix-opacity class and remain tracked as follow-up (not regressed here).

## Test data, environment, prerequisites

- Go toolchain on linux; `round2AllowSB` (paths mode, `system_exec: allow`) for fail-closed floor pins; `os.Unsetenv("HK_GATE_T_UNSET")` / `ZZ_EVAL` for unresolvable-expansion cases.
- Temp probe test created and removed (`internal/agent/zz_probe_temp_test.go`) for the 54th-loop repro; no prod-source edits beyond the fixes above; no commits/pushes/ruleset touches.
- Windows- and darwin-only runtime branches are untested on this linux host by construction; both package cross-compiles (`go build` does not compile `_test.go` files) are verified via `GOOS=windows go build ./...` and `GOOS=darwin go build ./...`, while the linux-only `systemexec_audit_unix_test.go` runs under the linux `go test` above.
