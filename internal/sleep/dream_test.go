// dream_test.go - SL-033 acceptance: dream tasks are forced into the
// dream quarantine invariants and the miner caps its output.
package sleep

import (
	"context"
	"strings"
	"testing"

	"amurru/hakase/internal/skill"
)

func TestParseDreamTasksForcesQuarantine(t *testing.T) {
	raw := "```json\n[{\"id\":\"model-invented\",\"intent\":\"variant task\",\"reference_kind\":\"rubric\",\"reference\":\"r\",\"split\":\"val\",\"origin\":\"real\"}]\n```"
	tasks := ParseDreamTasks(raw, "demo")
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	dt := tasks[0]
	if dt.Origin != "dream" {
		t.Errorf("origin = %q, want dream (quarantine)", dt.Origin)
	}
	if dt.Split != "" {
		t.Errorf("split must be cleared for quarantine assignment, got %q", dt.Split)
	}
	if !strings.HasPrefix(dt.ID, "m-") || dt.ID == "model-invented" {
		t.Errorf("IDs must be deterministic content hashes, got %q", dt.ID)
	}
	for _, tag := range dt.Tags {
		if tag == needsReviewTag {
			return
		}
	}
	t.Error("rubric dreams must carry needs_review")
}

func TestParseDreamTasksRejectsEmptyIntents(t *testing.T) {
	tasks := ParseDreamTasks(`[{"intent":"  "},{"intent":"real one"}]`, "demo")
	if len(tasks) != 1 || tasks[0].Intent != "real one" {
		t.Errorf("tasks = %+v", tasks)
	}
	if tasks := ParseDreamTasks("not json at all", "demo"); tasks != nil {
		t.Errorf("prose must not parse: %+v", tasks)
	}
}

func TestDefaultDreamMinerCountsAndCaps(t *testing.T) {
	var gotCount int
	call := func(_ context.Context, prompt string) (string, error) {
		gotCount++
		if !strings.Contains(prompt, "contrastive") {
			t.Errorf("dream prompt missing contrastive framing: %s", prompt)
		}
		// Lie about availability: return 99 tasks; the miner must cap.
		var b strings.Builder
		b.WriteString("[")
		for i := 0; i < 99; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"intent":"dream variant ` + string(rune('a'+i%26)) + `","reference_kind":"rubric"}`)
		}
		b.WriteString("]")
		return b.String(), nil
	}
	_ = gotCount
	tasks := []skill.MarkdownTask{
		{ID: "a", Intent: "task a", Split: "train"},
		{ID: "b", Intent: "task b", Split: "train"},
	}
	// factor 100 over 2 train tasks would want 200; the cap bounds it to 10.
	miner := DefaultDreamMiner(call)
	got, err := miner(context.Background(), "demo", tasks, 100, DefaultMaxDreamTasks)
	if err != nil {
		t.Fatalf("miner: %v", err)
	}
	if len(got) != DefaultMaxDreamTasks {
		t.Errorf("miner produced %d tasks, want capped at %d", len(got), DefaultMaxDreamTasks)
	}
	for _, d := range got {
		if d.Origin != "dream" {
			t.Fatalf("dream task lost its origin: %+v", d)
		}
	}
	// Zero factor never calls the model.
	called := false
	quiet := func(_ context.Context, _ string) (string, error) { called = true; return "[]", nil }
	if got, err := DefaultDreamMiner(quiet)(context.Background(), "demo", tasks, 0, 10); err != nil || got != nil || called {
		t.Errorf("zero factor must be inert: got=%v err=%v called=%v", got, err, called)
	}
}

func TestBuildDreamPrompt(t *testing.T) {
	prompt := BuildDreamPrompt("demo", []skill.MarkdownTask{{Intent: "deploy the service"}}, 3)
	for _, want := range []string{"deploy the service", "3 dream tasks", "Skill: demo"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("dream prompt missing %q", want)
		}
	}
}
