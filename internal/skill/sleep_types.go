// sleep_types.go - markdown-skill evolution types for the SkillOpt-Sleep
// port (plan Phase 1, SL-010): the trainable task unit, the bounded edit
// algebra applied to a protected learned block, and the strict validation
// gate. Code-skill evolution stays in evolver.go; everything here targets
// SKILL.md documents.
package skill

import (
	"regexp"
	"strings"
)

// Learned-block and protected-region markers. Step-level edits only ever
// touch the learned block; hand-written content (including any slow-update
// or appendix regions, which arrive in Phase 3) is never rewritten.
const (
	LearnedStart = "<!-- SKILLOPT-SLEEP:LEARNED START -->"
	LearnedEnd   = "<!-- SKILLOPT-SLEEP:LEARNED END -->"

	SlowUpdateStart = "<!-- SLOW_UPDATE_START -->"
	SlowUpdateEnd   = "<!-- SLOW_UPDATE_END -->"
	AppendixStart   = "<!-- APPENDIX_START -->"
	AppendixEnd     = "<!-- APPENDIX_END -->"
)

// learnedBanner marks machine-maintained content for human reviewers.
const learnedBanner = "_This block is maintained by hakase sleep. Edits here are proposed offline, validated against held-out tasks, and adopted only after review. Hand-edits outside this block are never touched._"

// JudgeCheck is one checkable assertion on a replayed response.
type JudgeCheck struct {
	Op      string `json:"op"` // contains | section_contains | tool_called
	Text    string `json:"text,omitempty"`
	Section string `json:"section,omitempty"`
	Tool    string `json:"tool,omitempty"`
}

// TaskJudge is the rule-judge attached to a mined task.
type TaskJudge struct {
	Checks []JudgeCheck `json:"checks,omitempty"`
}

// MarkdownTask is the training unit for markdown-skill evolution: a
// self-contained task mined from real sessions (or hand-written for the
// pilot), with its scoring contract and split assignment.
type MarkdownTask struct {
	ID             string    `json:"id"`
	Intent         string    `json:"intent"`
	ContextExcerpt string    `json:"context_excerpt,omitempty"`
	ReferenceKind  string    `json:"reference_kind,omitempty"` // exact | rule | rubric | none
	Reference      string    `json:"reference,omitempty"`
	Judge          TaskJudge `json:"judge,omitempty"`
	Tags           []string  `json:"tags,omitempty"`
	Split          string    `json:"split,omitempty"`  // train | val | test (legacy: replay | holdout)
	Origin         string    `json:"origin,omitempty"` // real | dream (default real)
	SkillHint      string    `json:"skill_hint,omitempty"`
}

// MarkdownTaskFile is the on-disk shape of a --tasks file.
type MarkdownTaskFile struct {
	Tasks []MarkdownTask `json:"tasks"`
}

// TextEdit is one bounded edit proposed against a skill document.
type TextEdit struct {
	Target    string `json:"target,omitempty"` // "skill" (default) - other targets are unmatched in Phase 1
	Op        string `json:"op"`               // add | delete | replace | insert_after
	Content   string `json:"content,omitempty"`
	Anchor    string `json:"anchor,omitempty"`
	Rationale string `json:"rationale,omitempty"`
	// Route classifies the edit under skill-aware reflection (plan Phase 3,
	// SL-032): "skill_defect" (default) edits the gated learned block;
	// "execution_lapse" reminders land in the protected appendix, bypassing
	// the gate by design. Only honored when the consumer opted in.
	Route string `json:"route,omitempty"`
}

// Edit route classes (SL-032).
const (
	RouteSkillDefect     = "skill_defect"
	RouteExecutionLapse  = "execution_lapse"
)

// ScoreDelta is one held-out task's baseline-vs-candidate comparison.
type ScoreDelta struct {
	TaskID         string   `json:"task_id"`
	Tags           []string `json:"tags,omitempty"`
	BaselineScore  float64  `json:"baseline_score"`
	CandidateScore float64  `json:"candidate_score"`
}

// Status classifies the movement: improved | regressed | unchanged.
func (d ScoreDelta) Status() string {
	switch {
	case d.CandidateScore > d.BaselineScore:
		return "improved"
	case d.CandidateScore < d.BaselineScore:
		return "regressed"
	default:
		return "unchanged"
	}
}

// GateResult is the validation-gate verdict for one candidate.
type GateResult struct {
	Action         string  // accept_new_best | accept | reject | reject_unverified | greedy_applied | greedy_noop
	Accepted       bool    // whether the candidate documents ship
	BaselineScore  float64 // gate-metric score of the incumbent on val
	CandidateScore float64 // gate-metric score of the candidate on val
	Reason         string  // human-readable verdict for the report
}

// GateOpts tunes one gate decision.
type GateOpts struct {
	// HoldoutLeaked marks a non-disjoint val slice: the comparison cannot
	// detect overfitting, so the gate abstains (reject_unverified).
	HoldoutLeaked bool
	// Greedy opts out of validation (explicit --greedy only): edits ship
	// without a val-improvement requirement, but scores still record.
	Greedy bool
	// Applied reports whether any edit survived application. Greedy mode
	// with nothing applied is a noop, never an accept.
	Applied bool
	// Regressed names val tasks that got worse (GateNoRegression): any
	// entry blocks acceptance even when the mean improves.
	Regressed []string
}

// SelectGateScore projects a (hard, soft) pair onto one comparison metric.
// Unknown metrics fail closed to hard.
func SelectGateScore(hard, soft float64, metric string, mixedWeight float64) float64 {
	switch strings.ToLower(strings.TrimSpace(metric)) {
	case "soft":
		return soft
	case "mixed":
		w := mixedWeight
		if w < 0 {
			w = 0
		}
		if w > 1 {
			w = 1
		}
		return (1-w)*hard + w*soft
	default:
		return hard
	}
}

// EvaluateGate renders the strict gate decision: a candidate ships only on
// a strict improvement of the gate metric, with zero tolerance for leaked
// validation or (optionally) per-task regressions.
func EvaluateGate(candidateScore, currentScore, bestScore float64, opts GateOpts) GateResult {
	if opts.HoldoutLeaked {
		return GateResult{Action: "reject_unverified", BaselineScore: currentScore,
			CandidateScore: candidateScore,
			Reason:         "validation slice not disjoint from training; comparison cannot detect overfitting"}
	}
	if opts.Greedy {
		if opts.Applied {
			return GateResult{Action: "greedy_applied", Accepted: true,
				BaselineScore: currentScore, CandidateScore: candidateScore,
				Reason: "greedy mode: edits accepted without validation"}
		}
		return GateResult{Action: "greedy_noop", BaselineScore: currentScore,
			CandidateScore: candidateScore, Reason: "greedy mode: no edits applied"}
	}
	if len(opts.Regressed) > 0 {
		return GateResult{Action: "reject", BaselineScore: currentScore,
			CandidateScore: candidateScore,
			Reason:         "no-regression gate blocked by: " + strings.Join(opts.Regressed, ", ")}
	}
	if candidateScore > currentScore {
		if candidateScore > bestScore {
			return GateResult{Action: "accept_new_best", Accepted: true,
				BaselineScore: currentScore, CandidateScore: candidateScore,
				Reason: "strict improvement over best"}
		}
		return GateResult{Action: "accept", Accepted: true,
			BaselineScore: currentScore, CandidateScore: candidateScore,
			Reason: "strict improvement over current"}
	}
	return GateResult{Action: "reject", BaselineScore: currentScore,
		CandidateScore: candidateScore, Reason: "no strict improvement"}
}

// untrustedTagRe strips marker-like text from edit content so the optimizer
// cannot duplicate or forge protected regions.
var markerStripRe = regexp.MustCompile(`(?i)<!--\s*(SKILLOPT-SLEEP:LEARNED|SLOW_UPDATE|APPENDIX)[^>]*-->`)

// normEditLine canonicalizes a learned-block line for dedup/anchor matching.
func normEditLine(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// extractLearned returns the raw inner text of the learned block, or "".
func extractLearned(doc string) string {
	s := strings.Index(doc, LearnedStart)
	e := strings.Index(doc, LearnedEnd)
	if s == -1 || e == -1 || e < s {
		return ""
	}
	return strings.TrimSpace(doc[s+len(LearnedStart) : e])
}

// currentLearnedLines returns the learned block's bullet lines.
func currentLearnedLines(doc string) []string {
	inner := extractLearned(doc)
	if inner == "" {
		return nil
	}
	var lines []string
	for _, ln := range strings.Split(inner, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "- ") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(ln, "- ")))
		}
	}
	return lines
}

// stripLearned removes every learned block (and dangling content) from doc.
func stripLearned(doc string) string {
	for {
		s := strings.Index(doc, LearnedStart)
		if s == -1 {
			break
		}
		e := strings.Index(doc[s:], LearnedEnd)
		if e == -1 {
			doc = doc[:s]
			break
		}
		doc = doc[:s] + doc[s+e+len(LearnedEnd):]
	}
	for strings.Contains(doc, "\n\n\n") {
		doc = strings.ReplaceAll(doc, "\n\n\n", "\n\n")
	}
	return strings.TrimRight(doc, "\n")
}

// setLearned replaces the learned block with the given bullet lines.
func setLearned(doc string, learned []string) string {
	base := stripLearned(doc)
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n" + LearnedStart + "\n")
	b.WriteString("## Learned preferences & procedures\n\n" + learnedBanner + "\n")
	for _, ln := range learned {
		ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "- "))
		if ln != "" {
			b.WriteString("\n- " + ln)
		}
	}
	b.WriteString("\n" + LearnedEnd + "\n")
	return b.String()
}

// cleanEditContent strips protected-region markers from edit content and
// normalizes it to a single-line learned bullet: internal newlines collapse
// to spaces so continuation lines can never be written into the block and
// then silently dropped by the next apply (learned bullets are one line by
// construction - CodeRabbit).
func cleanEditContent(s string) string {
	return strings.Join(strings.Fields(markerStripRe.ReplaceAllString(s, "")), " ")
}

// CleanEditContent is the exported form of cleanEditContent for consumers
// composing protected blocks (appendix reminders, slow-update guidance)
// outside ApplyEdits: markers must never survive into protected regions.
func CleanEditContent(s string) string {
	return cleanEditContent(s)
}

// applyEdits applies bounded edits to the learned block only. Hand-written
// content outside the block is never touched: a document without a block
// gains one at the tail, and anchors only match learned lines. It returns
// the new document plus applied/unmatched partitions. When nothing applies,
// the original document returns unchanged (no empty block is created).
func ApplyEdits(doc string, edits []TextEdit) (newDoc string, applied, unmatched []TextEdit) {
	lines := currentLearnedLines(doc)
	normSet := make(map[string]bool, len(lines))
	for _, ln := range lines {
		normSet[normEditLine(ln)] = true
	}

	for _, e := range edits {
		// Phase 1 consolidates skills only: edits naming any other
		// target are unmatched, never silently applied to this doc.
		if t := strings.ToLower(strings.TrimSpace(e.Target)); t != "" && t != "skill" {
			unmatched = append(unmatched, e)
			continue
		}
		content := cleanEditContent(e.Content)
		anchor := normEditLine(e.Anchor)
		switch strings.ToLower(strings.TrimSpace(e.Op)) {
		case "add":
			if content == "" || normSet[normEditLine(content)] {
				unmatched = append(unmatched, e)
				continue
			}
			lines = append(lines, content)
			normSet[normEditLine(content)] = true
			applied = append(applied, e)
		case "delete":
			if anchor == "" {
				unmatched = append(unmatched, e)
				continue
			}
			kept := lines[:0]
			changed := false
			for _, ln := range lines {
				if strings.Contains(normEditLine(ln), anchor) {
					changed = true
					continue
				}
				kept = append(kept, ln)
			}
			if !changed {
				unmatched = append(unmatched, e)
				continue
			}
			lines = kept
			normSet = make(map[string]bool, len(lines))
			for _, ln := range lines {
				normSet[normEditLine(ln)] = true
			}
			applied = append(applied, e)
		case "replace":
			if anchor == "" || content == "" {
				unmatched = append(unmatched, e)
				continue
			}
			changed := false
			for i, ln := range lines {
				if strings.Contains(normEditLine(ln), anchor) && ln != content {
					lines[i] = content
					changed = true
				}
			}
			if !changed {
				unmatched = append(unmatched, e)
				continue
			}
			normSet = make(map[string]bool, len(lines))
			for _, ln := range lines {
				normSet[normEditLine(ln)] = true
			}
			applied = append(applied, e)
		case "insert_after":
			if anchor == "" || content == "" {
				unmatched = append(unmatched, e)
				continue
			}
			at := -1
			for i, ln := range lines {
				if strings.Contains(normEditLine(ln), anchor) {
					at = i
					break
				}
			}
			if at == -1 {
				unmatched = append(unmatched, e)
				continue
			}
			lines = append(lines[:at+1], append([]string{content}, lines[at+1:]...)...)
			normSet[normEditLine(content)] = true
			applied = append(applied, e)
		default:
			unmatched = append(unmatched, e)
		}
	}

	if len(applied) == 0 {
		return doc, nil, unmatched
	}
	return setLearned(doc, lines), applied, unmatched
}

// IsSleepManagedDoc reports whether a SKILL.md carries a sleep-maintained
// learned block. Only managed docs are auto-adoptable (plan M5): hand-written
// skills always stage for explicit human adopt.
func IsSleepManagedDoc(doc string) bool {
	return strings.Contains(doc, LearnedStart)
}

// slowUpdateLines returns the bullet lines of the doc's slow-update tail
// block (longitudinal guidance written at epoch boundaries, plan SL-031).
func slowUpdateLines(doc string) []string {
	s := strings.Index(doc, SlowUpdateStart)
	e := strings.Index(doc, SlowUpdateEnd)
	if s == -1 || e == -1 || e < s {
		return nil
	}
	var lines []string
	for _, ln := range strings.Split(doc[s+len(SlowUpdateStart):e], "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "- ") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(ln, "- ")))
		}
	}
	return lines
}

// HasSlowUpdateBlock reports whether the doc carries a slow-update block.
func HasSlowUpdateBlock(doc string) bool {
	s := strings.Index(doc, SlowUpdateStart)
	e := strings.Index(doc, SlowUpdateEnd)
	return s != -1 && e != -1 && e > s
}

// stripSlowUpdate removes every slow-update block (dangling starts cut to
// end of document) so a fresh block replaces rather than accumulates.
func stripSlowUpdate(doc string) string {
	for {
		s := strings.Index(doc, SlowUpdateStart)
		if s == -1 {
			break
		}
		e := strings.Index(doc[s:], SlowUpdateEnd)
		if e == -1 {
			doc = doc[:s]
			break
		}
		doc = doc[:s] + doc[s+e+len(SlowUpdateEnd):]
	}
	for strings.Contains(doc, "\n\n\n") {
		doc = strings.ReplaceAll(doc, "\n\n\n", "\n\n")
	}
	return strings.TrimRight(doc, "\n")
}

// SetSlowUpdate replaces any existing slow-update block with the given
// guidance bullets, appended at the document tail. The block is protected:
// step edits never reach it (ApplyEdits only touches the learned block and
// strips markers from edit content), so this is the only writer.
func SetSlowUpdate(doc string, lines []string) string {
	if len(lines) == 0 {
		return stripSlowUpdate(doc)
	}
	base := stripSlowUpdate(doc)
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n" + SlowUpdateStart + "\n")
	b.WriteString("## Optimizer guidance (slow update)\n\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "- "))
		if ln != "" {
			b.WriteString("- " + ln + "\n")
		}
	}
	b.WriteString(SlowUpdateEnd + "\n")
	return b.String()
}

// stripAppendix removes every appendix block (dangling starts cut to end of
// document) so a fresh block replaces rather than accumulates.
func stripAppendix(doc string) string {
	for {
		s := strings.Index(doc, AppendixStart)
		if s == -1 {
			break
		}
		e := strings.Index(doc[s:], AppendixEnd)
		if e == -1 {
			doc = doc[:s]
			break
		}
		doc = doc[:s] + doc[s+e+len(AppendixEnd):]
	}
	for strings.Contains(doc, "\n\n\n") {
		doc = strings.ReplaceAll(doc, "\n\n\n", "\n\n")
	}
	return strings.TrimRight(doc, "\n")
}

// SetAppendix replaces any existing protected appendix block with the given
// reminder bullets, appended at the document tail. Lapse reminders land
// here ungated (plan SL-032); the block is otherwise never written.
func SetAppendix(doc string, lines []string) string {
	if len(lines) == 0 {
		return stripAppendix(doc)
	}
	base := stripAppendix(doc)
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n" + AppendixStart + "\n")
	b.WriteString("## Execution reminders\n\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "- "))
		if ln != "" {
			b.WriteString("- " + ln + "\n")
		}
	}
	b.WriteString(AppendixEnd + "\n")
	return b.String()
}

// NormalizeEditRoute canonicalizes an edit's route class: known values are
// lowercased, everything else falls back to skill_defect (fail-closed to
// the gated path - an unrecognized route must never bypass the gate).
func NormalizeEditRoute(route string) string {
	switch strings.ToLower(strings.TrimSpace(route)) {
	case RouteExecutionLapse:
		return RouteExecutionLapse
	default:
		return RouteSkillDefect
	}
}

// SplitSkillDoc splits a raw SKILL.md document into its frontmatter block
// (including both --- delimiters) and the remainder, mirroring
// ParseMarkdownSkill's delimiter scan without validation. Used for the
// frontmatter-freeze check: mutations must never alter front.
func SplitSkillDoc(doc string) (front, rest string, ok bool) {
	// Normalize like the parser so in-memory docs compare cleanly.
	normalized := strings.ReplaceAll(doc, "\r\n", "\n")
	normalized = strings.TrimPrefix(normalized, "\xEF\xBB\xBF")
	if !strings.HasPrefix(normalized, "---\n") {
		return "", "", false
	}
	lines := strings.Split(normalized, "\n")
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return "", "", false
	}
	return strings.Join(lines[:closeIdx+1], "\n"), strings.Join(lines[closeIdx+1:], "\n"), true
}
