// replay_test.go - SL-011 acceptance: judges, dispatch, batch, allowlist.
package sleep

import (
	"context"
	"iter"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/skill"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestHeadingContains(t *testing.T) {
	resp := "# Title\n\nSome **Usage** bold.\n\n## 1. Usage Guide\n\nlabel: Usage\n\nBody mentions usage here."
	if !headingContains(resp, "usage") {
		t.Error("numbered ATX heading must match case-insensitively")
	}
	if !headingContains(resp, "Usage Guide") {
		t.Error("literal heading text must match")
	}
	boldOnly := "Some **Usage** bold.\nlabel: Usage\nBody usage."
	if headingContains(boldOnly, "usage") {
		t.Error("bold/label/body text must never count as a heading")
	}
	if headingContains("####### too deep", "deep") {
		t.Error("7-hash line is not a heading")
	}
	if headingContains("#NoSpace", "nospace") {
		t.Error("# without trailing space is not a heading")
	}
	if headingContains("anything", "") {
		t.Error("empty section must not match")
	}
}

func TestCheckPasses_Matrix(t *testing.T) {
	resp := "## Usage\n\nRun make test please."
	called := map[string]bool{"read_file": true}
	cases := []struct {
		name  string
		check skill.JudgeCheck
		want  bool
	}{
		{"contains ci", skill.JudgeCheck{Op: "contains", Text: "MAKE TEST"}, true},
		{"contains miss", skill.JudgeCheck{Op: "contains", Text: "deploy"}, false},
		{"contains empty", skill.JudgeCheck{Op: "contains"}, false},
		{"section hit", skill.JudgeCheck{Op: "section_contains", Section: "usage"}, true},
		{"section miss", skill.JudgeCheck{Op: "section_contains", Section: "deploy"}, false},
		{"tool hit", skill.JudgeCheck{Op: "tool_called", Tool: "read_file"}, true},
		{"tool miss", skill.JudgeCheck{Op: "tool_called", Tool: "write_file"}, false},
		{"tool empty", skill.JudgeCheck{Op: "tool_called"}, false},
		{"unknown op", skill.JudgeCheck{Op: "teleport"}, false},
		{"empty op", skill.JudgeCheck{}, false},
	}
	for _, c := range cases {
		if got := checkPasses(c.check, resp, called); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestScoreRules_FractionAndEmpty(t *testing.T) {
	task := skill.MarkdownTask{ReferenceKind: "rule", Judge: skill.TaskJudge{Checks: []skill.JudgeCheck{
		{Op: "contains", Text: "alpha"},
		{Op: "contains", Text: "beta"},
	}}}
	hard, soft, reason, failed := ScoreRules(task, "alpha only", nil)
	if hard != 0 || soft != 0.5 || len(failed) != 1 {
		t.Errorf("hard=%v soft=%v failed=%v reason=%q", hard, soft, failed, reason)
	}
	hard, soft, _, _ = ScoreRules(task, "alpha and beta", nil)
	if hard != 1 || soft != 1 {
		t.Errorf("all-pass must score 1/1: %v/%v", hard, soft)
	}
	empty := skill.MarkdownTask{ReferenceKind: "rule"}
	if hard, _, _, _ := ScoreRules(empty, "anything", nil); hard != 0 {
		t.Error("empty rule must fail closed, not vacuous-pass")
	}
}

func TestScoreExact(t *testing.T) {
	task := skill.MarkdownTask{ReferenceKind: "exact", Reference: "  hello  "}
	if hard, soft, _ := ScoreExact(task, "hello"); hard != 1 || soft != 1 {
		t.Error("trimmed equality must pass")
	}
	if hard, _, _ := ScoreExact(task, "bye"); hard != 0 {
		t.Error("mismatch must fail")
	}
	if hard, _, _ := ScoreExact(skill.MarkdownTask{ReferenceKind: "exact"}, "x"); hard != 0 {
		t.Error("empty reference must fail closed")
	}
}

func TestParseRubricReply(t *testing.T) {
	hard, soft, rationale, err := ParseRubricReply("```json\n{\"hard\": 1, \"soft\": 0.5, \"rationale\": \"ok\"}\n```")
	if err != nil || hard != 1 || soft != 0.5 || rationale != "ok" {
		t.Errorf("fenced parse: %v %v %q %v", hard, soft, rationale, err)
	}
	hard, soft, _, err = ParseRubricReply(`{"hard": 0, "soft": 1.7, "rationale": "x"}`)
	if err != nil || hard != 0 || soft != 1 {
		t.Errorf("clamp: %v %v %v", hard, soft, err)
	}
	if _, _, _, err := ParseRubricReply("not json"); err == nil {
		t.Error("unparseable rubric must error")
	}
}

func TestScoreTask_Dispatch(t *testing.T) {
	ctx := context.Background()
	// rubric without a judge fails closed.
	st := ScoreTask(ctx, skill.MarkdownTask{ID: "r", ReferenceKind: "rubric", Reference: "rubric"}, "text", nil, nil)
	if st.Hard != 0 || !strings.Contains(st.FailReason, "unavailable") {
		t.Errorf("rubric without judge: %+v", st)
	}
	// none kind is honest about having no signal.
	st = ScoreTask(ctx, skill.MarkdownTask{ID: "n", ReferenceKind: "none"}, "text", nil, nil)
	if st.Hard != 0 || !strings.Contains(st.FailReason, "no checkable signal") {
		t.Errorf("none kind: %+v", st)
	}
	// rubric with a stub judge scores through.
	rubric := func(context.Context, skill.MarkdownTask, string) (float64, float64, string, error) {
		return 1, 0.8, "", nil
	}
	st = ScoreTask(ctx, skill.MarkdownTask{ID: "r2", ReferenceKind: "rubric"}, "text", nil, rubric)
	if st.Hard != 1 || st.Soft != 0.8 {
		t.Errorf("rubric judge: %+v", st)
	}
}

func TestReplayBatch_ErrorAndAggregate(t *testing.T) {
	ctx := context.Background()
	okRunner := func(context.Context, string, skill.MarkdownTask) (string, []string, error) {
		return "the answer is 42", nil, nil
	}
	failRunner := func(context.Context, string, skill.MarkdownTask) (string, []string, error) {
		return "", nil, context.DeadlineExceeded
	}
	tasks := []skill.MarkdownTask{
		{ID: "t1", Intent: "q", ReferenceKind: "exact", Reference: "the answer is 42", Split: "train"},
		{ID: "t2", Intent: "q", ReferenceKind: "exact", Reference: "other", Split: "train"},
	}
	scored := ReplayBatch(ctx, okRunner, "skill", tasks, nil, ReplayOpts{})
	if len(scored) != 2 || scored[0].Hard != 1 || scored[1].Hard != 0 {
		t.Fatalf("batch scored wrong: %+v", scored)
	}
	if h, s := AggregateScores(scored); h != 0.5 || s != 0.5 {
		t.Errorf("aggregate = %v/%v, want 0.5/0.5", h, s)
	}
	errScored := ReplayBatch(ctx, failRunner, "skill", tasks[:1], nil, ReplayOpts{})
	if errScored[0].Hard != 0 || !strings.Contains(errScored[0].FailReason, "replay error") {
		t.Errorf("runner error must score 0 with reason: %+v", errScored[0])
	}
	if h, s := AggregateScores(nil); h != 0 || s != 0 {
		t.Error("empty aggregate must be 0/0")
	}
}

func TestSingleShotTarget_PromptShape(t *testing.T) {
	var gotPrompt string
	call := func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "done", nil
	}
	run := SingleShotTarget(call)
	resp, called, err := run(context.Background(), "SKILL BODY", skill.MarkdownTask{Intent: "do it", ContextExcerpt: "ctx"})
	if err != nil || resp != "done" || len(called) != 0 {
		t.Fatalf("single-shot: %q %v %v", resp, called, err)
	}
	for _, want := range []string{"SKILL BODY", "do it", "ctx"} {
		if !strings.Contains(gotPrompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBuildReplayTools_Allowlist(t *testing.T) {
	old := sandbox.CurrentSandbox
	sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(nil)
	defer func() { sandbox.CurrentSandbox = old }()

	tools, err := buildReplayTools(nil)
	if err != nil {
		t.Fatalf("buildReplayTools: %v", err)
	}
	denied := []string{"write_file", "patch", "system_exec", "system_exec_start",
		"delegate_task", "clarify", "cronjob", "memory", "send_message",
		"download_file", "python_interpreter", "save_skill"}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name()] = true
	}
	for _, d := range denied {
		if names[d] {
			t.Errorf("denied tool %q in replay allowlist", d)
		}
	}
	if !names["read_file"] {
		t.Error("read_file must be present")
	}
	// Exhaustive: nothing outside the allowlist may attach.
	for n := range names {
		if !replayAllowTools[n] {
			t.Errorf("tool %q attached but not allowlisted", n)
		}
	}
}

func TestAgenticTarget_Guards(t *testing.T) {
	if _, err := AgenticTarget(AgenticOpts{}); err == nil {
		t.Error("nil model must fail closed")
	}
	old := sandbox.CurrentSandbox
	sandbox.CurrentSandbox = &sandbox.SandboxConfig{Mode: sandbox.SandboxModeOff}
	defer func() { sandbox.CurrentSandbox = old }()
	if _, err := AgenticTarget(AgenticOpts{Model: stubLLM{}}); err == nil {
		t.Error("sandbox-off must refuse agentic replay")
	}
}

// stubLLM is a canned-text model.LLM for offline agentic replay tests.
type stubLLM struct{ text string }

func (s stubLLM) Name() string { return "stub" }

func (s stubLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role:  genai.RoleModel,
				Parts: []*genai.Part{{Text: s.text}},
			},
		}, nil)
	}
}

func TestAgenticTarget_EndToEnd(t *testing.T) {
	old := sandbox.CurrentSandbox
	sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(nil)
	defer func() { sandbox.CurrentSandbox = old }()

	run, err := AgenticTarget(AgenticOpts{Model: stubLLM{text: "stub answer"}, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("AgenticTarget: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, called, err := run(ctx, "Follow the skill.", skill.MarkdownTask{ID: "t1", Intent: "say something"})
	if err != nil {
		t.Fatalf("agentic replay: %v", err)
	}
	if !strings.Contains(resp, "stub answer") {
		t.Errorf("response missing stub text: %q", resp)
	}
	if len(called) != 0 {
		t.Errorf("no tools should have run: %v", called)
	}
}
