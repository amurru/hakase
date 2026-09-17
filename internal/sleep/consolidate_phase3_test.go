// consolidate_phase3_test.go - SL-030/SL-032/SL-034 acceptance over the
// consolidation epoch: rank_and_select, skill-aware lapse routing, and the
// noise-band annotation.
package sleep

import (
	"context"
	"strings"
	"testing"

	"amurru/hakase/internal/skill"
)

// gateTasks builds 2 train + 2 val exact tasks.
func gateTasks() []skill.MarkdownTask {
	return []skill.MarkdownTask{
		sleepTask("t1", "train", "real"),
		sleepTask("t2", "train", "real"),
		sleepTask("v1", "val", "real"),
		sleepTask("v2", "val", "real"),
	}
}

// passRunner answers "yes" only when the skill body carries the learned
// rule, so the candidate strictly improves on val.
func passRunner() TargetRunner {
	return func(_ context.Context, skillBody string, _ skill.MarkdownTask) (string, []string, error) {
		if strings.Contains(skillBody, "learned rule: verify output") {
			return "yes", nil, nil
		}
		return "no", nil, nil
	}
}

func staticReflector(edits []skill.TextEdit) Reflector {
	return func(context.Context, []ScoredTask, []ScoredTask, string, int) ([]skill.TextEdit, string, error) {
		return edits, "raw", nil
	}
}

func TestConsolidate_LapseRemindersBypassGateIntoAppendix(t *testing.T) {
	edits := []skill.TextEdit{
		{Op: "add", Content: "learned rule: verify output", Route: skill.RouteSkillDefect},
		{Op: "add", Content: "reminder: re-read the checklist before answering", Route: skill.RouteExecutionLapse},
		{Op: "add", Content: "reminder: cite the section used", Route: skill.RouteExecutionLapse},
	}
	res := Consolidate(context.Background(), passRunner(), nil, gateTasks(), skillWithRule(),
		ReplayOpts{}, ConsolidateOpts{Reflect: staticReflector(edits), SkillAware: true})

	if !res.Accepted {
		t.Fatalf("accepted body edit expected: %+v", res)
	}
	if !res.LapseBypassed || len(res.LapseReminders) != 2 {
		t.Errorf("lapse reminders = %+v bypassed=%v", res.LapseReminders, res.LapseBypassed)
	}
	if !strings.Contains(res.NewSkill, skill.AppendixStart) ||
		!strings.Contains(res.NewSkill, "re-read the checklist before answering") {
		t.Errorf("appendix reminders missing from the proposal: %s", res.NewSkill)
	}
	if !strings.Contains(res.NewSkill, skill.LearnedStart) || !strings.Contains(res.NewSkill, "learned rule: verify output") {
		t.Errorf("defect edit must still land in the learned block: %s", res.NewSkill)
	}
	// The gate scored the defect flow; the delta reflects the learned rule.
	if res.CandidateScore <= res.BaselineScore {
		t.Errorf("strict improvement expected: %+v", res)
	}
	if res.GateAction != "accept_new_best" && res.GateAction != "accept" {
		t.Errorf("gate action = %s", res.GateAction)
	}
}

func TestConsolidate_AppendixOnlyNightAcceptedUngated(t *testing.T) {
	edits := []skill.TextEdit{
		{Op: "add", Content: "reminder: slow down on multi-part asks", Route: skill.RouteExecutionLapse},
	}
	res := Consolidate(context.Background(), passRunner(), nil, gateTasks(), skillWithRule(),
		ReplayOpts{}, ConsolidateOpts{Reflect: staticReflector(edits), SkillAware: true})

	if res.GateAction != "accept_lapse_only" || !res.Accepted {
		t.Fatalf("appendix-only night: %+v", res)
	}
	if res.CandidateScore != res.BaselineScore {
		t.Errorf("appendix-only night must not move the score: %+v", res)
	}
	if !res.NoiseRange {
		t.Error("appendix-only accept must be noise-flagged (no scored change)")
	}
	if !strings.Contains(res.NewSkill, "reminder: slow down on multi-part asks") {
		t.Errorf("reminder missing: %s", res.NewSkill)
	}
}

func TestConsolidate_LapseCapBoundsUngatedContent(t *testing.T) {
	var lapse []skill.TextEdit
	for i := 0; i < DefaultLapseCap+2; i++ {
		lapse = append(lapse, skill.TextEdit{
			Op: "add", Content: "reminder number " + string(rune('a'+i)), Route: skill.RouteExecutionLapse})
	}
	res := Consolidate(context.Background(), passRunner(), nil, gateTasks(), skillWithRule(),
		ReplayOpts{}, ConsolidateOpts{Reflect: staticReflector(lapse), SkillAware: true})
	if len(res.LapseReminders) != DefaultLapseCap {
		t.Errorf("lapse reminders = %d, want capped at %d", len(res.LapseReminders), DefaultLapseCap)
	}
}

func TestConsolidate_LapseDroppedWithRejectedCandidate(t *testing.T) {
	// Candidate regresses on val: the gate rejects; lapse reminders ride
	// only accepted proposals.
	failingRunner := func(_ context.Context, skillBody string, _ skill.MarkdownTask) (string, []string, error) {
		if strings.Contains(skillBody, "learned rule: verify output") {
			return "no", nil, nil // candidate worse than baseline on rule tasks
		}
		return "yes", nil, nil
	}
	edits := []skill.TextEdit{
		{Op: "add", Content: "learned rule: verify output"},
		{Op: "add", Content: "reminder: check twice", Route: skill.RouteExecutionLapse},
	}
	res := Consolidate(context.Background(), failingRunner, nil, gateTasks(), skillWithRule(),
		ReplayOpts{}, ConsolidateOpts{Reflect: staticReflector(edits), SkillAware: true})
	if res.Accepted {
		t.Fatalf("regressing candidate must be rejected: %+v", res)
	}
	if !res.LapseBypassed || len(res.LapseReminders) != 1 {
		t.Errorf("lapse bookkeeping = %+v bypassed=%v", res.LapseReminders, res.LapseBypassed)
	}
	if strings.Contains(res.NewSkill, skill.AppendixStart) {
		t.Errorf("rejected candidate must not carry the appendix: %s", res.NewSkill)
	}
}

func TestConsolidate_SkillAwareOffTreatsAllEditsAsDefect(t *testing.T) {
	edits := []skill.TextEdit{
		{Op: "add", Content: "learned rule: verify output", Route: skill.RouteExecutionLapse},
	}
	res := Consolidate(context.Background(), passRunner(), nil, gateTasks(), skillWithRule(),
		ReplayOpts{}, ConsolidateOpts{Reflect: staticReflector(edits)})
	if !res.Accepted || res.LapseBypassed {
		t.Fatalf("skill-aware off must keep the gated defect flow: %+v", res)
	}
	if strings.Contains(res.NewSkill, skill.AppendixStart) {
		t.Errorf("no appendix may appear when skill-aware is off: %s", res.NewSkill)
	}
	if !strings.Contains(res.NewSkill, "learned rule: verify output") {
		t.Errorf("the edit must land in the learned block: %s", res.NewSkill)
	}
}

func TestConsolidate_RankerSelectsOverBudgetPool(t *testing.T) {
	edits := []skill.TextEdit{
		{Op: "add", Content: "edit one"},
		{Op: "add", Content: "learned rule: verify output"},
		{Op: "add", Content: "edit three"},
	}
	ranker := func(_ context.Context, pool []skill.TextEdit, budget int) ([]skill.TextEdit, []RankingDetail, error) {
		if budget != 2 || len(pool) != 3 {
			t.Errorf("ranker got budget=%d pool=%d", budget, len(pool))
		}
		// The ranker keeps the useful rule and "edit one"; "edit three" drops.
		selected := []skill.TextEdit{pool[1], pool[0]}
		details := make([]RankingDetail, len(pool))
		for i := range pool {
			details[i] = RankingDetail{Index: i, Op: pool[i].Op}
		}
		details[1].Selected = true
		details[0].Selected = true
		return selected, details, nil
	}
	res := Consolidate(context.Background(), passRunner(), nil, gateTasks(), skillWithRule(),
		ReplayOpts{}, ConsolidateOpts{EditBudget: 2, Reflect: staticReflector(edits), Ranker: ranker})
	if len(res.RankingDetails) != 3 {
		t.Errorf("ranking details = %d, want 3", len(res.RankingDetails))
	}
	if res.ClippedEdits != 1 {
		t.Errorf("clipped = %d, want 1", res.ClippedEdits)
	}
	for _, e := range res.Applied {
		if e.Content == "edit three" {
			t.Errorf("ranker must exclude unselected edits, got applied %+v", res.Applied)
		}
	}
	if !res.Accepted {
		t.Errorf("the ranked-in rule edit must gate and accept: %+v", res)
	}
}

func TestConsolidate_NoiseRangeAnnotatedOnSmallVal(t *testing.T) {
	res := Consolidate(context.Background(), passRunner(), nil, gateTasks(), skillWithRule(),
		ReplayOpts{}, ConsolidateOpts{Reflect: staticReflector([]skill.TextEdit{
			{Op: "add", Content: "learned rule: verify output"},
		})})
	if !res.Accepted {
		t.Fatalf("expected accept: %+v", res)
	}
	if !res.NoiseRange {
		t.Errorf("a 2-task val win must be noise-flagged (SL-034): %+v", res)
	}
}
