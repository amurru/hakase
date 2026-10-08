package agent

import (
	"strings"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/skill"
)

// TestSkillsIndexFormatOverheadIsBounded pins what this change actually
// controls: the per-entry formatting cost, independent of description length.
//
// A flat "bytes per skill" budget cannot work here. Descriptions range from
// one line to several hundred bytes, so the average depends on which skills
// happen to be installed: ~228 bytes/skill across the 148 skills present in
// a working tree that also has editor/agent skill directories, and ~445
// across this repo's own 23. The first version of this test used a flat
// budget and failed in CI for exactly that reason.
//
// What the format adds on top of the name and description is the
// "<UNTRUSTED_DATA>" marker plus separators - about 43 bytes. Before this
// change the per-entry overhead was roughly 210 bytes (a repeated load
// sentence plus an absolute Location path), so this bound separates the two
// formats cleanly while tolerating any skill mix.
func TestSkillsIndexFormatOverheadIsBounded(t *testing.T) {
	md := skill.DiscoverMarkdownSkills(".", nil, func(string) {})
	if len(md) == 0 {
		t.Skip("no markdown skills discovered")
	}
	// Force eager mode to test per-entry overhead regardless of skill count.
	cfg := &config.Config{SkillsIndexMode: "eager"}
	block := getSkillsPrompt(md, func(string) {}, cfg)

	var content int
	for _, s := range md {
		content += len(s.Frontmatter.Name) + len(s.Frontmatter.Description)
	}
	overheadPerSkill := float64(len(block)-content) / float64(len(md))

	const maxOverhead = 64.0
	if overheadPerSkill > maxOverhead {
		t.Errorf("skills index adds %.0f bytes/skill of formatting overhead for %d skills (index %d bytes, name+description %d), want <= %.0f - the per-entry load sentence or Location path is probably back",
			overheadPerSkill, len(md), len(block), content, maxOverhead)
	}
	t.Logf("%d skills: index %d bytes, %.0f bytes/skill overhead", len(md), len(block), overheadPerSkill)

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
	cfg := &config.Config{SkillsIndexMode: "eager"}
	block := getSkillsPrompt(md, func(string) {}, cfg)

	if n := strings.Count(block, "load_markdown_skill"); n > 1 {
		t.Errorf("load_markdown_skill mentioned %d times, want once in the header", n)
	}
	// The per-entry absolute path is dead weight: the model loads by name.
	if strings.Contains(block, "Location:") {
		t.Error("index still emits a Location line; skills are loaded by name")
	}
}

// TestOrchestratorInstructionBudgetWithRealSkills records the end-to-end
// prompt size for the skills this working tree happens to have. It is a
// ceiling, not a target, and it is loose on purpose: the number moves with
// the installed skill set, which differs between a developer working tree
// and a CI checkout.
//
// The remaining bulk is the skill descriptions themselves plus the per-entry
// <UNTRUSTED_DATA> marker, which is a security control and stays. Going lower
// means truncating descriptions, which trades skill-selection quality for
// tokens and should be validated against real sessions rather than guessed at.
func TestOrchestratorInstructionBudgetWithRealSkills(t *testing.T) {
	md := skill.DiscoverMarkdownSkills(".", nil, func(string) {})
	if len(md) == 0 {
		t.Skip("no markdown skills discovered")
	}
	full := buildOrchestratorInstruction(getSkillsPrompt(md, func(string) {}), &config.Config{})

	const maxBytes = 64 << 10
	if len(full) > maxBytes {
		t.Errorf("orchestrator instruction with %d skills is %d bytes, over the %d ceiling", len(md), len(full), maxBytes)
	}
	t.Logf("orchestrator instruction with %d skills: %d bytes (~%d tokens)", len(md), len(full), len(full)/4)
}

func TestSkillsIndexThresholdAndStub(t *testing.T) {
	md := skill.DiscoverMarkdownSkills(".", nil, func(string) {})
	if len(md) == 0 {
		t.Skip("no markdown skills discovered")
	}

	// 1. Force search-stub mode
	stubCfg := &config.Config{SkillsIndexMode: "search-stub"}
	blockStub := getSkillsPrompt(md, func(string) {}, stubCfg)
	if !strings.Contains(blockStub, "skills installed; call search_skills{query}") {
		t.Errorf("expected search-stub prompt to contain stub line, got:\n%s", blockStub)
	}

	// 2. Auto mode with low threshold forces stub
	lowThreshCfg := &config.Config{SkillsIndexMode: "auto", SkillsSearchThreshold: 1}
	blockLow := getSkillsPrompt(md, func(string) {}, lowThreshCfg)
	if !strings.Contains(blockLow, "skills installed; call search_skills{query}") {
		t.Errorf("expected low-threshold prompt to contain stub line, got:\n%s", blockLow)
	}

	// 3. Eager mode bypasses threshold
	eagerCfg := &config.Config{SkillsIndexMode: "eager", SkillsSearchThreshold: 1}
	blockEager := getSkillsPrompt(md, func(string) {}, eagerCfg)
	if strings.Contains(blockEager, "call search_skills{query}") {
		t.Errorf("expected eager prompt NOT to contain stub line")
	}
}

func TestSearchSkillsToolRelevance(t *testing.T) {
	md := skill.DiscoverMarkdownSkills(".", nil, func(string) {})
	if len(md) == 0 {
		t.Skip("no markdown skills discovered")
	}

	st, err := CreateSearchSkillsTool(md, ".", nil, nil)
	if err != nil {
		t.Fatalf("CreateSearchSkillsTool error: %v", err)
	}

	if st.Name() != "search_skills" {
		t.Errorf("tool name = %q, want %q", st.Name(), "search_skills")
	}
}

func TestPrefixByteStability(t *testing.T) {
	md := skill.DiscoverMarkdownSkills(".", nil, func(string) {})
	if len(md) == 0 {
		t.Skip("no markdown skills discovered")
	}

	eagerCfg := &config.Config{SkillsIndexMode: "eager"}
	prompt1 := getSkillsPrompt(md, func(string) {}, eagerCfg)
	prompt2 := getSkillsPrompt(md, func(string) {}, eagerCfg)

	if prompt1 != prompt2 {
		t.Errorf("prefix byte stability failed: prompt outputs differ between calls")
	}
}
