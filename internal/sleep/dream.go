// dream.go - dream rollouts (plan Phase 3, SL-033): contrastive synthetic
// training tasks distilled from real train tasks by the model. Dream tasks
// carry origin=dream, so the existing quarantine (mine.go finalize and
// consolidate SplitTasks) pins them to the train slice: they can sharpen
// reflection but never validate, and they never enter the write-only test
// slice. Defaults stay off (dream_factor 0).
package sleep

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"amurru/hakase/internal/skill"
)

// DefaultMaxDreamTasks caps synthesized tasks per skill per night; the
// night report shows the count (cost accounting, plan SL-033 exit).
const DefaultMaxDreamTasks = 10

// MaxDreamExamplesInPrompt bounds the real examples shown to the dreamer.
const MaxDreamExamplesInPrompt = 6

// DreamMiner synthesizes contrastive training tasks for a skill group.
// Implementations must force Origin="dream" and leave Split empty (the
// quarantine assigns train).
type DreamMiner func(ctx context.Context, skillName string, tasks []skill.MarkdownTask, factor float64, cap int) ([]skill.MarkdownTask, error)

// BuildDreamPrompt renders the dreamer prompt for wiring or tests.
func BuildDreamPrompt(skillName string, tasks []skill.MarkdownTask, count int) string {
	var b strings.Builder
	b.WriteString("You generate synthetic training tasks (\"dreams\") for a markdown skill's optimizer.\n\n")
	fmt.Fprintf(&b, "Skill: %s\n\n", skillName)
	b.WriteString("From the real task examples below, derive contrastive variants: same skill area,\n")
	b.WriteString("but stressing the opposite failure mode or an edge case the examples only imply.\n")
	b.WriteString("A variant must be answerable from the skill's guidance alone and must NOT copy an example verbatim.\n\n")
	b.WriteString("Real task examples:\n")
	for i, t := range tasks {
		fmt.Fprintf(&b, "\nExample %d:\n  intent: %s\n", i+1, t.Intent)
		if t.ContextExcerpt != "" {
			head := t.ContextExcerpt
			if len(head) > 400 {
				head = head[:400]
			}
			fmt.Fprintf(&b, "  prior outcome (excerpt): %s\n", head)
		}
	}
	fmt.Fprintf(&b, "\nGenerate %d dream tasks. Reply with ONLY a JSON array (or {\"tasks\": [...]}) of objects:\n", count)
	b.WriteString(`{"intent": "...", "reference_kind": "rubric", "reference": "what a good answer does", "tags": ["outcome:fail"]}.`)
	return b.String()
}

// ParseDreamTasks parses a dreamer reply ({\"tasks\":[...]} or a bare array),
// forcing the dream quarantine invariants: origin=dream, split cleared,
// deterministic IDs (a model-invented ID must never collide with a real one
// or with another dream), and rubric tasks flagged needs_review.
func ParseDreamTasks(raw string, skillName string) []skill.MarkdownTask {
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
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return nil
	}
	var parsed []skill.MarkdownTask
	var wrapped struct {
		Tasks []skill.MarkdownTask `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(doc), &wrapped); err == nil && wrapped.Tasks != nil {
		parsed = wrapped.Tasks
	} else if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		return nil
	}
	out := make([]skill.MarkdownTask, 0, len(parsed))
	for _, t := range parsed {
		if strings.TrimSpace(t.Intent) == "" {
			continue
		}
		// Quarantine invariants (SL-010/SL-021): dreams train only.
		t.Origin = "dream"
		t.Split = ""
		if strings.EqualFold(strings.TrimSpace(t.ReferenceKind), "rubric") {
			t.Tags = append(t.Tags, needsReviewTag)
		}
		t.ID = minedTaskID("dream:"+skillName, len(out), t.Intent)
		out = append(out, t)
	}
	return out
}

// DefaultDreamMiner builds a DreamMiner over a model caller (routed through
// the night's token ledger by the cycle, so dream spend is accounted).
func DefaultDreamMiner(call ModelCaller) DreamMiner {
	return func(ctx context.Context, skillName string, tasks []skill.MarkdownTask, factor float64, cap int) ([]skill.MarkdownTask, error) {
		if factor <= 0 || len(tasks) == 0 {
			return nil, nil
		}
		count := int(float64(len(tasks))*factor + 0.5)
		if count < 1 {
			count = 1
		}
		if cap > 0 && count > cap {
			count = cap
		}
		if len(tasks) > MaxDreamExamplesInPrompt {
			tasks = tasks[:MaxDreamExamplesInPrompt]
		}
		raw, err := call(ctx, BuildDreamPrompt(skillName, tasks, count))
		if err != nil {
			return nil, err
		}
		tasks_out := ParseDreamTasks(raw, skillName)
		if len(tasks_out) > count {
			tasks_out = tasks_out[:count]
		}
		return tasks_out, nil
	}
}
