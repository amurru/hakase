// skills_md.go - Markdown skill model, frontmatter parsing and validation.
//
// Implements the portable SKILL.md format shared by Claude Code, Codex CLI,
// Gemini CLI and OpenCode (agentskills.io/specification): one directory per
// skill containing a SKILL.md file with YAML frontmatter (name, description
// required) and a progressive-disclosure markdown body.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// MarkdownSkillFrontmatter is the YAML frontmatter block of a SKILL.md file.
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

// MarkdownSkillMeta is the lightweight metadata used by the list_skills tool
// output for markdown skills.
type MarkdownSkillMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

// MarkdownSkill is a discovered markdown skill: a directory containing a
// SKILL.md file with YAML frontmatter and a markdown body.
type MarkdownSkill struct {
	Frontmatter MarkdownSkillFrontmatter
	Path        string   // absolute path to SKILL.md
	Dir         string   // skill directory (parent of SKILL.md)
	Body        string   // content after closing frontmatter delimiter
	Scripts     []string // files under scripts/, relative to Dir (e.g. "scripts/foo.py")
	Source      string   // discovery root dir this skill came from
}

var markdownSkillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateSkillName checks a markdown skill name against the portability
// constraints: non-empty, at most 64 characters, lowercase alphanumeric
// tokens separated by single hyphens.
func ValidateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("skill name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("skill name exceeds 64 characters")
	}
	if !markdownSkillNameRe.MatchString(name) {
		return fmt.Errorf("skill name must be lowercase alphanumeric with single hyphens")
	}
	return nil
}

// ParseMarkdownSkill reads and validates a SKILL.md file at path. It returns
// the parsed skill or an error describing the first validation failure.
func ParseMarkdownSkill(path string) (*MarkdownSkill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Strip a UTF-8 BOM (0xEF 0xBB 0xBF) if present.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	// Normalize CRLF line endings before any processing.
	content := strings.ReplaceAll(string(data), "\r\n", "\n")

	// Frontmatter must start at byte 0.
	if !strings.HasPrefix(content, "---\n") {
		return nil, fmt.Errorf("frontmatter must start with ---")
	}

	// Scan line by line after the opener; the FIRST line whose trimmed
	// content is exactly "---" ends the frontmatter. A "---" line inside
	// the body must NOT be treated as a delimiter.
	lines := strings.Split(content, "\n")
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return nil, fmt.Errorf("missing closing --- delimiter")
	}

	// Body is everything after the closing delimiter, verbatim. Joining
	// the remainder with "\n" reproduces the original text exactly because
	// every line was separated by a single "\n" after CRLF normalization.
	body := strings.Join(lines[closeIdx+1:], "\n")

	// Parse the frontmatter YAML. Metadata parsing is non-fatal: if the
	// full unmarshal fails (e.g. a metadata value cannot map into
	// map[string]interface{}), fall back to a minimal struct with only
	// Name and Description; the skill survives with Metadata = nil.
	frontmatterYAML := strings.Join(lines[1:closeIdx], "\n")
	var fm MarkdownSkillFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatterYAML), &fm); err != nil {
		var minimal struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if merr := yaml.Unmarshal([]byte(frontmatterYAML), &minimal); merr != nil {
			return nil, err
		}
		fm = MarkdownSkillFrontmatter{Name: minimal.Name, Description: minimal.Description}
	}

	if err := ValidateSkillName(fm.Name); err != nil {
		return nil, err
	}
	if fm.Description == "" {
		return nil, fmt.Errorf("skill description cannot be empty")
	}
	if len(fm.Description) > 1024 {
		return nil, fmt.Errorf("skill description exceeds 1024 characters")
	}

	// The skill directory name must match the frontmatter name exactly
	// (case-sensitive).
	dir := filepath.Dir(path)
	if filepath.Base(dir) != fm.Name {
		return nil, fmt.Errorf("skill name %q does not match directory name %q", fm.Name, filepath.Base(dir))
	}

	scripts, err := listSkillScripts(dir)
	if err != nil {
		return nil, fmt.Errorf("listing scripts for %q: %w", path, err)
	}

	return &MarkdownSkill{
		Frontmatter: fm,
		Path:        path,
		Dir:         dir,
		Body:        body,
		Scripts:     scripts,
	}, nil
}

// listSkillScripts returns the files under <dir>/scripts/, relative to dir
// (e.g. "scripts/foo.py"). Returns an empty slice when no scripts dir exists.
func listSkillScripts(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "scripts", "*"))
	if err != nil {
		return nil, err
	}
	scripts := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(dir, m)
		if err != nil {
			return nil, err
		}
		scripts = append(scripts, rel)
	}
	return scripts, nil
}
