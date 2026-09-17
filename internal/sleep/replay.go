// replay.go - offline task replay and rule judging for markdown-skill
// evolution (plan Phase 1, SL-011).
//
// Two replay modes share the scoring core:
//   - Single-shot (default): one model prompt per task with the skill body
//     injected; no tools, no Runner. Cheap and headless-safe.
//   - Agentic (opt-in, replay_agent.go): an isolated llmagent+runner with
//     read-only tools for skills whose value is tool behavior.
//
// Scoring is exact | rule first; rubric judging is last-resort and needs an
// explicitly wired judge (never silent). Tasks with no checkable signal
// score 0 with an explanatory reason: the gate must see honesty, not hope.
package sleep

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"amurru/hakase/internal/skill"
)

// ModelCaller sends one prompt to a model and returns text.
type ModelCaller func(ctx context.Context, prompt string) (string, error)

// TargetRunner replays one task under a skill body. It returns the final
// response text plus the names of tools the run actually called (empty for
// single-shot).
type TargetRunner func(ctx context.Context, skillBody string, task skill.MarkdownTask) (response string, toolsCalled []string, err error)

// RubricJudge scores a response against a task rubric via a model call.
type RubricJudge func(ctx context.Context, task skill.MarkdownTask, response string) (hard, soft float64, rationale string, err error)

// ScoredTask is one replayed task with its outcome.
type ScoredTask struct {
	Task        skill.MarkdownTask
	Hard        float64
	Soft        float64
	Response    string
	FailReason  string
	ToolsCalled []string
}

// ReplayOpts tunes a replay batch.
type ReplayOpts struct {
	// Timeout bounds one task replay. Zero uses DefaultReplayTimeout.
	Timeout time.Duration
}

// DefaultReplayTimeout bounds one task replay (plan exec_timeout).
const DefaultReplayTimeout = 120 * time.Second

// SingleShotTarget builds a TargetRunner over a ModelCaller: the skill body,
// intent, and context excerpt form one prompt; no tools run.
func SingleShotTarget(call ModelCaller) TargetRunner {
	return func(ctx context.Context, skillBody string, task skill.MarkdownTask) (string, []string, error) {
		var b strings.Builder
		b.WriteString("Follow this skill document to complete the task.\n\n")
		b.WriteString("## Skill\n" + skillBody + "\n\n")
		b.WriteString("## Task\n" + task.Intent + "\n")
		if task.ContextExcerpt != "" {
			b.WriteString("\n## Context\n" + task.ContextExcerpt + "\n")
		}
		resp, err := call(ctx, b.String())
		if err != nil {
			return "", nil, err
		}
		return resp, nil, nil
	}
}

// ScoreExact scores reference equality after trimming space. An empty
// reference is unscorable and fails closed.
func ScoreExact(task skill.MarkdownTask, response string) (hard, soft float64, reason string) {
	if strings.TrimSpace(task.Reference) == "" {
		return 0, 0, "empty exact reference"
	}
	if strings.TrimSpace(response) == strings.TrimSpace(task.Reference) {
		return 1, 1, ""
	}
	return 0, 0, "exact mismatch"
}

// ScoreRules evaluates rule checks: all must pass for hard=1; soft is the
// pass fraction. A rule with no checks fails closed (vacuous truth would
// inflate the gate).
func ScoreRules(task skill.MarkdownTask, response string, toolsCalled []string) (hard, soft float64, reason string, failed []string) {
	checks := task.Judge.Checks
	if len(checks) == 0 {
		return 0, 0, "empty rule: no checks", nil
	}
	called := make(map[string]bool, len(toolsCalled))
	for _, t := range toolsCalled {
		called[t] = true
	}
	passed := 0
	for i, c := range checks {
		label := c.Op
		if label == "" {
			label = fmt.Sprintf("check[%d]", i)
		}
		if checkPasses(c, response, called) {
			passed++
			continue
		}
		failed = append(failed, label)
	}
	soft = float64(passed) / float64(len(checks))
	if passed == len(checks) {
		return 1, soft, "", nil
	}
	return 0, soft, fmt.Sprintf("failed checks: %s", strings.Join(failed, ", ")), failed
}

// checkPasses evaluates one rule check. Text matching is case-insensitive;
// section_contains matches literally inside ATX headings only (never bold
// lines, labels, or body text); tool_called needs the exact tool name.
func checkPasses(c skill.JudgeCheck, response string, called map[string]bool) bool {
	switch strings.ToLower(strings.TrimSpace(c.Op)) {
	case "contains":
		if c.Text == "" {
			return false
		}
		return strings.Contains(strings.ToLower(response), strings.ToLower(c.Text))
	case "section_contains":
		if c.Section == "" {
			return false
		}
		return headingContains(response, c.Section)
	case "tool_called":
		if c.Tool == "" {
			return false
		}
		return called[c.Tool]
	default:
		return false
	}
}

// headingContains reports whether any ATX heading line contains section
// (case-insensitive). Only lines starting with 1-6 '#' followed by a space
// count; everything else (bold, labels, body) is ignored by design.
func headingContains(response, section string) bool {
	want := strings.ToLower(strings.TrimSpace(section))
	if want == "" {
		return false
	}
	for _, line := range strings.Split(response, "\n") {
		trimmed := strings.TrimSpace(line)
		hashes := 0
		for hashes < len(trimmed) && trimmed[hashes] == '#' {
			hashes++
		}
		if hashes == 0 || hashes > 6 {
			continue
		}
		rest := trimmed[hashes:]
		if !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
			continue
		}
		if strings.Contains(strings.ToLower(strings.TrimSpace(rest)), want) {
			return true
		}
	}
	return false
}

// BuildRubricPrompt renders the rubric-judge prompt for wiring or tests.
func BuildRubricPrompt(task skill.MarkdownTask, response string) string {
	return "You are a strict judge. Score the response against the rubric.\n\n" +
		"## Task\n" + task.Intent + "\n\n" +
		"## Rubric\n" + task.Reference + "\n\n" +
		"## Response\n" + response + "\n\n" +
		`Reply with ONLY a JSON object: {"hard": 0 or 1, "soft": 0.0-1.0, "rationale": "one sentence"}.`
}

// ParseRubricReply parses a rubric-judge reply: a ```json fenced object if
// present, else the raw reply. Scores clamp to range; hard thresholds at 0.5.
func ParseRubricReply(raw string) (hard, soft float64, rationale string, err error) {
	doc := raw
	if start := strings.Index(doc, "```"); start != -1 {
		rest := doc[start+3:]
		if nl := strings.Index(rest, "\n"); nl != -1 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end != -1 {
			doc = rest[:end]
		}
	}
	var parsed struct {
		Hard      float64 `json:"hard"`
		Soft      float64 `json:"soft"`
		Rationale string  `json:"rationale"`
	}
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(doc)))
	if derr := dec.Decode(&parsed); derr != nil {
		return 0, 0, "", fmt.Errorf("rubric reply did not parse: %w", derr)
	}
	if parsed.Hard >= 0.5 {
		hard = 1
	}
	soft = parsed.Soft
	if soft < 0 {
		soft = 0
	}
	if soft > 1 {
		soft = 1
	}
	return hard, soft, parsed.Rationale, nil
}

// DefaultRubricJudge builds a RubricJudge over a ModelCaller.
func DefaultRubricJudge(call ModelCaller) RubricJudge {
	return func(ctx context.Context, task skill.MarkdownTask, response string) (float64, float64, string, error) {
		raw, err := call(ctx, BuildRubricPrompt(task, response))
		if err != nil {
			return 0, 0, "", err
		}
		return ParseRubricReply(raw)
	}
}

// ScoreTask dispatches on reference kind and scores one replay. Rubric kind
// without a judge fails closed (never silently skipped).
func ScoreTask(ctx context.Context, task skill.MarkdownTask, response string, toolsCalled []string, rubric RubricJudge) ScoredTask {
	st := ScoredTask{Task: task, Response: response, ToolsCalled: toolsCalled}
	switch strings.ToLower(strings.TrimSpace(task.ReferenceKind)) {
	case "exact":
		st.Hard, st.Soft, st.FailReason = ScoreExact(task, response)
	case "rule":
		var failed []string
		st.Hard, st.Soft, st.FailReason, failed = ScoreRules(task, response, toolsCalled)
		_ = failed
	case "rubric":
		if rubric == nil {
			st.FailReason = "rubric judge unavailable"
			break
		}
		hard, soft, rationale, err := rubric(ctx, task, response)
		if err != nil {
			st.FailReason = "rubric judge failed: " + err.Error()
			break
		}
		st.Hard, st.Soft = hard, soft
		if hard < 1 {
			st.FailReason = rationale
		}
	default:
		st.FailReason = "no checkable signal (reference_kind " + task.ReferenceKind + ")"
	}
	return st
}

// ReplayBatch replays every task under skillBody in order (deterministic),
// scoring each with rubric when needed. A runner error scores 0 with the
// error as fail reason so a dead backend self-diagnoses instead of passing.
func ReplayBatch(ctx context.Context, run TargetRunner, skillBody string, tasks []skill.MarkdownTask, rubric RubricJudge, opts ReplayOpts) []ScoredTask {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultReplayTimeout
	}
	out := make([]ScoredTask, 0, len(tasks))
	for _, task := range tasks {
		tctx, cancel := context.WithTimeout(ctx, timeout)
		resp, called, err := run(tctx, skillBody, task)
		cancel()
		if err != nil {
			out = append(out, ScoredTask{Task: task, FailReason: "replay error: " + err.Error()})
			continue
		}
		out = append(out, ScoreTask(ctx, task, resp, called, rubric))
	}
	return out
}

// AggregateScores means hard/soft over scored tasks. Empty input scores 0.
func AggregateScores(scored []ScoredTask) (hard, soft float64) {
	if len(scored) == 0 {
		return 0, 0
	}
	for _, s := range scored {
		hard += s.Hard
		soft += s.Soft
	}
	return hard / float64(len(scored)), soft / float64(len(scored))
}
