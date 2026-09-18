// Package sleep implements hakase's SkillOpt-Sleep offline self-improvement
// loop (plan docs/skillopt-sleep/plan.md): harvest session transcripts,
// mine recurring tasks, replay them against skill candidates, consolidate
// bounded edits behind a held-out validation gate, and stage proposals for
// explicit human adopt. No live self-modification: nothing here mutates a
// live skill without the adopt step verifying hashes.
//
// Phase 0 ships only the redaction primitive (SL-004). Harvest/mine/replay
// land in Phases 1-2.
package sleep

import (
	"errors"
	"regexp"

	"amurru/hakase/internal/util"
)

// RedactSecrets forwards to the canonical engine in internal/util (shared
// with the skill-package mutator prompts so both layers redact identically
// with no import cycle). See util.RedactSecrets.
func RedactSecrets(s string) (string, bool) {
	return util.RedactSecrets(s)
}

// untrustedTag strips pre-existing <UNTRUSTED_DATA> markers (any case, any
// attributes) so attacker-planted tags cannot disable wrapping via the
// double-wrap early-return hole.
var untrustedTag = regexp.MustCompile(`(?i)</?UNTRUSTED_DATA[^>]*>`)

// WrapSleepExcerpt frames a harvested excerpt as untrusted data for miner /
// replay / judge / reflection prompts. Unlike the global guard it has no
// length floor (short strings like "ignore rules" are wrapped too) and it
// strips pre-existing markers before wrapping instead of early-returning.
func WrapSleepExcerpt(s string) string {
	stripped := untrustedTag.ReplaceAllString(s, "")
	return "\n<UNTRUSTED_DATA>\n" + stripped + "\n</UNTRUSTED_DATA>\n"
}

// ResolveRedaction is the normative SL-004 truth table for proceeding
// without redaction. redactSecrets is the configured value
// (sleep.redact_secrets, default true); allowFlag is the explicit
// --allow-unredacted CLI flag (never env, never file-alone).
//
//   - (true, false)  -> redacted, no warning, no error.
//   - (true, true)   -> redacted with warning (flag alone does nothing).
//   - (false, true)  -> unredacted, no warning, no error (explicit opt-out).
//   - (false, false) -> hard error: refuse harvest/mine/replay/judge/stage.
func ResolveRedaction(redactSecrets, allowFlag bool) (unredacted bool, warning string, err error) {
	switch {
	case redactSecrets && !allowFlag:
		return false, "", nil
	case redactSecrets && allowFlag:
		return false, "--allow-unredacted ignored: redaction is enabled, continuing redacted", nil
	case !redactSecrets && allowFlag:
		return true, "", nil
	default:
		return false, "", errors.New("sleep: redaction disabled without --allow-unredacted: refusing to run (set sleep.redact_secrets=true or pass --allow-unredacted explicitly)")
	}
}
