package agent

import "amurru/hakase/internal/skill"

// Type aliases for backward compatibility with code that expects these types
// in the agent package.
type (
	MarkdownSkillFrontmatter = skill.MarkdownSkillFrontmatter
	MarkdownSkill            = skill.MarkdownSkill
	MarkdownSkillMeta        = skill.MarkdownSkillMeta
)

// DiscoverMarkdownSkillsForTest is aliased to the skill package.
var DiscoverMarkdownSkillsForTest = skill.DiscoverMarkdownSkillsForTest
