// skill_evolve_md_test.go - SL-013 CLI acceptance: usage, resolution,
// and loud model-bootstrap failure. Full consolidation paths run with
// stubbed runners in internal/sleep (no model in unit tests).
package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/skill"
	"amurru/hakase/internal/sleep"
)

// evolveMDTestEnv builds a temp project with .agents/skills/demo/SKILL.md
// and a task file, chdir'd into it with an isolated HAKASE_HOME.
func evolveMDTestEnv(t *testing.T) (tasksPath string) {
	t.Helper()
	cronTestEnv(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(cwd, ".agents", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: demo\ndescription: Demo skill for evolve-md tests.\n---\n\n# Demo\n\nHand-written.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	tasksPath = filepath.Join(cwd, "tasks.json")
	tasks := `{"tasks": [
  {"id": "t1", "intent": "q", "reference_kind": "exact", "reference": "yes", "split": "train"},
  {"id": "v1", "intent": "q", "reference_kind": "exact", "reference": "yes", "split": "val"}
]}`
	if err := os.WriteFile(tasksPath, []byte(tasks), 0o600); err != nil {
		t.Fatal(err)
	}
	return tasksPath
}

func TestSkillEvolveMD_Usage(t *testing.T) {
	cronTestEnv(t)
	if code := runSkillEvolveMD([]string{}); code != 2 {
		t.Errorf("no args = %d, want 2", code)
	}
	if code := runSkillEvolveMD([]string{"--skill", "demo"}); code != 2 {
		t.Errorf("missing --tasks = %d, want 2", code)
	}
	if code := runSkillEvolveMD([]string{"--skill", "demo", "--tasks", "t.json", "--gate-metric", "bogus"}); code != 2 {
		t.Errorf("bad metric = %d, want 2", code)
	}
	if code := runSkillEvolveMD([]string{"--skill", "demo", "--tasks", "t.json", "--edit-budget", "0"}); code != 2 {
		t.Errorf("bad budget = %d, want 2", code)
	}
	if code := runSkillEvolveMD([]string{"extra", "--skill", "demo", "--tasks", "t.json"}); code != 2 {
		t.Errorf("positional = %d, want 2", code)
	}
	if code := runSkillEvolveMD([]string{"--skill", "demo", "--tasks", "t.json", "--adopt", "--report", "r.md"}); code != 2 {
		t.Errorf("adopt+report conflict = %d, want 2", code)
	}
}

func TestSkillEvolveMD_UnknownSkill(t *testing.T) {
	tasks := evolveMDTestEnv(t)
	if code := runSkillEvolveMD([]string{"--skill", "nope", "--tasks", tasks}); code != 1 {
		t.Errorf("unknown skill = %d, want 1", code)
	}
}

func TestSkillEvolveMD_RequiresModel(t *testing.T) {
	tasks := evolveMDTestEnv(t)
	for _, k := range []string{
		"HAKASE_API_KEY", "HAKASE_PROVIDER", "HAKASE_MODEL",
		"HAKASE_BASE_URL", "HAKASE_SUMMARY_MODEL",
	} {
		t.Setenv(k, "")
	}
	// Resolves fine, then fails loudly at bootstrap (no config reachable).
	if code := runSkillEvolveMD([]string{"--skill", "demo", "--tasks", tasks, "--dry-run"}); code != 1 {
		t.Errorf("no-model dry-run = %d, want 1", code)
	}
}

func TestSkillEvolveMD_BlockedWhenDisabled(t *testing.T) {
	tasks := evolveMDTestEnv(t)
	if err := skill.SetSkillDisabled(skill.KindMarkdown, "demo", true); err != nil {
		t.Fatal(err)
	}
	if code := runSkillEvolveMD([]string{"--skill", "demo", "--tasks", tasks}); code != 1 {
		t.Errorf("disabled skill = %d, want 1", code)
	}
}

func TestResolveEvolveMDSkill(t *testing.T) {
	evolveMDTestEnv(t)
	cwd, _ := os.Getwd()
	if resolveEvolveMDSkill(cwd, nil, "DEMO") == nil {
		t.Error("resolution must be case-insensitive")
	}
	if resolveEvolveMDSkill(cwd, nil, "missing") != nil {
		t.Error("missing skill must resolve nil")
	}
}

func TestEvolveMDReflector(t *testing.T) {
	ctx := context.Background()
	failures := []sleep.ScoredTask{{
		Task:       skill.MarkdownTask{ID: "t1", Intent: "q"},
		Hard:       0,
		Response:   "no",
		FailReason: "exact mismatch",
	}}

	var gotPrompt string
	call := func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "```json\n[{\"op\": \"add\", \"content\": \"Rule.\", \"rationale\": \"why\"}]\n```", nil
	}
	edits, raw, err := evolveMDReflector(call, "demo", 4)(ctx, failures, nil, "body", 4)
	if err != nil || len(edits) != 1 || raw == "" {
		t.Fatalf("reflector: %+v %q %v", edits, raw, err)
	}
	if !strings.Contains(gotPrompt, "demo") {
		t.Error("prompt must carry the skill name")
	}

	// Model errors propagate as CallError material.
	errCall := func(context.Context, string) (string, error) {
		return "", errors.New("boom")
	}
	if _, _, err := evolveMDReflector(errCall, "demo", 4)(ctx, failures, nil, "body", 4); err == nil {
		t.Error("model errors must propagate")
	}

	// Unparseable replies are no-ops, not errors.
	proseCall := func(context.Context, string) (string, error) {
		return "no changes needed, all good", nil
	}
	edits, _, err = evolveMDReflector(proseCall, "demo", 4)(ctx, failures, nil, "body", 4)
	if err != nil || len(edits) != 0 {
		t.Errorf("prose must be an empty no-op: %+v %v", edits, err)
	}
}
