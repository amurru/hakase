// consolidate_test.go - SL-013 acceptance: split, gate flows, greedy.
package sleep

import (
	"context"
	"strings"
	"testing"

	"amurru/hakase/internal/skill"
)

func sleepTask(id, split, origin string) skill.MarkdownTask {
	return skill.MarkdownTask{ID: id, Intent: "intent " + id, ReferenceKind: "exact", Reference: "yes", Split: split, Origin: origin}
}

func TestSplitTasks_BasicAndLegacy(t *testing.T) {
	tasks := []skill.MarkdownTask{
		sleepTask("t1", "train", "real"),
		sleepTask("v1", "val", "real"),
		sleepTask("legacy-t", "replay", "real"),
		sleepTask("legacy-v", "holdout", "real"),
		sleepTask("te1", "test", "real"),
		sleepTask("dream-v", "val", "dream"), // dream quarantined to train
	}
	train, val, leaked := SplitTasks(tasks)
	if leaked {
		t.Fatal("disjoint split must not leak")
	}
	trainIDs := map[string]bool{}
	for _, x := range train {
		trainIDs[x.ID] = true
	}
	for _, want := range []string{"t1", "legacy-t", "dream-v"} {
		if !trainIDs[want] {
			t.Errorf("train missing %s: %v", want, trainIDs)
		}
	}
	if len(val) != 2 || val[0].ID != "v1" || val[1].ID != "legacy-v" {
		t.Errorf("val wrong: %+v", val)
	}
	for _, x := range append(train, val...) {
		if x.ID == "te1" {
			t.Error("test task leaked into train/val")
		}
	}
}

func TestSplitTasks_FallbackLeaks(t *testing.T) {
	// All-train input: val falls back onto train -> leaked.
	train, val, leaked := SplitTasks([]skill.MarkdownTask{sleepTask("t1", "train", "real")})
	if !leaked || len(val) != 1 || len(train) != 1 {
		t.Errorf("fallback must leak: train=%d val=%d leaked=%v", len(train), len(val), leaked)
	}
	// Empty input: no-op, no leak.
	_, _, leaked = SplitTasks(nil)
	if leaked {
		t.Error("empty input must not leak")
	}
	// Overlapping IDs across disjoint labels: leaked.
	_, _, leaked = SplitTasks([]skill.MarkdownTask{sleepTask("same", "train", "real"), sleepTask("same", "val", "real")})
	if !leaked {
		t.Error("overlapping IDs must leak")
	}
}

// scriptRunner answers from a script: task ID -> response.
func scriptRunner(script map[string]string) TargetRunner {
	return func(_ context.Context, _ string, task skill.MarkdownTask) (string, []string, error) {
		if r, ok := script[task.ID]; ok {
			return r, nil, nil
		}
		return "no", nil, nil
	}
}

// skillWithRule returns a skill body whose learned block contains marker,
// so add-edits append observably.
func skillWithRule() string {
	return "---\nname: demo\ndescription: Demo.\n---\n\n# Demo\n"
}

func TestConsolidate_AcceptsStrictImprovement(t *testing.T) {
	ctx := context.Background()
	tasks := []skill.MarkdownTask{
		{ID: "t1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "train"},
		{ID: "v1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "val"},
		{ID: "v2", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "val"},
	}
	// Baseline skill answers "no" everywhere; candidate answers "yes" once
	// the learned rule lands. The runner keys off the learned block.
	base := skillWithRule()
	run := func(_ context.Context, body string, task skill.MarkdownTask) (string, []string, error) {
		if strings.Contains(body, "Always answer yes.") {
			return "yes", nil, nil
		}
		return "no", nil, nil
	}
	reflect := func(_ context.Context, failures, _ []ScoredTask, _ string, _ int) ([]skill.TextEdit, string, error) {
		if len(failures) == 0 {
			return nil, "", nil
		}
		return []skill.TextEdit{{Op: "add", Content: "Always answer yes.", Rationale: "fixes failures"}}, "raw", nil
	}
	res := Consolidate(ctx, run, nil, tasks, base, ReplayOpts{}, ConsolidateOpts{Reflect: reflect})
	if !res.Accepted {
		t.Fatalf("must accept strict improvement: %+v", res)
	}
	if res.GateAction != "accept_new_best" {
		t.Errorf("action = %s", res.GateAction)
	}
	if len(res.Applied) != 1 || len(res.Deltas) != 2 {
		t.Errorf("applied=%d deltas=%d", len(res.Applied), len(res.Deltas))
	}
	if !strings.Contains(res.NewSkill, "Always answer yes.") {
		t.Error("candidate doc missing the rule")
	}
	if len(res.HoldoutDetail) != 2 {
		t.Errorf("holdout detail missing: %+v", res.HoldoutDetail)
	}
}

func TestConsolidate_RejectsNoGain(t *testing.T) {
	ctx := context.Background()
	tasks := []skill.MarkdownTask{
		{ID: "t1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "train"},
		{ID: "v1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "val"},
	}
	run := scriptRunner(map[string]string{"t1": "no", "v1": "no"})
	reflect := func(_ context.Context, _, _ []ScoredTask, _ string, _ int) ([]skill.TextEdit, string, error) {
		return []skill.TextEdit{{Op: "add", Content: "Useless rule.", Rationale: "x"}}, "raw", nil
	}
	res := Consolidate(ctx, run, nil, tasks, skillWithRule(), ReplayOpts{}, ConsolidateOpts{Reflect: reflect})
	if res.Accepted || res.GateAction != "reject" {
		t.Fatalf("must reject flat candidate: %+v", res)
	}
	if len(res.Rejected) != 1 || res.NewSkill != skillWithRule() {
		t.Errorf("rejected bookkeeping wrong: %+v", res)
	}
}

func TestConsolidate_LeakedNeverCertifies(t *testing.T) {
	ctx := context.Background()
	tasks := []skill.MarkdownTask{
		{ID: "t1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "train"},
	}
	run := func(_ context.Context, body string, _ skill.MarkdownTask) (string, []string, error) {
		if strings.Contains(body, "RULE") {
			return "yes", nil, nil
		}
		return "no", nil, nil
	}
	reflect := func(_ context.Context, _, _ []ScoredTask, _ string, _ int) ([]skill.TextEdit, string, error) {
		return []skill.TextEdit{{Op: "add", Content: "RULE.", Rationale: "x"}}, "", nil
	}
	res := Consolidate(ctx, run, nil, tasks, skillWithRule(), ReplayOpts{}, ConsolidateOpts{Reflect: reflect})
	if res.Accepted || res.GateAction != "reject_unverified" || !res.HoldoutLeaked {
		t.Fatalf("leaked night must abstain: %+v", res)
	}
}

func TestConsolidate_EvalOnlyAndNoop(t *testing.T) {
	ctx := context.Background()
	tasks := []skill.MarkdownTask{
		{ID: "t1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "train"},
		{ID: "v1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "val"},
	}
	run := scriptRunner(map[string]string{"t1": "yes", "v1": "no"})
	res := Consolidate(ctx, run, nil, tasks, skillWithRule(), ReplayOpts{}, ConsolidateOpts{})
	if res.GateAction != "eval_only" || res.Accepted {
		t.Fatalf("nil reflector must be eval-only: %+v", res)
	}
	if res.BaselineScore != 0 {
		t.Errorf("baseline val score = %v, want 0 (v1 answers no)", res.BaselineScore)
	}

	// Reflector proposing nothing: noop.
	emptyReflect := func(_ context.Context, _, _ []ScoredTask, _ string, _ int) ([]skill.TextEdit, string, error) {
		return nil, "", nil
	}
	res = Consolidate(ctx, run, nil, tasks, skillWithRule(), ReplayOpts{}, ConsolidateOpts{Reflect: emptyReflect})
	if res.GateAction != "noop" || res.NoEditsReason == "" {
		t.Fatalf("empty proposal must noop with reason: %+v", res)
	}
}

func TestConsolidate_GreedyAndClipping(t *testing.T) {
	ctx := context.Background()
	tasks := []skill.MarkdownTask{
		{ID: "t1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "train"},
		{ID: "v1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "val"},
	}
	run := scriptRunner(map[string]string{"t1": "no", "v1": "no"})
	reflect := func(_ context.Context, _, _ []ScoredTask, _ string, budget int) ([]skill.TextEdit, string, error) {
		_ = budget
		return []skill.TextEdit{
			{Op: "add", Content: "One."}, {Op: "add", Content: "Two."}, {Op: "add", Content: "Three."},
		}, "", nil
	}
	res := Consolidate(ctx, run, nil, tasks, skillWithRule(), ReplayOpts{},
		ConsolidateOpts{Reflect: reflect, Greedy: true, EditBudget: 2})
	if !res.Accepted || res.GateAction != "greedy_applied" {
		t.Fatalf("greedy must ship applied edits: %+v", res)
	}
	if len(res.Applied) != 2 || res.ClippedEdits != 1 {
		t.Errorf("budget clip wrong: applied=%d clipped=%d", len(res.Applied), res.ClippedEdits)
	}
}

func TestConsolidate_NoRegressionBlocks(t *testing.T) {
	ctx := context.Background()
	tasks := []skill.MarkdownTask{
		{ID: "t1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "train"},
		{ID: "v1", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "val"},
		{ID: "v2", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "val"},
	}
	// Baseline: v1 passes, v2 fails (0.5). Candidate: v1 fails, v2 passes
	// (0.5, lateral) while train improves. Mean gate would tie-accept...
	// tie (0.5 vs 0.5) is not a strict improvement, so craft instead:
	// candidate fixes train AND v2, breaks v1, and v3 added passing to lift
	// the mean: use three val tasks.
	tasks = append(tasks, skill.MarkdownTask{ID: "v3", Intent: "q", ReferenceKind: "exact", Reference: "yes", Split: "val"})
	base := map[string]string{"t1": "no", "v1": "yes", "v2": "no", "v3": "no"}
	cand := map[string]string{"t1": "yes", "v1": "no", "v2": "yes", "v3": "yes"}
	run := func(_ context.Context, body string, task skill.MarkdownTask) (string, []string, error) {
		if strings.Contains(body, "RULE") {
			return cand[task.ID], nil, nil
		}
		return base[task.ID], nil, nil
	}
	reflect := func(_ context.Context, _, _ []ScoredTask, _ string, _ int) ([]skill.TextEdit, string, error) {
		return []skill.TextEdit{{Op: "add", Content: "RULE.", Rationale: "x"}}, "", nil
	}
	// Mean: baseline val 1/3, candidate val 2/3 -> strict improvement, but
	// v1 regressed: strict mode must block.
	res := Consolidate(ctx, run, nil, tasks, skillWithRule(), ReplayOpts{},
		ConsolidateOpts{Reflect: reflect, GateNoRegression: true})
	if res.Accepted {
		t.Fatalf("no-regression gate must block: %+v", res)
	}
	if len(res.Rejected) != 1 {
		t.Fatalf("tentative edits must roll back to rejected: %+v", res)
	}
	found := false
	for _, d := range res.Deltas {
		if d.TaskID == "v1" && d.Status() == "regressed" {
			found = true
		}
	}
	if !found {
		t.Errorf("v1 regression missing from deltas: %+v", res.Deltas)
	}
}
