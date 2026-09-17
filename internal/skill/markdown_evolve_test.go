// markdown_evolve_test.go - SL-012 acceptance: prompt, parse, freeze.
package skill

import (
	"strings"
	"testing"
)

func TestBuildMarkdownMutationPrompt(t *testing.T) {
	failures := []MarkdownFailure{{
		Task:   MarkdownTask{ID: "t1", Intent: "Report in meters", ContextExcerpt: "field notes"},
		Actual: "reported in feet",
	}}
	prompt := BuildMarkdownMutationPrompt("units", sleepTestDoc, failures, 4, "")
	for _, want := range []string{"units", "Report in meters", "field notes", "reported in feet", "at most 4 edits", "```json"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "Optimizer memory") {
		t.Errorf("empty memory must not add the memory section: %s", prompt)
	}
	memorized := BuildMarkdownMutationPrompt("units", sleepTestDoc, failures, 4, "Trend over recent nights: improving.")
	if !strings.Contains(memorized, "Optimizer memory") || !strings.Contains(memorized, "Trend over recent nights: improving.") {
		t.Errorf("optimizer memory section missing: %s", memorized)
	}
	if strings.Contains(memorized, "```markdown\n## Optimizer memory") {
		t.Errorf("memory must sit outside the skill markdown fence: %s", memorized)
	}

	secret := []MarkdownFailure{{
		Task:   MarkdownTask{ID: "s", Intent: "use key ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD now"},
		Actual: "leaked",
	}}
	secretPrompt := BuildMarkdownMutationPrompt("s", sleepTestDoc, secret, 4, "")
	if strings.Contains(secretPrompt, "ghp_") {
		t.Errorf("secret survived in prompt: %s", secretPrompt)
	}
}

func TestBuildSkillAwareMutationPrompt(t *testing.T) {
	prompt := BuildSkillAwareMutationPrompt("units", sleepTestDoc, nil, 4, "")
	for _, want := range []string{"skill_defect", "execution_lapse", "route"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("skill-aware prompt missing %q", want)
		}
	}
}

func TestParseMarkdownEdits(t *testing.T) {
	raw := "Here:\n```json\n[{\"op\": \"add\", \"content\": \"Rule.\", \"rationale\": \"why\"}]\n```\nbye"
	edits, ok := ParseMarkdownEdits(raw)
	if !ok || len(edits) != 1 || edits[0].Op != "add" || edits[0].Content != "Rule." {
		t.Fatalf("fenced parse: %+v %v", edits, ok)
	}

	// Raw JSON without fences.
	edits, ok = ParseMarkdownEdits(`[{"op":"DELETE","anchor":"x"}]`)
	if !ok || len(edits) != 1 || edits[0].Op != "delete" {
		t.Fatalf("raw parse with op normalization: %+v %v", edits, ok)
	}

	// Explicit empty array: ok, no edits.
	edits, ok = ParseMarkdownEdits("```json\n[]\n```")
	if !ok || len(edits) != 0 {
		t.Fatalf("empty array: %+v %v", edits, ok)
	}

	// Prose without JSON: no-op.
	if _, ok := ParseMarkdownEdits("I have no changes to suggest."); ok {
		t.Error("prose must not parse")
	}
	if _, ok := ParseMarkdownEdits(""); ok {
		t.Error("empty reply must not parse")
	}
}

func TestCheckFrontmatterFrozen(t *testing.T) {
	if err := CheckFrontmatterFrozen(sleepTestDoc, sleepTestDoc); err != nil {
		t.Fatalf("identical docs: %v", err)
	}
	changed := strings.Replace(sleepTestDoc, "description: Demo skill.", "description: Hijacked.", 1)
	if err := CheckFrontmatterFrozen(sleepTestDoc, changed); err == nil {
		t.Error("changed frontmatter must fail")
	}
	stripped := strings.Replace(sleepTestDoc, "---\nname: demo\ndescription: Demo skill.\n---\n", "", 1)
	if err := CheckFrontmatterFrozen(sleepTestDoc, stripped); err == nil {
		t.Error("lost frontmatter must fail")
	}
}
