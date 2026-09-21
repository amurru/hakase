// mine_test.go - SL-021 acceptance: deterministic seeded split with
// disjoint slices, legacy no-test behavior, caps, skill clustering,
// outcome/check derivation, dream quarantine, LLM-miner hook, and the
// machine-marker write that trips the M4 review gate.
package sleep

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"amurru/hakase/internal/skill"
)

// makeDigest builds one digest with nTurns prompt/final turn records.
func makeDigest(id string, nTurns int, skillUsed []string) SessionDigest {
	d := SessionDigest{SessionID: id, SkillsUsed: skillUsed}
	for i := 0; i < nTurns; i++ {
		d.Turns = append(d.Turns, SessionTurn{
			Prompt: "prompt " + id + " turn " + string(rune('a'+i)),
			Final:  "final " + id + " turn " + string(rune('a'+i)),
		})
	}
	d.TurnCount = len(d.Turns)
	return d
}

func countSplit(tasks []skill.MarkdownTask, split string) int {
	n := 0
	for _, t := range tasks {
		if t.Split == split {
			n++
		}
	}
	return n
}

func TestMine_DeterministicDisjointSplit(t *testing.T) {
	var digests []SessionDigest
	for i := 0; i < 6; i++ {
		digests = append(digests, makeDigest("sess"+string(rune('0'+i)), 5, nil))
	}
	r1 := Mine(digests, MineOpts{TrainFraction: 0.6, ValFraction: 0.2, TestFraction: 0.2})
	r2 := Mine(digests, MineOpts{TrainFraction: 0.6, ValFraction: 0.2, TestFraction: 0.2})
	b1, _ := json.Marshal(r1.Tasks)
	b2, _ := json.Marshal(r2.Tasks)
	if string(b1) != string(b2) {
		t.Error("mining must be deterministic for identical input")
	}
	if len(r1.Tasks) != 30 {
		t.Fatalf("tasks = %d, want 30", len(r1.Tasks))
	}
	// Disjoint by ID: every ID appears exactly once.
	seen := map[string]int{}
	for _, tk := range r1.Tasks {
		seen[tk.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("task %s assigned %d times, want 1", id, n)
		}
	}
	if n := countSplit(r1.Tasks, "test"); n < 4 {
		t.Errorf("test slice = %d, want ~6 (20%%)", n)
	}
	if len(r1.Log) == 0 {
		t.Error("split decisions must be logged")
	}
}

func TestMine_LegacyNoTestSlice(t *testing.T) {
	digests := []SessionDigest{makeDigest("sess", 10, nil)}
	res := Mine(digests, MineOpts{})
	if n := countSplit(res.Tasks, "test"); n != 0 {
		t.Errorf("default fractions must keep the legacy no-test behavior, got %d test tasks", n)
	}
	if countSplit(res.Tasks, "train") == 0 || countSplit(res.Tasks, "val") == 0 {
		t.Error("default fractions must populate train and val")
	}
}

func TestMine_CapAndPriority(t *testing.T) {
	digests := []SessionDigest{makeDigest("sess", 60, nil)}
	// Mark the first turn as a failure: it must survive the cap.
	digests[0].Turns[0].Signals = []string{"wrong"}
	res := Mine(digests, MineOpts{})
	if len(res.Tasks) != DefaultMaxTasksPerNight {
		t.Fatalf("tasks = %d, want cap %d", len(res.Tasks), DefaultMaxTasksPerNight)
	}
	found := false
	for _, tk := range res.Tasks {
		if tk.ID == minedTaskID("sess", 0, "prompt sess turn a") {
			found = true
			if tk.Split == "test" {
				t.Skip("failure task landed in test slice; still counted")
			}
		}
	}
	if !found {
		t.Error("failure-shaped turn must be prioritized into the kept set")
	}
	capLogged := false
	for _, ln := range res.Log {
		if strings.Contains(ln, "cap:") {
			capLogged = true
		}
	}
	if !capLogged {
		t.Error("cap hit must be logged")
	}
}

func TestMine_SkillHintAndCatchAll(t *testing.T) {
	digests := []SessionDigest{
		makeDigest("s1", 1, []string{"pdf-tools"}),
		makeDigest("s2", 1, nil),
	}
	res := Mine(digests, MineOpts{})
	byID := map[string]skill.MarkdownTask{}
	for _, tk := range res.Tasks {
		byID[tk.ID] = tk
	}
	hinted := byID[minedTaskID("s1", 0, "prompt s1 turn a")]
	caught := byID[minedTaskID("s2", 0, "prompt s2 turn a")]
	if hinted.SkillHint != "pdf-tools" {
		t.Errorf("hint = %q, want pdf-tools", hinted.SkillHint)
	}
	if caught.SkillHint != ManagedCatchAll {
		t.Errorf("catch-all = %q, want %q", caught.SkillHint, ManagedCatchAll)
	}
}

func TestMine_OutcomesAndChecks(t *testing.T) {
	neg := makeDigest("neg", 1, nil)
	neg.Turns[0].Signals = []string{"wrong"}
	neg.Turns[0].Final = "the command failed with an error"

	pos := makeDigest("pos", 1, nil)
	pos.Turns[0].Signals = []string{"thanks"}
	pos.Turns[0].Final = "all done"

	retry := makeDigest("retry", 2, nil)
	retry.Turns[1].Prompt = "try again please"

	yesNo := makeDigest("yesno", 1, nil)
	yesNo.Turns[0].Prompt = "is the build green now?"
	yesNo.Turns[0].Final = "Yes, the build passed."

	plain := makeDigest("plain", 1, nil)
	plain.Turns[0].Final = "here is the summary"

	res := Mine([]SessionDigest{neg, pos, retry, yesNo, plain}, MineOpts{})
	byID := map[string]skill.MarkdownTask{}
	for _, tk := range res.Tasks {
		byID[tk.ID] = tk
	}
	hasTag := func(tk skill.MarkdownTask, tag string) bool {
		for _, tg := range tk.Tags {
			if tg == tag {
				return true
			}
		}
		return false
	}
	if !hasTag(byID[minedTaskID("neg", 0, "prompt neg turn a")], outcomeFail) {
		t.Error("error final must classify outcome:fail")
	}
	if !hasTag(byID[minedTaskID("pos", 0, "prompt pos turn a")], outcomeSuccess) {
		t.Error("positive feedback must classify outcome:success")
	}
	if !hasTag(byID[minedTaskID("retry", 0, "prompt retry turn a")], outcomeFail) {
		t.Error("retry follow-up must classify the first turn outcome:fail")
	}
	yn := byID[minedTaskID("yesno", 0, "is the build green now?")]
	if yn.ReferenceKind != "exact" || yn.Reference != "yes" {
		t.Errorf("derivable yes/no check = %s/%q, want exact/yes", yn.ReferenceKind, yn.Reference)
	}
	pl := byID[minedTaskID("plain", 0, "prompt plain turn a")]
	if pl.ReferenceKind != "rubric" || !hasTag(pl, needsReviewTag) {
		t.Errorf("underivable check = %s, want rubric + needs_review", pl.ReferenceKind)
	}
}

func TestMine_DreamQuarantine(t *testing.T) {
	stub := func([]SessionDigest) ([]skill.MarkdownTask, error) {
		var tasks []skill.MarkdownTask
		for i := 0; i < 10; i++ {
			tasks = append(tasks, skill.MarkdownTask{
				ID: "dream-" + string(rune('a'+i)), Intent: "synthetic", Origin: "dream",
			})
		}
		for i := 0; i < 10; i++ {
			tasks = append(tasks, skill.MarkdownTask{
				ID: "real-" + string(rune('a'+i)), Intent: "real task", Origin: "real",
			})
		}
		return tasks, nil
	}
	res := Mine(nil, MineOpts{LLMMiner: stub, TestFraction: 0.2, ValFraction: 0.2, TrainFraction: 0.6})
	quarantineLogged := false
	for _, ln := range res.Log {
		if strings.Contains(ln, "quarantine:") {
			quarantineLogged = true
		}
	}
	if !quarantineLogged {
		t.Error("dream quarantine must be logged")
	}
	for _, tk := range res.Tasks {
		if tk.Origin == "dream" && tk.Split != "train" {
			t.Errorf("dream task %s in split %q, want train-only quarantine", tk.ID, tk.Split)
		}
	}
	if n := countSplit(res.Tasks, "test"); n != 2 {
		t.Errorf("test slice = %d, want 2 (20%% of the 10 real tasks)", n)
	}
}

func TestMine_LLMMinerError(t *testing.T) {
	res := Mine(nil, MineOpts{LLMMiner: func([]SessionDigest) ([]skill.MarkdownTask, error) {
		return nil, errMineFailed
	}})
	if len(res.Tasks) != 0 {
		t.Error("failed miner must emit no tasks")
	}
	if len(res.Log) == 0 || !strings.Contains(res.Log[0], "llm miner failed") {
		t.Error("miner failure must be logged")
	}
}

var errMineFailed = &mineError{"stub failure"}

type mineError struct{ msg string }

func (e *mineError) Error() string { return e.msg }

func TestMine_InvalidFractionsFallback(t *testing.T) {
	digests := []SessionDigest{makeDigest("sess", 10, nil)}
	res := Mine(digests, MineOpts{TrainFraction: 0.5, ValFraction: 0.2, TestFraction: 0.2})
	fallbackLogged := false
	for _, ln := range res.Log {
		if strings.Contains(ln, "invalid fractions") {
			fallbackLogged = true
		}
	}
	if !fallbackLogged {
		t.Error("invalid fractions must be logged")
	}
	if countSplit(res.Tasks, "test") != 0 {
		t.Error("fallback is the legacy no-test split")
	}
}

func TestMine_DuplicateTaskIDFailsInvariant(t *testing.T) {
	// The LLM miner path can return duplicate IDs; the exactly-once
	// invariant must nil the whole batch (CodeRabbit).
	stub := func([]SessionDigest) ([]skill.MarkdownTask, error) {
		return []skill.MarkdownTask{
			{ID: "dup", Intent: "one", Origin: "real"},
			{ID: "dup", Intent: "two", Origin: "real"}, // same split after assignment
		}, nil
	}
	res := Mine(nil, MineOpts{LLMMiner: stub})
	if res.Tasks != nil {
		t.Errorf("duplicate IDs must fail the invariant, got %d tasks", len(res.Tasks))
	}
	invariantLogged := false
	for _, ln := range res.Log {
		if strings.Contains(ln, "INVARIANT VIOLATION") {
			invariantLogged = true
		}
	}
	if !invariantLogged {
		t.Error("invariant violation must be logged")
	}
	// A distinct-ID batch still ships.
	ok := func([]SessionDigest) ([]skill.MarkdownTask, error) {
		return []skill.MarkdownTask{
			{ID: "a", Intent: "one", Origin: "real"},
			{ID: "b", Intent: "two", Origin: "real"},
		}, nil
	}
	if res := Mine(nil, MineOpts{LLMMiner: ok}); len(res.Tasks) != 2 {
		t.Errorf("distinct IDs must ship, got %d tasks", len(res.Tasks))
	}
}

func TestMine_DefaultSplitLogMatchesBehavior(t *testing.T) {
	// CodeRabbit: the logged fractions must equal the effective split. The
	// legacy defaults are 0.8/0.2/0 (train takes everything val does not).
	digests := []SessionDigest{makeDigest("sess", 10, nil)}
	res := Mine(digests, MineOpts{})
	for _, ln := range res.Log {
		if strings.Contains(ln, "split: 10 real-origin tasks") {
			if !strings.Contains(ln, "0.80/0.20/0.00") {
				t.Errorf("split log misreports the default fractions: %s", ln)
			}
			return
		}
	}
	t.Error("split decision must be logged")
}

func TestWriteMineTasks_TripsM4Gate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	res := Mine([]SessionDigest{makeDigest("sess", 4, nil)}, MineOpts{})
	if err := WriteMineTasks(path, res.Tasks, "2026-09-13T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mined file mode = %o, want 600", info.Mode().Perm())
		}
	}
	// The machine marker is what makes real-backend consumers refuse.
	if err := RequireReviewed(path); err == nil {
		t.Error("mined tasks must require review before real-backend use")
	}
	tasks, err := LoadMarkdownTasks(path)
	if err != nil {
		t.Fatalf("round-trip through the standard loader: %v", err)
	}
	if len(tasks) != len(res.Tasks) {
		t.Errorf("round-trip tasks = %d, want %d", len(tasks), len(res.Tasks))
	}
}
