// slowupdate.go - epoch-boundary slow update and optimizer memory (plan
// Phase 3, SL-031): per-skill score history from the sleep state classifies
// the longitudinal trend (improved | regressed | persistent_fail |
// stable_success), which renders as a protected SLOW_UPDATE tail block on
// tonight's baseline and as the meta-skill sidecar
// (references/optimizer-memory.md) prepended to reflect prompts.
//
// The sidecar is optimizer-side memory and is NEVER part of the skill
// document: it lives next to the live SKILL.md, which markdown-skill
// discovery does not read, and only the reflector prompt ever sees it.
package sleep

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"amurru/hakase/internal/util"
)

// scoreFullThreshold marks "the skill passes everything" for trend rules.
const scoreFullThreshold = 0.999

// ClassifyEpochTrend classifies a skill's per-night history (oldest first,
// as recorded in SleepState.Nights). Scores are the gate metric outcome per
// night: the candidate score when accepted, else the baseline (what the
// incumbent actually held). Rules:
//
//   - fewer than 2 consolidated nights: "" (no trend yet)
//   - every night 0: persistent_fail (the guidance never helped)
//   - every night full: stable_success (protect what works)
//   - last > previous: improved; last < previous: regressed
//   - otherwise "" (flat: no guidance, no churn)
func ClassifyEpochTrend(records []NightGroupRecord) string {
	var scores []float64
	for _, r := range records {
		if !r.Consolidated {
			continue
		}
		s := r.CandidateScore
		if !r.Accepted {
			s = r.BaselineScore
		}
		scores = append(scores, s)
	}
	if len(scores) < 2 {
		return ""
	}
	allZero, allFull := true, true
	for _, s := range scores {
		if s != 0 {
			allZero = false
		}
		if s < scoreFullThreshold {
			allFull = false
		}
	}
	last, prev := scores[len(scores)-1], scores[len(scores)-2]
	switch {
	case allZero:
		return "persistent_fail"
	case allFull:
		return "stable_success"
	case last > prev:
		return "improved"
	case last < prev:
		return "regressed"
	default:
		return ""
	}
}

// TrendGuidance renders the slow-update bullets for a trend. Unknown or
// empty trends yield no bullets (no block is written).
func TrendGuidance(trend string) []string {
	switch trend {
	case "improved":
		return []string{
			"Trend over recent nights: improving. Keep the direction of the latest learned bullets; avoid churn in sections that already work.",
		}
	case "regressed":
		return []string{
			"Trend over recent nights: the last epoch regressed. Re-examine the most recent learned bullets first; prefer reverting or sharpening them over adding new ones.",
		}
	case "persistent_fail":
		return []string{
			"Trend over recent nights: tasks keep failing despite accumulated bullets. The current procedures are not working; propose structurally different guidance instead of more of the same.",
		}
	case "stable_success":
		return []string{
			"Trend over recent nights: consistently passing. Protect what works; make only minimal, well-justified edits.",
		}
	default:
		return nil
	}
}

// optimizerMemoryDirName is the meta-skill sidecar location relative to the
// live SKILL.md, mirroring SkillOpt's references/ layout.
const optimizerMemoryDirName = "references"

// OptimizerMemoryPath returns the sidecar path for a live skill file.
func OptimizerMemoryPath(livePath string) string {
	return filepath.Join(filepath.Dir(livePath), optimizerMemoryDirName, "optimizer-memory.md")
}

// OptimizerMemoryHead caps the sidecar excerpt prepended to prompts.
const OptimizerMemoryHead = 2000

// ReadOptimizerMemory returns the sidecar text for a live skill ("" when
// absent or unreadable - memory is advisory, never load-bearing) redacted
// and head-truncated for prompt hygiene.
func ReadOptimizerMemory(livePath string) string {
	data, err := os.ReadFile(OptimizerMemoryPath(livePath))
	if err != nil || len(data) == 0 {
		return ""
	}
	head := string(data)
	if len(head) > OptimizerMemoryHead {
		head = head[:OptimizerMemoryHead]
	}
	redacted, _ := util.RedactSecrets(head)
	return redacted
}

// WriteOptimizerMemory persists the sidecar: the trend guidance plus the
// per-night history table. 0700 dir / 0600 file, atomic (SL-005).
func WriteOptimizerMemory(livePath string, records []NightGroupRecord, trend string) error {
	path := OptimizerMemoryPath(livePath)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	blob := []byte(RenderOptimizerMemory(records, trend))
	return writeFileAtomic(path, blob, 0o600)
}

// RenderOptimizerMemory renders the sidecar markdown (also used in the
// staged report so reviewers see what the optimizer remembers).
func RenderOptimizerMemory(records []NightGroupRecord, trend string) string {
	var b strings.Builder
	b.WriteString("# Optimizer memory\n\n")
	b.WriteString("_Meta-skill sidecar: optimizer-side memory for the sleep cycle. Never deployed with the skill; markdown discovery ignores this directory._\n\n")
	if guidance := TrendGuidance(trend); len(guidance) > 0 {
		b.WriteString("## Current trend: " + trend + "\n\n")
		for _, g := range guidance {
			b.WriteString("- " + g + "\n")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("## Current trend: (none yet)\n\n")
	}
	b.WriteString("## Per-night outcomes\n\n")
	b.WriteString("| Night (UTC) | Baseline | Candidate | Accepted |\n|---|---:|---:|---|\n")
	for _, r := range records {
		accepted := "no"
		if r.Accepted {
			accepted = "yes"
		}
		b.WriteString(fmt.Sprintf("| %s | %.3f | %.3f | %s |\n",
			r.StartedAt.Format("2006-01-02 15:04"), r.BaselineScore, r.CandidateScore, accepted))
	}
	return b.String()
}

// skillHistory extracts one skill's per-night records from the state
// (oldest first; Nights are appended chronologically).
func skillHistory(state SleepState, skillName string) []NightGroupRecord {
	var out []NightGroupRecord
	for _, n := range state.Nights {
		for _, g := range n.Groups {
			if strings.EqualFold(g.SkillName, skillName) {
				out = append(out, g)
			}
		}
	}
	return out
}
