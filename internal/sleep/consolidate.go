// consolidate.go - one SkillOpt consolidation epoch over a markdown skill
// (plan Phase 1, SL-013): reflect on train failures into bounded edits,
// gate the candidate on a disjoint validation slice, and re-verify with a
// fresh final replay before shipping.
//
// Skill and only skill evolves here; memory/knowledge consolidation arrives
// with the Phase 2 sleep cycle.
package sleep

import (
	"context"
	"strings"

	"amurru/hakase/internal/skill"
)

// Reflector proposes bounded edits from scored train outcomes. Failures
// carry the signal; successes keep the optimizer honest. Raw is the
// optimizer's reply for diagnostics (empty when nothing was proposed).
type Reflector func(ctx context.Context, failures, successes []ScoredTask, skillBody string, budget int) (edits []skill.TextEdit, raw string, err error)

// ConsolidateOpts tunes one consolidation epoch.
type ConsolidateOpts struct {
	// EditBudget caps applied edits per epoch (learning rate). Zero means DefaultEditBudget.
	EditBudget int
	// GateMetric projects (hard, soft) to one score: hard | soft | mixed.
	GateMetric string
	// MixedWeight is the soft weight for mixed. Zero means DefaultMixedWeight.
	MixedWeight float64
	// GateNoRegression blocks acceptance when any val task regresses, even
	// when the mean improves.
	GateNoRegression bool
	// Greedy opts out of validation (explicit --greedy only).
	Greedy bool
	// MaxFailuresInPrompt caps train failures shown to the reflector.
	// Zero means DefaultMaxFailuresInPrompt.
	MaxFailuresInPrompt int
	// Reflect proposes edits. Nil means evaluation-only (score + report).
	Reflect Reflector
	// Ranker selects the top-budget edits when the reflector over-proposes
	// past the learning rate (plan Phase 3, SL-030). Nil - or a ranker
	// error - falls back to documented truncation.
	Ranker Ranker
	// SkillAware enables skill-aware reflection routing (plan Phase 3,
	// SL-032): edits the reflector tags route=execution_lapse land in the
	// protected appendix as ungated reminders (capped, logged); everything
	// else keeps the gated learned-block flow. Default off.
	SkillAware bool
}

// Defaults for ConsolidateOpts zero values.
const (
	DefaultEditBudget          = 4
	DefaultMixedWeight         = 0.5
	DefaultMaxFailuresInPrompt = 5
)

// Skill-aware reflection (plan Phase 3, SL-032) knobs: at most this many
// lapse reminders land in the protected appendix per night (ungated content
// stays bounded), and sub-threshold wins are annotated as noise (SL-034).
const (
	DefaultLapseCap     = 3
	NoiseDeltaThreshold = 0.015 // 1.5 points on the 0..1 gate scale
	MinValTasksForClaim = 20
)

// consolidationRan reports whether a consolidation result reflects real
// replay work (a live backend and actual train/val tasks). Dead-backend and
// task-less noops must not feed the slow-update trend history.
func consolidationRan(result ConsolidationResult) bool {
	if result.CallError != "" {
		return false
	}
	if result.GateAction == "noop" && result.NoEditsReason == "no train/val tasks (test-only or empty input)" {
		return false
	}
	return true
}

// HoldoutTaskDetail is per-val-task evidence so a flat night self-diagnoses:
// empty responses mean the backend failed, non-empty-but-failing means the
// judge is strict or the edit did not help.
type HoldoutTaskDetail struct {
	ID           string  `json:"id"`
	Hard         float64 `json:"hard"`
	Soft         float64 `json:"soft"`
	ResponseLen  int     `json:"response_len"`
	ResponseHead string  `json:"response_head"`
	Why          string  `json:"why"`
}

// ConsolidationResult is everything one epoch produced.
type ConsolidationResult struct {
	Accepted       bool                `json:"accepted"`
	GateAction     string              `json:"gate_action"`
	BaselineScore  float64             `json:"baseline_score"`
	CandidateScore float64             `json:"candidate_score"`
	NewSkill       string              `json:"new_skill,omitempty"`
	Applied        []skill.TextEdit    `json:"applied,omitempty"`
	Rejected       []skill.TextEdit    `json:"rejected,omitempty"`
	Unmatched      []skill.TextEdit    `json:"unmatched,omitempty"`
	ClippedEdits   int                 `json:"clipped_edits,omitempty"`
	Deltas         []skill.ScoreDelta  `json:"deltas,omitempty"`
	HoldoutDetail  []HoldoutTaskDetail `json:"holdout_detail,omitempty"`
	// RankingDetails records the rank_and_select outcome when the edit
	// pool exceeded the learning rate (plan SL-030). Nil when the pool fit.
	RankingDetails []RankingDetail `json:"ranking_details,omitempty"`
	// Skill-aware reflection (SL-032): lapse reminders that bypassed the
	// gate by design, appended to the protected appendix of the shipped
	// proposal (accepted nights only; dropped with rejected candidates).
	LapseReminders []skill.TextEdit `json:"lapse_reminders,omitempty"`
	LapseBypassed  bool             `json:"lapse_bypassed,omitempty"`
	// NoiseRange marks an accepted result whose win is inside single-seed
	// noise (delta < 1.5pt or val n < 20): a claim, not a proof (SL-034).
	NoiseRange bool `json:"noise_range,omitempty"`
	// ReplayDenials records deny-by-default events from agentic replay
	// (plan SL-040): off-allowlist tool-call attempts and sandbox path
	// refusals. Nil for single-shot replay (no tools involved).
	ReplayDenials *DeniedToolAttempts `json:"replay_denials,omitempty"`
	HoldoutLeaked bool                `json:"holdout_leaked"`
	ReflectRaw    string              `json:"reflect_raw,omitempty"`
	CallError     string              `json:"call_error,omitempty"`
	NoEditsReason string              `json:"no_edits_reason,omitempty"`
}

// normalizeSplit maps legacy split names to the canonical three.
func normalizeSplit(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "replay":
		return "train"
	case "holdout":
		return "val"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// SplitTasks partitions tasks into train (reflect) and val (gate) slices.
// Test tasks never enter either. Dream-origin tasks never enter val (they
// would validate on synthetic data). Empty val falls back to train (or any
// non-test task) and flags holdout_leaked: the comparison cannot detect
// overfitting, so the gate must abstain. Empty train falls back to val with
// the same flag. Overlapping IDs across a non-fallback split also leak.
func SplitTasks(tasks []skill.MarkdownTask) (train, val []skill.MarkdownTask, leaked bool) {
	for _, t := range tasks {
		split := normalizeSplit(t.Split)
		origin := strings.ToLower(strings.TrimSpace(t.Origin))
		if split == "test" {
			continue
		}
		if origin == "dream" {
			train = append(train, t)
			continue
		}
		switch split {
		case "train":
			train = append(train, t)
		case "val":
			val = append(val, t)
		case "":
			train = append(train, t)
		}
	}
	if len(val) == 0 {
		val = append([]skill.MarkdownTask(nil), train...)
		if len(val) == 0 {
			for _, t := range tasks {
				if normalizeSplit(t.Split) != "test" {
					val = append(val, t)
				}
			}
		}
		leaked = len(val) > 0
	}
	if len(train) == 0 {
		train = append([]skill.MarkdownTask(nil), val...)
		leaked = leaked || len(train) > 0
	}
	if !leaked && len(train) > 0 && len(val) > 0 {
		ids := make(map[string]bool, len(train))
		for _, t := range train {
			ids[t.ID] = true
		}
		for _, t := range val {
			if ids[t.ID] {
				leaked = true
				break
			}
		}
	}
	return train, val, leaked
}

// scoreOn runs one val replay and projects it onto the gate metric.
func scoreOn(ctx context.Context, run TargetRunner, rubric RubricJudge, skillBody string, val []skill.MarkdownTask, metric string, w float64, replayOpts ReplayOpts) ([]ScoredTask, float64) {
	pairs := ReplayBatch(ctx, run, skillBody, val, rubric, replayOpts)
	h, s := AggregateScores(pairs)
	return pairs, skill.SelectGateScore(h, s, metric, w)
}

// taskDeltas compares candidate val scores against the baseline val tasks
// on the gate metric.
func taskDeltas(val []skill.MarkdownTask, base, cand []ScoredTask, metric string, w float64) ([]skill.ScoreDelta, []string) {
	byID := func(pairs []ScoredTask) map[string]ScoredTask {
		m := make(map[string]ScoredTask, len(pairs))
		for _, p := range pairs {
			m[p.Task.ID] = p
		}
		return m
	}
	bm, cm := byID(base), byID(cand)
	var deltas []skill.ScoreDelta
	var regressed []string
	for _, t := range val {
		b, okB := bm[t.ID]
		c, okC := cm[t.ID]
		if !okB || !okC {
			continue
		}
		bs := skill.SelectGateScore(b.Hard, b.Soft, metric, w)
		cs := skill.SelectGateScore(c.Hard, c.Soft, metric, w)
		d := skill.ScoreDelta{TaskID: t.ID, Tags: t.Tags, BaselineScore: bs, CandidateScore: cs}
		deltas = append(deltas, d)
		if d.Status() == "regressed" {
			regressed = append(regressed, t.ID)
		}
	}
	return deltas, regressed
}

// holdoutDetail renders per-val-task evidence from a replay batch.
func holdoutDetail(pairs []ScoredTask) []HoldoutTaskDetail {
	out := make([]HoldoutTaskDetail, 0, len(pairs))
	for _, p := range pairs {
		head := p.Response
		if len(head) > 200 {
			head = head[:200]
		}
		why := p.FailReason
		if len(why) > 200 {
			why = why[:200]
		}
		out = append(out, HoldoutTaskDetail{
			ID: p.Task.ID, Hard: p.Hard, Soft: p.Soft,
			ResponseLen: len(p.Response), ResponseHead: head, Why: why,
		})
	}
	return out
}

// Consolidate runs one reflect -> bounded-edit -> gate epoch over skillBody.
func Consolidate(ctx context.Context, run TargetRunner, rubric RubricJudge, tasks []skill.MarkdownTask, skillBody string, replayOpts ReplayOpts, opts ConsolidateOpts) ConsolidationResult {
	budget := opts.EditBudget
	if budget <= 0 {
		budget = DefaultEditBudget
	}
	metric := opts.GateMetric
	if metric == "" {
		metric = "mixed"
	}
	w := opts.MixedWeight
	if w <= 0 {
		w = DefaultMixedWeight
	}
	maxFail := opts.MaxFailuresInPrompt
	if maxFail <= 0 {
		maxFail = DefaultMaxFailuresInPrompt
	}

	train, val, leaked := SplitTasks(tasks)
	res := ConsolidationResult{HoldoutLeaked: leaked, NewSkill: skillBody}
	if len(train) == 0 && len(val) == 0 {
		res.GateAction = "noop"
		res.NoEditsReason = "no train/val tasks (test-only or empty input)"
		return res
	}

	// Baseline on the val slice: the gate reference.
	basePairs, baseScore := scoreOn(ctx, run, rubric, skillBody, val, metric, w, replayOpts)
	res.BaselineScore = baseScore
	res.HoldoutDetail = holdoutDetail(basePairs)

	// Train outcomes drive reflection.
	trainPairs := ReplayBatch(ctx, run, skillBody, train, rubric, replayOpts)
	var failures, successes []ScoredTask
	for _, p := range trainPairs {
		if p.Hard < 1 {
			failures = append(failures, p)
		} else {
			successes = append(successes, p)
		}
	}

	if opts.Reflect == nil {
		res.GateAction = "eval_only"
		res.NoEditsReason = "evaluation-only (no reflector)"
		res.CandidateScore = baseScore
		return res
	}

	if len(failures) > maxFail {
		failures = failures[:maxFail]
	}
	edits, raw, err := opts.Reflect(ctx, failures, successes, skillBody, budget)
	res.ReflectRaw = raw
	if err != nil {
		res.GateAction = "reject"
		res.NoEditsReason = "reflector failed"
		res.CallError = err.Error()
		res.CandidateScore = baseScore
		return res
	}

	// Skill-aware routing (SL-032): lapse reminders never enter the gated
	// learned-block flow. When skill-aware is off the reflector prompt never
	// tags lapses, but NormalizeEditRoute still fails closed to defect.
	var lapse []skill.TextEdit
	if opts.SkillAware {
		defect := edits[:0:0]
		for _, e := range edits {
			if skill.NormalizeEditRoute(e.Route) == skill.RouteExecutionLapse {
				lapse = append(lapse, e)
			} else {
				defect = append(defect, e)
			}
		}
		if len(lapse) > DefaultLapseCap {
			lapse = lapse[:DefaultLapseCap]
		}
		edits = defect
	}

	if len(edits) > budget {
		selected, details, ranked := rankAndSelect(ctx, edits, budget, opts.Ranker)
		if ranked {
			res.RankingDetails = details
		}
		res.ClippedEdits = len(edits) - len(selected)
		edits = selected
	}
	if len(edits) == 0 && len(lapse) == 0 {
		res.GateAction = "noop"
		res.NoEditsReason = "optimizer proposed no edits"
		res.CandidateScore = baseScore
		return res
	}

	candidate, applied, unmatched := skillBody, []skill.TextEdit(nil), []skill.TextEdit(nil)
	if len(edits) > 0 {
		candidate, applied, unmatched = skill.ApplyEdits(skillBody, edits)
	}
	res.Unmatched = unmatched
	if len(edits) > 0 {
		if err := skill.CheckFrontmatterFrozen(skillBody, candidate); err != nil {
			res.GateAction = "reject"
			res.NoEditsReason = "frontmatter freeze violated: " + err.Error()
			res.Unmatched = edits
			res.CandidateScore = baseScore
			return res
		}
		if len(applied) == 0 && len(lapse) == 0 {
			res.GateAction = "noop"
			res.NoEditsReason = "edits changed nothing (all unmatched)"
			res.CandidateScore = baseScore
			return res
		}
	}

	// Lapse reminders ride the protected appendix of whatever ships (SL-032);
	// the gate never scores them, by design, and the report logs the bypass.
	appendLapse := len(lapse) > 0
	if appendLapse {
		res.LapseReminders = lapse
		res.LapseBypassed = true
		candidate = skill.SetAppendix(candidate, lapseContent(lapse))
	}

	// Appendix-only night: no gated edit applied, reminders only. The gate
	// is bypassed by design (protected content, no learned-guidance change);
	// scores record the incumbent unchanged and the win is noise-flagged.
	if len(applied) == 0 {
		res.GateAction = "accept_lapse_only"
		res.Accepted = true
		res.CandidateScore = baseScore
		res.NoiseRange = true
		res.NewSkill = candidate
		return res
	}

	// Greedy mode accepts without validation (explicit opt-out only).
	if opts.Greedy {
		gate := skill.EvaluateGate(0, 0, 0, skill.GateOpts{Greedy: true, Applied: true})
		res.GateAction = gate.Action
		res.Accepted = gate.Accepted
		res.Applied = applied
		res.NewSkill = candidate
		return res
	}

	// Gated trial on the val slice.
	trialPairs, trialScore := scoreOn(ctx, run, rubric, candidate, val, metric, w, replayOpts)
	trialDeltas, trialRegressed := taskDeltas(val, basePairs, trialPairs, metric, w)
	var blocked []string
	if opts.GateNoRegression {
		blocked = trialRegressed
	}
	trialGate := skill.EvaluateGate(trialScore, baseScore, baseScore,
		skill.GateOpts{HoldoutLeaked: leaked, Regressed: blocked})
	if !trialGate.Accepted {
		res.GateAction = trialGate.Action
		res.Rejected = applied
		res.Deltas = trialDeltas
		res.CandidateScore = trialScore
		if trialGate.Action == "reject_unverified" {
			res.NoEditsReason = "gate abstained: validation leaked"
		}
		return res
	}

	// Fresh final replay decides: a later replay that regresses rolls the
	// tentative edits back into the rejected set (bookkeeping consistency).
	finalPairs, finalScore := scoreOn(ctx, run, rubric, candidate, val, metric, w, replayOpts)
	finalDeltas, finalRegressed := taskDeltas(val, basePairs, finalPairs, metric, w)
	blocked = nil
	if opts.GateNoRegression {
		blocked = finalRegressed
	}
	finalGate := skill.EvaluateGate(finalScore, baseScore, baseScore,
		skill.GateOpts{HoldoutLeaked: leaked, Regressed: blocked})
	res.Deltas = finalDeltas
	res.CandidateScore = finalScore
	res.HoldoutDetail = holdoutDetail(finalPairs)
	if !finalGate.Accepted {
		res.GateAction = finalGate.Action
		res.Rejected = applied
		if finalGate.Action == "reject_unverified" {
			res.NoEditsReason = "gate abstained: validation leaked"
		}
		return res
	}
	res.GateAction = finalGate.Action
	res.Accepted = true
	res.Applied = applied
	res.NewSkill = candidate
	res.NoiseRange = isNoiseRange(finalScore, baseScore, len(val))
	return res
}

// isNoiseRange flags single-seed wins inside the noise band (plan SL-034):
// deltas under 1.5 points, or validation slices smaller than the claim
// minimum, are annotated in the report as unproven rather than celebrated.
func isNoiseRange(candidate, baseline float64, valN int) bool {
	if valN < MinValTasksForClaim {
		return true
	}
	return candidate-baseline < NoiseDeltaThreshold
}

// lapseContent renders lapse reminders as appendix bullets: marker-stripped
// content, falling back to the rationale when the content is empty.
func lapseContent(lapse []skill.TextEdit) []string {
	lines := make([]string, 0, len(lapse))
	for _, e := range lapse {
		content := skill.CleanEditContent(e.Content)
		if content == "" {
			content = skill.CleanEditContent(e.Rationale)
		}
		if content != "" {
			lines = append(lines, content)
		}
	}
	return lines
}
