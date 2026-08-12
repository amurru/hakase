// Package skill provides markdown and Python skill discovery, parsing,
// validation, evaluation, and darwinian evolution.
package skill

import "amurru/hakase/internal/interfaces"

// --- Markdown skill types (SKILL.md) ---

// MarkdownSkillFrontmatter is the YAML frontmatter of a SKILL.md file.
// Metadata is tolerant by design: non-string values must not invalidate a
// skill (see ParseMarkdownSkill fallback).
type MarkdownSkillFrontmatter struct {
	Name          string                 `yaml:"name"`
	Description   string                 `yaml:"description"`
	License       string                 `yaml:"license,omitempty"`
	Compatibility string                 `yaml:"compatibility,omitempty"`
	Metadata      map[string]interface{} `yaml:"metadata,omitempty"`
	AllowedTools  string                 `yaml:"allowed-tools,omitempty"`
}

// MarkdownSkill represents a discovered markdown skill (SKILL.md).
type MarkdownSkill struct {
	Frontmatter MarkdownSkillFrontmatter
	Path        string   // absolute path to SKILL.md
	Dir         string   // skill directory (parent of SKILL.md)
	Body        string   // content after closing frontmatter delimiter
	Scripts     []string // files under scripts/, relative to Dir (e.g. "scripts/foo.py")
	Source      string   // discovery root dir this skill came from
}

// MarkdownSkillMeta is the lightweight metadata used by the list_skills tool
// output for markdown skills.
type MarkdownSkillMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

// --- Python skill registry types (skills.json) ---

// SkillMeta is a single entry in the Python skill registry.
type SkillMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FileName    string `json:"file_name"`
	SavedAt     string `json:"saved_at"`
	// Evolution tracking (plan Phase 3c): Deprecated marks skills whose eval
	// hit rate fell below the deprecation threshold; EvalScore is the last
	// full eval-set score; EvolveCount counts successful promotions;
	// LastEvolvedAt is when the skill was last promoted. All omitempty so
	// existing skills.json files remain backward compatible.
	Deprecated    bool    `json:"deprecated,omitempty"`
	EvalScore     float64 `json:"eval_score,omitempty"`
	EvolveCount   int     `json:"evolve_count,omitempty"`
	LastEvolvedAt string  `json:"last_evolved_at,omitempty"`
}

// SkillRegistry is the JSON structure persisted in skills.json.
type SkillRegistry struct {
	Skills []SkillMeta `json:"skills"`
}

// --- Bridge for cross-package test access ---

// DiscoverMarkdownSkillsForTest is a bridge function variable set by the
// root package's init() to call the actual DiscoverMarkdownSkills. This
// allows the agent package's tests to exercise skill discovery without
// importing the root package.
var DiscoverMarkdownSkillsForTest func(cwd string, extraDirs []string, log interfaces.LogFunc) []MarkdownSkill
