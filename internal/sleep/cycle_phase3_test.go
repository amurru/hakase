// cycle_phase3_test.go - Phase 3 cycle acceptance: the lr schedule reaches
// the reflector, slow-update guidance rides the baseline from state
// history, the optimizer-memory sidecar is written, dream rollouts and
// recall flow into the night, the judge seam separates, and per-skill
// epoch records land in the state file.
package sleep

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/knowledge"
	"amurru/hakase/internal/skill"
)

func TestCycle_LRSchedulerReachesReflector(t *testing.T) {
	env := newCycleEnv(t, true)
	// Two prior nights -> epoch 2. Linear over 10 nights from 4 to 1:
	// factor 0.8, budget = 1 + round(3*0.8) = 3.
	if err := SaveSleepState(env.statePath, SleepState{Nights: []NightRecord{
		{StartedAt: time.Now().UTC().Add(-2 * time.Hour), Outcome: "empty"},
		{StartedAt: time.Now().UTC().Add(-time.Hour), Outcome: "empty"},
	}}); err != nil {
		t.Fatal(err)
	}
	opts := baseCycleOpts(env)
	opts.LRScheduler = "linear"
	opts.LRHorizon = 10
	opts.LRFloor = 1
	var recorded []int
	opts.ReflectorFor = func(call ModelCaller, name string, budget int, rc ReflectContext) Reflector {
		recorded = append(recorded, budget)
		return DefaultReflectorFor(call, name, budget, rc)
	}
	if _, err := RunCycle(context.Background(), opts); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(recorded) != 1 || recorded[0] != 3 {
		t.Errorf("scheduled budget = %v, want [3]", recorded)
	}
}

func TestCycle_SlowUpdateFromStateHistory(t *testing.T) {
	env := newCycleEnv(t, true)
	// Two prior zero-score nights for demo: persistent_fail.
	if err := SaveSleepState(env.statePath, SleepState{Nights: []NightRecord{
		{StartedAt: time.Now().UTC().Add(-2 * time.Hour), Outcome: "empty", Groups: []NightGroupRecord{
			{SkillName: "demo", BaselineScore: 0, CandidateScore: 0, Consolidated: true}}},
		{StartedAt: time.Now().UTC().Add(-time.Hour), Outcome: "empty", Groups: []NightGroupRecord{
			{SkillName: "demo", BaselineScore: 0, CandidateScore: 0, Consolidated: true}}},
	}}); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(env.workDir, ".agents", "skills", "demo", "SKILL.md")
	var sawSlowUpdate bool
	opts := baseCycleOpts(env)
	opts.ReflectorFor = func(call ModelCaller, name string, budget int, rc ReflectContext) Reflector {
		inner := DefaultReflectorFor(call, name, budget, rc)
		return func(ctx context.Context, failures, successes []ScoredTask, skillBody string, b int) ([]skill.TextEdit, string, error) {
			if strings.Contains(skillBody, skill.SlowUpdateStart) &&
				strings.Contains(skillBody, "tasks keep failing") {
				sawSlowUpdate = true
			}
			return inner(ctx, failures, successes, skillBody, b)
		}
	}
	res, err := RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !sawSlowUpdate {
		t.Error("baseline must carry the slow-update guidance block")
	}
	g := res.Groups[0]
	if !g.Consolidated {
		t.Errorf("consolidated flag missing: %+v", g)
	}
	// The sidecar records tonight's row with the post-night trend.
	mem, err := os.ReadFile(OptimizerMemoryPath(live))
	if err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}
	if !strings.Contains(string(mem), "Per-night outcomes") {
		t.Errorf("sidecar malformed: %s", mem)
	}
	// State records the per-skill epoch outcome.
	st, _, err := LoadSleepState(env.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, n := range st.Nights {
		for _, rec := range n.Groups {
			if rec.SkillName == "demo" && rec.Consolidated {
				found = true
			}
		}
	}
	if !found {
		t.Error("state must record the demo group's epoch outcome")
	}
}

func TestCycle_JudgeSeparationReported(t *testing.T) {
	env := newCycleEnv(t, true)

	// Shared judge: reported, never silent.
	opts := baseCycleOpts(env)
	shared, err := RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("shared run: %v", err)
	}
	if !shared.SharedBackend || shared.JudgeModel != shared.TargetModel {
		t.Errorf("shared backend = %+v", shared)
	}
	sharedLogged := false
	for _, ln := range shared.Log {
		if strings.Contains(ln, "shared_backend=true") {
			sharedLogged = true
		}
	}
	if !sharedLogged {
		t.Error("judge sharing must be logged")
	}

	// Distinct judge caller: SharedBackend false, judge named.
	opts = baseCycleOpts(env)
	opts.JudgeCall = func(context.Context, string) (string, error) { return "", nil }
	opts.JudgeModelKey = "openai/judge-model"
	distinct, err := RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("distinct run: %v", err)
	}
	if distinct.SharedBackend || distinct.JudgeModel != "openai/judge-model" {
		t.Errorf("judge separation = %+v", distinct)
	}
}

func TestCycle_DreamRolloutsQuarantinedAndCounted(t *testing.T) {
	env := newCycleEnv(t, true)
	opts := baseCycleOpts(env)
	opts.DreamRollouts = true
	opts.DreamFactor = 0.5
	opts.DreamMiner = func(_ context.Context, name string, tasks []skill.MarkdownTask, _ float64, _ int) ([]skill.MarkdownTask, error) {
		if len(tasks) == 0 {
			t.Error("dream miner must see the group's train tasks")
		}
		return []skill.MarkdownTask{
			{ID: "dream-1", Intent: "synthetic variant one", Origin: "dream"},
			{ID: "dream-2", Intent: "synthetic variant two", Origin: "dream"},
		}, nil
	}
	res, err := RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	g := res.Groups[0]
	if g.DreamTasks != 2 {
		t.Errorf("dream tasks = %d, want 2", g.DreamTasks)
	}
	if !slicesContains(g.Notes, "dream: 2 synthetic train task(s)") {
		t.Errorf("dream note missing: %+v", g.Notes)
	}
	if res.Truncated {
		t.Error("dream spend must fit under the default ledger")
	}
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

func TestCycle_RecallFeedsReflectorContext(t *testing.T) {
	env := newCycleEnv(t, true)
	kbDir := filepath.Join(env.workDir, "kb")
	if err := os.MkdirAll(kbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	note := &knowledge.KnowledgeNote{
		Slug: "verify-output-lessons",
		Frontmatter: knowledge.KnowledgeFrontmatter{
			Title:   "Verifying output lessons",
			Tags:    []string{"lessons-learned"},
			Summary: "Always verify output before declaring success.",
		},
		Body: "Lessons learned: the agent should always verify output before declaring success on task check questions.",
	}
	note.Raw = string(knowledge.SerializeNote(note))
	if err := knowledge.SaveNote(kbDir, note); err != nil {
		t.Fatal(err)
	}
	opts := baseCycleOpts(env)
	opts.RecallK = 2
	opts.KnowledgeDir = kbDir
	var sawRecall bool
	opts.ReflectorFor = func(call ModelCaller, name string, budget int, rc ReflectContext) Reflector {
		if strings.Contains(rc.OptimizerMemory, "Recalled lessons") &&
			strings.Contains(rc.OptimizerMemory, "Verifying output lessons") {
			sawRecall = true
		}
		return DefaultReflectorFor(call, name, budget, rc)
	}
	res, err := RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !sawRecall {
		t.Error("recalled notes must reach the reflector context")
	}
	if !slicesContains(res.Groups[0].Notes, "recall: 1 knowledge note") {
		t.Errorf("recall note missing from group notes: %+v", res.Groups[0].Notes)
	}
}
