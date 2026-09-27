package agent

import (
	"strings"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/skill"
)

// TestSkillsIndexStaysSmall guards the per-turn prefill cost of the skill
// index.
//
// The index is the single largest part of the orchestrator's system prompt,
// and the system prompt is the PREFIX of every request. At 148 skills the
// index was ~58 KB, dwarfing the ~10 KB of actual instructions, and the
// 4-line-per-entry format repeated the same "call load_markdown_skill ..."
// sentence 148 times.
func TestSkillsIndexStaysSmall(t *testing.T) {
	md := skill.DiscoverMarkdownSkills(".", nil, func(string) {})
	if len(md) == 0 {
		t.Skip("no markdown skills discovered")
	}
	block := getSkillsPrompt(md, func(string) {})

	// Budget scales with the number of skills so this does not fail every
	// time someone adds one, while still catching a format regression. The
	// old 4-line-per-entry format averaged ~395 bytes/skill.
	const perSkill = 300
	budget := len(md)*perSkill + 2048
	if len(block) > budget {
		t.Errorf("skills index is %d bytes for %d skills, over the %d budget (%.0f bytes/skill)",
			len(block), len(md), budget, float64(len(block))/float64(len(md)))
	}

	// Every skill must still be listed, or the model cannot choose it.
	for _, s := range md {
		if !strings.Contains(block, s.Frontmatter.Name) {
			t.Errorf("skill %q missing from the index", s.Frontmatter.Name)
		}
	}
}

// TestSkillsIndexStatesTheLoadInstructionOnce pins that the loading
// instructions are hoisted rather than repeated per entry.
func TestSkillsIndexStatesTheLoadInstructionOnce(t *testing.T) {
	md := skill.DiscoverMarkdownSkills(".", nil, func(string) {})
	if len(md) == 0 {
		t.Skip("no markdown skills discovered")
	}
	block := getSkillsPrompt(md, func(string) {})

	if n := strings.Count(block, "load_markdown_skill"); n > 1 {
		t.Errorf("load_markdown_skill mentioned %d times, want once in the header", n)
	}
	// The per-entry absolute path is dead weight: the model loads by name.
	if strings.Contains(block, "Location:") {
		t.Error("index still emits a Location line; skills are loaded by name")
	}
}

// TestOrchestratorInstructionBudgetWithRealSkills is the end-to-end number:
// what one turn actually costs before the model is called.
//
// The budget guards against the format regressing, not a target to hit. It
// was ~69 KB before the index was compacted; the remaining bulk is the skill
// descriptions themselves plus the per-entry <UNTRUSTED_DATA> wrapper, which
// is a security control and stays. Going lower means truncating descriptions,
// which trades skill-selection quality for tokens and should be validated
// against real sessions before it is done, not guessed at.
func TestOrchestratorInstructionBudgetWithRealSkills(t *testing.T) {
	md := skill.DiscoverMarkdownSkills(".", nil, func(string) {})
	if len(md) == 0 {
		t.Skip("no markdown skills discovered")
	}
	full := buildOrchestratorInstruction(getSkillsPrompt(md, func(string) {}), &config.Config{})

	const maxBytes = 46 << 10 // 46 KiB, down from ~69 KB
	if len(full) > maxBytes {
		t.Errorf("orchestrator instruction with %d skills is %d bytes, over the %d budget", len(md), len(full), maxBytes)
	}
	t.Logf("orchestrator instruction with %d skills: %d bytes (~%d tokens)", len(md), len(full), len(full)/4)
}
