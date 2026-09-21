// cycle_test.go - SL-022 acceptance: dry-run stays read-only, a run stages
// one night with the gate table, empty nights advance the checkpoint, the
// token guard truncates, the model-key pause is hard, the checkpoint is
// monotonic, auto-adopt is managed-only, and corrupt state rolls back.
// Everything runs on stubbed seams with no network and no live model.
package sleep

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/skill"
)

// cycleEnv builds a temp project: a demo skill (optionally sleep-managed),
// a reviewed seed task file (4 train + 2 val, exact "yes"), and paths.
type cycleEnv struct {
	workDir, statePath, outputDir, tasksPath string
}

func writeCycleSkill(t *testing.T, dir string, managed bool) string {
	t.Helper()
	skillDir := filepath.Join(dir, ".agents", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: demo\ndescription: Demo skill for cycle tests.\n---\n\n# Demo\n\nHand-written guidance.\n"
	if managed {
		doc += "\n" + skill.LearnedStart + "\n\n## Learned preferences & procedures\n\n- existing learned line\n" + skill.LearnedEnd + "\n"
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(skillDir, "SKILL.md")
}

func writeSeedTasks(t *testing.T, path string, hint string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"tasks":[`)
	for i := 1; i <= 4; i++ {
		b.WriteString(`{"id":"t` + string(rune('0'+i)) + `","intent":"is the task ` + string(rune('0'+i)) + ` done?","reference_kind":"exact","reference":"yes","split":"train","skill_hint":"` + hint + `"}`)
		if i < 4 || true {
			b.WriteString(",")
		}
	}
	b.WriteString(`{"id":"v1","intent":"is the check green?","reference_kind":"exact","reference":"yes","split":"val","skill_hint":"` + hint + `"},`)
	b.WriteString(`{"id":"v2","intent":"is the check red?","reference_kind":"exact","reference":"yes","split":"val","skill_hint":"` + hint + `"}]}`)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MarkTasksReviewed(path, "tester"); err != nil {
		t.Fatal(err)
	}
}

// stubCycleCall answers replay prompts from the skill body's learned line
// (baseline scores 0, the edited candidate scores 1) and proposes one add
// edit to the reflector.
func stubCycleCall() ModelCaller {
	return func(_ context.Context, prompt string) (string, error) {
		switch {
		case strings.Contains(prompt, "Follow this skill document"):
			if strings.Contains(prompt, "always verify output") {
				return "yes", nil
			}
			return "no", nil
		case strings.Contains(prompt, "You are improving a saved markdown skill"):
			return "```json\n[{\"op\":\"add\",\"content\":\"learned procedure: always verify output\",\"rationale\":\"fix failures\"}]\n```", nil
		default:
			return "", nil
		}
	}
}

func newCycleEnv(t *testing.T, managed bool) cycleEnv {
	t.Helper()
	root := t.TempDir()
	env := cycleEnv{
		workDir:   root,
		statePath: filepath.Join(root, "state", "sleep-state.json"),
		outputDir: filepath.Join(root, "outputs", "sleep"),
		tasksPath: filepath.Join(root, "tasks.json"),
	}
	writeCycleSkill(t, root, managed)
	writeSeedTasks(t, env.tasksPath, "demo")
	return env
}

func baseCycleOpts(env cycleEnv) CycleOpts {
	return CycleOpts{
		SeedTasksPath: env.tasksPath,
		Call:          stubCycleCall(),
		StatePath:     env.statePath,
		OutputDir:     env.outputDir,
		WorkDir:       env.workDir,
		ModelKey:      "gemini/test-model",
		AutoAdopt:     true,
	}
}

func TestCycle_RunStagesNightAndAutoAdoptsManaged(t *testing.T) {
	env := newCycleEnv(t, true)
	live := filepath.Join(env.workDir, ".agents", "skills", "demo", "SKILL.md")

	res, err := RunCycle(context.Background(), baseCycleOpts(env))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.DryRun || res.Truncated {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(res.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(res.Groups))
	}
	g := res.Groups[0]
	if g.SkillName != "demo" || g.Consolidation == nil || !g.Consolidation.Accepted {
		t.Fatalf("group = %+v, want accepted demo consolidation", g)
	}
	if !g.Adopted {
		t.Errorf("managed skill must auto-adopt, got skipped=%q", g.Skipped)
	}
	if g.StagingDir == "" {
		t.Fatal("staging dir missing")
	}
	// Night container holds the gate table + diagnostics.
	if _, err := os.Stat(filepath.Join(filepath.Dir(g.StagingDir), "night.md")); err != nil {
		t.Errorf("night.md missing: %v", err)
	}
	// The auto-adopt updated the live skill in place.
	liveBytes, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(liveBytes), "always verify output") {
		t.Error("auto-adopt must install the accepted proposal into the live managed skill")
	}
	// State: model key pinned, night recorded as staged, checkpoint set.
	data, err := os.ReadFile(env.statePath)
	if err != nil {
		t.Fatalf("state missing: %v", err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(env.statePath); info.Mode().Perm() != 0o600 {
			t.Errorf("state mode = %o, want 600", info.Mode().Perm())
		}
	}
	var st SleepState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if st.LastModelKey != "gemini/test-model" {
		t.Errorf("last_model_key = %q", st.LastModelKey)
	}
	if len(st.Nights) != 1 || st.Nights[0].Outcome != "staged" {
		t.Errorf("nights = %+v, want one staged record", st.Nights)
	}
	if st.LastHarvest.IsZero() {
		t.Error("checkpoint must advance on a staging night")
	}
}

func TestCycle_AutoAdoptSkipsHandWritten(t *testing.T) {
	env := newCycleEnv(t, false)
	live := filepath.Join(env.workDir, ".agents", "skills", "demo", "SKILL.md")
	before, _ := os.ReadFile(live)

	res, err := RunCycle(context.Background(), baseCycleOpts(env))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	g := res.Groups[0]
	if !g.Consolidation.Accepted {
		t.Fatalf("consolidation should accept: %+v", g.Consolidation)
	}
	if g.Adopted {
		t.Error("hand-written skills must never auto-adopt (M5)")
	}
	after, _ := os.ReadFile(live)
	if string(before) != string(after) {
		t.Error("live hand-written skill must stay untouched without explicit adopt")
	}
	if g.StagingDir == "" {
		t.Error("accepted proposal must still be staged for review")
	}
}

func TestCycle_DryRunIsReadOnly(t *testing.T) {
	env := newCycleEnv(t, true)
	opts := baseCycleOpts(env)
	opts.DryRun = true
	opts.Call = nil // dry-run needs no model

	res, err := RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !res.DryRun || res.MinedTasks != 6 {
		t.Fatalf("dry-run result = %+v, want 6 mined tasks", res)
	}
	if len(res.Groups) != 1 || res.Groups[0].Tasks != 6 {
		t.Fatalf("groups = %+v, want one 6-task group", res.Groups)
	}
	if _, err := os.Stat(env.statePath); !os.IsNotExist(err) {
		t.Error("dry-run must not write state")
	}
	if _, err := os.Stat(env.outputDir); !os.IsNotExist(err) {
		t.Error("dry-run must not create outputs")
	}
}

func TestCycle_TokenBudgetTruncates(t *testing.T) {
	env := newCycleEnv(t, true)
	opts := baseCycleOpts(env)
	opts.MaxTokensPerNight = 1

	res, err := RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Truncated {
		t.Errorf("token budget must truncate the night, got %+v", res)
	}
	var st SleepState
	data, err := os.ReadFile(env.statePath)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(data, &st)
	if len(st.Nights) != 1 || st.Nights[0].Outcome != "aborted" || st.Nights[0].AbortReason == "" {
		t.Errorf("night record = %+v, want aborted with reason", st.Nights)
	}
}

func TestCycle_ModelKeyHardPause(t *testing.T) {
	env := newCycleEnv(t, true)
	// Seed a prior state under a different model identity.
	if err := SaveSleepState(env.statePath, SleepState{LastModelKey: "openai/old-model"}); err != nil {
		t.Fatal(err)
	}
	opts := baseCycleOpts(env)
	if _, err := RunCycle(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "--acknowledge-model-change") {
		t.Fatalf("model change must hard-pause, got err=%v", err)
	}
	opts.AcknowledgeModelChange = true
	if _, err := RunCycle(context.Background(), opts); err != nil {
		t.Fatalf("acknowledged change must proceed: %v", err)
	}
}

func TestCycle_MonotonicCheckpointEnforced(t *testing.T) {
	env := newCycleEnv(t, true)
	future := time.Now().UTC().Add(time.Hour)
	if err := SaveSleepState(env.statePath, SleepState{LastHarvest: future}); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCycle(context.Background(), baseCycleOpts(env)); err == nil || !strings.Contains(err.Error(), "backwards") {
		t.Fatalf("checkpoint regression must be refused, got err=%v", err)
	}
}

func TestCycle_EmptyNightAdvancesCheckpoint(t *testing.T) {
	env := newCycleEnv(t, true)
	emptySessions := filepath.Join(env.workDir, "empty-sessions")
	if err := os.MkdirAll(emptySessions, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := baseCycleOpts(env)
	opts.SeedTasksPath = ""
	opts.Harvest = HarvestOpts{SessionsDir: emptySessions}

	res, err := RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("empty night: %v", err)
	}
	if res.MinedTasks != 0 || len(res.Groups) != 0 || res.StagingDir != "" {
		t.Errorf("empty night result = %+v", res)
	}
	var st SleepState
	data, err := os.ReadFile(env.statePath)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(data, &st)
	if len(st.Nights) != 1 || st.Nights[0].Outcome != "empty" {
		t.Errorf("nights = %+v, want one empty record", st.Nights)
	}
	if st.LastHarvest.IsZero() {
		t.Error("empty night must advance the checkpoint")
	}
}

func TestCycle_UnresolvableHintSkips(t *testing.T) {
	env := newCycleEnv(t, true)
	writeSeedTasks(t, env.tasksPath, "ghost")
	opts := baseCycleOpts(env)

	res, err := RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Skipped == "" {
		t.Fatalf("groups = %+v, want one logged skip", res.Groups)
	}
	if res.StagingDir != "" {
		t.Error("a fully skipped night stages nothing")
	}
}

func TestCycle_CorruptStateRollsBack(t *testing.T) {
	env := newCycleEnv(t, true)
	if err := os.MkdirAll(filepath.Dir(env.statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.statePath, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := RunCycle(context.Background(), baseCycleOpts(env))
	if err != nil {
		t.Fatalf("corrupt state must roll back, not abort: %v", err)
	}
	rollbackLogged := false
	for _, ln := range res.Log {
		if strings.Contains(ln, "corrupt") {
			rollbackLogged = true
		}
	}
	if !rollbackLogged {
		t.Error("corrupt-state reset must be logged, never silent")
	}
	// The bad file is preserved aside.
	entries, _ := os.ReadDir(filepath.Dir(env.statePath))
	foundAside := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "sleep-state.json.corrupt-") {
			foundAside = true
		}
	}
	if !foundAside {
		t.Error("corrupt state file must be preserved for forensics")
	}
}

func TestPruneStagingDirs_ShortNamesDoNotPanic(t *testing.T) {
	// CodeRabbit: the old len<8 guard sliced [:15] and panicked on 8-14
	// character directory names.
	dir := t.TempDir()
	for _, name := range []string{"20260101", "night-1234567", "20260102-150405", "not-a-timestamp-long"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "20260102-150405", "night.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pruneStagingDirs(dir, StatePruneAge, time.Now) // must not panic
}

func TestCycle_FanOutRunsEveryGroup(t *testing.T) {
	root := t.TempDir()
	writeCycleSkill(t, root, true)
	other := filepath.Join(root, ".agents", "skills", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: other\ndescription: Other skill for fan-out tests.\n---\n\n# Other\n"
	if err := os.WriteFile(filepath.Join(other, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	env := cycleEnv{
		workDir:   root,
		statePath: filepath.Join(root, "state", "sleep-state.json"),
		outputDir: filepath.Join(root, "outputs", "sleep"),
		tasksPath: filepath.Join(root, "tasks.json"),
	}
	writeSeedTasks(t, env.tasksPath, "other")

	// Without fan-out: single largest group runs; other hints logged.
	opts := baseCycleOpts(env)
	opts.SkillName = "other"
	res, err := RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("targeted run: %v", err)
	}
	if len(res.Groups) != 1 || res.Groups[0].SkillName != "other" {
		t.Fatalf("targeted groups = %+v", res.Groups)
	}

	// Fan-out with two hints: both resolve and consolidate.
	both := filepath.Join(root, "both.json")
	var b strings.Builder
	b.WriteString(`{"tasks":[`)
	for i := 1; i <= 2; i++ {
		b.WriteString(`{"id":"t` + string(rune('0'+i)) + `","intent":"q?","reference_kind":"exact","reference":"yes","split":"train","skill_hint":"demo"},`)
		b.WriteString(`{"id":"v` + string(rune('0'+i)) + `","intent":"q?","reference_kind":"exact","reference":"yes","split":"val","skill_hint":"other"},`)
	}
	b.WriteString(`{"id":"v3","intent":"q?","reference_kind":"exact","reference":"yes","split":"val","skill_hint":"other"}]}`)
	if err := os.WriteFile(both, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MarkTasksReviewed(both, "tester"); err != nil {
		t.Fatal(err)
	}
	opts = baseCycleOpts(env)
	opts.SeedTasksPath = both
	opts.FanOut = true
	res, err = RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("fan-out run: %v", err)
	}
	if len(res.Groups) != 2 {
		t.Fatalf("fan-out groups = %+v, want both", res.Groups)
	}
	for _, g := range res.Groups {
		if g.Skipped != "" {
			t.Errorf("fan-out group %s skipped: %s", g.SkillName, g.Skipped)
		}
	}
}

// Compile-time guard: the cycle's consolidation consumes the skill package
// seam set the plan requires.
var _ = skill.ApplyEdits
