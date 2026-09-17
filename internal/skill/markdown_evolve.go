// markdown_evolve.go - mutator side of markdown-skill evolution (plan
// Phase 1, SL-012): render the reflect prompt over train failures, parse
// the optimizer's bounded-edit reply, and enforce the frontmatter freeze.
//
// The mutator proposes add/delete/replace/insert_after edits against the
// learned block only (see applyEdits): it can sharpen the skill's learned
// procedures but never rewrite hand-written guidance or frontmatter.
package skill

import (
	"encoding/json"
	"fmt"
	"strings"

	"amurru/hakase/internal/util"
)

// MarkdownFailure is one train failure shown to the mutator: the task that
// failed plus what the target produced.
type MarkdownFailure struct {
	Task   MarkdownTask
	Actual string
	Error  string
}

// BuildMarkdownMutationPrompt renders the reflector prompt: skill body plus
// a bounded sample of train failures, requesting a JSON edit array. All
// failure fields are redacted before leaving the process (plan SL-004).
// optimizerMemory is the meta-skill sidecar text (plan SL-031): longitudinal
// optimizer guidance that is never part of the skill document; empty omits
// the section.
func BuildMarkdownMutationPrompt(skillName, body string, failures []MarkdownFailure, editBudget int, optimizerMemory string) string {
	var b strings.Builder
	b.WriteString("You are improving a saved markdown skill for an AI agent.\n\n")
	if s := strings.TrimSpace(optimizerMemory); s != "" {
		b.WriteString("## Optimizer memory (longitudinal guidance for YOU, never part of the skill)\n\n")
		b.WriteString(s + "\n\n")
	}
	b.WriteString(fmt.Sprintf("Skill name: %s\n\nCurrent skill document:\n```markdown\n%s\n```\n\n", skillName, body))
	if len(failures) > 0 {
		b.WriteString("The skill fails these evaluation tasks when its guidance is followed:\n")
		for i, f := range failures {
			intent, _ := util.RedactSecrets(f.Task.Intent)
			ctxExcerpt, _ := util.RedactSecrets(f.Task.ContextExcerpt)
			expected, _ := util.RedactSecrets(taskExpected(f.Task))
			actual, _ := util.RedactSecrets(f.Actual)
			b.WriteString(fmt.Sprintf("\nTask %d: %s\n", i+1, f.Task.ID))
			b.WriteString(fmt.Sprintf("  intent:   %s\n", intent))
			if ctxExcerpt != "" {
				b.WriteString(fmt.Sprintf("  context:  %s\n", ctxExcerpt))
			}
			b.WriteString(fmt.Sprintf("  expected: %s\n", expected))
			b.WriteString(fmt.Sprintf("  actual:   %s\n", actual))
			if f.Error != "" {
				redactedErr, _ := util.RedactSecrets(f.Error)
				b.WriteString(fmt.Sprintf("  error:    %s\n", redactedErr))
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Propose at most %d edits that would fix these failures for future tasks. ", editBudget)
	b.WriteString("Edits apply to a learned-procedures block appended to the document; ")
	b.WriteString("you cannot rewrite the hand-written guidance above, only sharpen what the skill has learned.\n")
	b.WriteString("Ops: `add` appends a learned bullet (content required); `delete` removes learned bullets containing anchor; ")
	b.WriteString("`replace` rewrites learned bullets containing anchor with content; `insert_after` inserts content after the learned bullet containing anchor.\n")
	b.WriteString("Output ONLY a single ```json code block containing the JSON array of edits, no prose. ")
	b.WriteString("Each edit: {\"op\": \"add|delete|replace|insert_after\", \"content\": \"...\", \"anchor\": \"...\", \"rationale\": \"...\"}.")
	return b.String()
}

// BuildSkillAwareMutationPrompt extends the reflector prompt with
// skill-aware reflection routing (plan Phase 3, SL-032): the optimizer
// classifies each failure as SKILL_DEFECT (the document lacks or misstates
// guidance -> a gated learned-block edit) or EXECUTION_LAPSE (the guidance
// existed but was not followed -> an ungated protected-appendix reminder).
// Patch-only is enforced structurally: the reply is still a bounded edit
// array, never a rewrite.
func BuildSkillAwareMutationPrompt(skillName, body string, failures []MarkdownFailure, editBudget int, optimizerMemory string) string {
	prompt := BuildMarkdownMutationPrompt(skillName, body, failures, editBudget, optimizerMemory)
	prompt += "\n\nAdditionally, classify each failure you address as one of:\n" +
		"- `skill_defect`: the document's guidance is missing or wrong for this task. Propose the edit against the learned block as usual.\n" +
		"- `execution_lapse`: the guidance already covered it but was not followed. Propose a short `add` edit with \"route\": \"execution_lapse\"; it lands in the protected appendix as a reminder and is NOT validation-gated.\n" +
		"Each edit may carry an optional \"route\": \"skill_defect\" (default) or \"execution_lapse\". Patch-only: never propose rewrites."
	return prompt
}

// taskExpected renders what a task expects for the prompt: the exact
// reference, the rule checks, or the rubric.
func taskExpected(t MarkdownTask) string {
	switch strings.ToLower(strings.TrimSpace(t.ReferenceKind)) {
	case "exact", "rubric":
		return t.Reference
	case "rule":
		var parts []string
		for _, c := range t.Judge.Checks {
			switch strings.ToLower(strings.TrimSpace(c.Op)) {
			case "contains":
				parts = append(parts, "response contains "+fmt.Sprintf("%q", c.Text))
			case "section_contains":
				parts = append(parts, "heading contains "+fmt.Sprintf("%q", c.Section))
			case "tool_called":
				parts = append(parts, "tool "+fmt.Sprintf("%q", c.Tool)+" called")
			default:
				parts = append(parts, c.Op)
			}
		}
		return strings.Join(parts, "; ")
	default:
		return t.Reference
	}
}

// ParseMarkdownEdits extracts the JSON edit array from a mutator reply: the
// first ```json (or bare ```) fenced block, falling back to the raw reply.
// It returns ok=false when no parseable array exists (optimizer no-op).
// A valid but empty array returns ok=true with no edits (explicit "no
// changes needed"). Op-level validation happens in applyEdits, which files
// unknown ops/shapes as unmatched rather than failing the batch.
func ParseMarkdownEdits(raw string) (edits []TextEdit, ok bool) {
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
		return nil, false
	}
	var parsed []TextEdit
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		return nil, false
	}
	for i := range parsed {
		parsed[i].Op = strings.ToLower(strings.TrimSpace(parsed[i].Op))
		parsed[i].Target = strings.ToLower(strings.TrimSpace(parsed[i].Target))
		parsed[i].Route = NormalizeEditRoute(parsed[i].Route)
	}
	return parsed, true
}

// CheckFrontmatterFrozen verifies a candidate document keeps the original
// frontmatter byte-identical (plan SL-012). The applier preserves it by
// construction; this is the defense-in-depth assertion at the consolidate
// and adopt boundaries.
func CheckFrontmatterFrozen(before, after string) error {
	beforeFront, _, ok := SplitSkillDoc(before)
	if !ok {
		return fmt.Errorf("original document has no frontmatter")
	}
	afterFront, _, ok := SplitSkillDoc(after)
	if !ok {
		return fmt.Errorf("candidate lost its frontmatter")
	}
	if beforeFront != afterFront {
		return fmt.Errorf("candidate changed frontmatter (frozen)")
	}
	return nil
}
