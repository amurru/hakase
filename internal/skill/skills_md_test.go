// skills_md_test.go - tests for the markdown skill frontmatter parser and
// validation (skills_md.go).
package skill

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeSkill creates <tmp>/<name>/SKILL.md with the given content and
// returns the path to the SKILL.md file.
func writeSkill(t *testing.T, name, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("writeSkill: %v", err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeSkill: %v", err)
	}
	return path
}

func TestParseMarkdownSkillMinimal(t *testing.T) {
	path := writeSkill(t, "demo-skill", "---\nname: demo-skill\ndescription: Does a thing\n---\n# Demo Skill\n\nSome body text.\n")

	skill, err := ParseMarkdownSkill(path)
	if err != nil {
		t.Fatalf("ParseMarkdownSkill: unexpected error: %v", err)
	}
	if skill.Frontmatter.Name != "demo-skill" {
		t.Errorf("Name: expected %q, got %q", "demo-skill", skill.Frontmatter.Name)
	}
	if skill.Frontmatter.Description != "Does a thing" {
		t.Errorf("Description: expected %q, got %q", "Does a thing", skill.Frontmatter.Description)
	}
	if skill.Body != "# Demo Skill\n\nSome body text.\n" {
		t.Errorf("Body: expected %q, got %q", "# Demo Skill\n\nSome body text.\n", skill.Body)
	}
	if skill.Path != path {
		t.Errorf("Path: expected %q, got %q", path, skill.Path)
	}
	if filepath.Base(skill.Dir) != "demo-skill" {
		t.Errorf("Dir: expected base %q, got %q", "demo-skill", filepath.Base(skill.Dir))
	}
	if len(skill.Scripts) != 0 {
		t.Errorf("Scripts: expected empty, got %v", skill.Scripts)
	}
	if skill.Source != "" {
		t.Errorf("Source: parser must not set Source, got %q", skill.Source)
	}
}

func TestParseMarkdownSkillOptionalFields(t *testing.T) {
	path := writeSkill(t, "full-skill", `---
name: full-skill
description: Full featured skill
license: MIT
compatibility: go >= 1.26
metadata:
  author: hakase
  version: 1
allowed-tools: python_interpreter system_exec
---
Body
`)

	skill, err := ParseMarkdownSkill(path)
	if err != nil {
		t.Fatalf("ParseMarkdownSkill: unexpected error: %v", err)
	}
	if skill.Frontmatter.License != "MIT" {
		t.Errorf("License: expected %q, got %q", "MIT", skill.Frontmatter.License)
	}
	if skill.Frontmatter.Compatibility != "go >= 1.26" {
		t.Errorf("Compatibility: expected %q, got %q", "go >= 1.26", skill.Frontmatter.Compatibility)
	}
	if skill.Frontmatter.AllowedTools != "python_interpreter system_exec" {
		t.Errorf("AllowedTools: expected %q, got %q", "python_interpreter system_exec", skill.Frontmatter.AllowedTools)
	}
	if skill.Frontmatter.Metadata == nil {
		t.Fatal("Metadata: expected non-nil")
	}
	if skill.Frontmatter.Metadata["author"] != "hakase" {
		t.Errorf("Metadata[author]: expected %q, got %v", "hakase", skill.Frontmatter.Metadata["author"])
	}
	if v, ok := skill.Frontmatter.Metadata["version"].(int); !ok || v != 1 {
		t.Errorf("Metadata[version]: expected int 1, got %v", skill.Frontmatter.Metadata["version"])
	}
}

func TestParseMarkdownSkillMissingName(t *testing.T) {
	path := writeSkill(t, "noname-skill", "---\ndescription: No name here\n---\nBody\n")
	if _, err := ParseMarkdownSkill(path); err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestParseMarkdownSkillMissingDescription(t *testing.T) {
	path := writeSkill(t, "nodesc-skill", "---\nname: nodesc-skill\n---\nBody\n")
	if _, err := ParseMarkdownSkill(path); err == nil {
		t.Fatal("expected error for missing description")
	}
}

func TestValidateSkillName(t *testing.T) {
	valid := []string{"a", "demo-skill", "web-researcher-2", "x1-y2-z3"}
	for _, name := range valid {
		if err := ValidateSkillName(name); err != nil {
			t.Errorf("ValidateSkillName(%q): unexpected error: %v", name, err)
		}
	}

	invalid := []string{
		"",          // empty
		"Bad_Name",  // underscore
		"bad--name", // double hyphen
		"-bad",      // leading hyphen
		"bad-",      // trailing hyphen
		"UPPER",     // uppercase
		strings.Repeat("a", 65), // too long
		"a b",  // space
		"a_b",  // underscore
		"a.b",  // dot
		"a/b",  // slash
		"中文",   // non-ASCII
	}
	for _, name := range invalid {
		if err := ValidateSkillName(name); err == nil {
			t.Errorf("ValidateSkillName(%q): expected error", name)
		}
	}
}

func TestParseMarkdownSkillDescriptionTooLong(t *testing.T) {
	desc := strings.Repeat("d", 1025)
	path := writeSkill(t, "long-desc", "---\nname: long-desc\ndescription: "+desc+"\n---\nBody\n")
	if _, err := ParseMarkdownSkill(path); err == nil {
		t.Fatal("expected error for 1025-char description")
	}
}

func TestParseMarkdownSkillDirMismatch(t *testing.T) {
	path := writeSkill(t, "wrong-dir", "---\nname: right-dir\ndescription: ok\n---\nBody\n")
	_, err := ParseMarkdownSkill(path)
	if err == nil {
		t.Fatal("expected error for name/dir mismatch")
	}
	if !strings.Contains(err.Error(), "does not match directory name") {
		t.Errorf("error: got %q, want substring %q", err, "does not match directory name")
	}
}

func TestParseMarkdownSkillBodyHorizontalRule(t *testing.T) {
	content := "---\nname: hr-skill\ndescription: has an hr\n---\n# Heading\n\nSome text\n\n---\n\nMore text\n"
	path := writeSkill(t, "hr-skill", content)

	skill, err := ParseMarkdownSkill(path)
	if err != nil {
		t.Fatalf("ParseMarkdownSkill: unexpected error: %v", err)
	}
	if want := "# Heading\n\nSome text\n\n---\n\nMore text\n"; skill.Body != want {
		t.Errorf("Body: expected %q, got %q", want, skill.Body)
	}
}

func TestParseMarkdownSkillCRLF(t *testing.T) {
	content := "---\r\nname: crlf-skill\r\ndescription: crlf endings\r\n---\r\n# Heading\r\n\r\nBody line\r\n"
	path := writeSkill(t, "crlf-skill", content)

	skill, err := ParseMarkdownSkill(path)
	if err != nil {
		t.Fatalf("ParseMarkdownSkill: unexpected error: %v", err)
	}
	if skill.Frontmatter.Name != "crlf-skill" {
		t.Errorf("Name: expected %q, got %q", "crlf-skill", skill.Frontmatter.Name)
	}
	if skill.Frontmatter.Description != "crlf endings" {
		t.Errorf("Description: expected %q, got %q", "crlf endings", skill.Frontmatter.Description)
	}
	if want := "# Heading\n\nBody line\n"; skill.Body != want {
		t.Errorf("Body: expected %q, got %q", want, skill.Body)
	}
}

func TestParseMarkdownSkillBOM(t *testing.T) {
	content := "\xEF\xBB\xBF---\nname: bom-skill\ndescription: has BOM\n---\nBody\n"
	path := writeSkill(t, "bom-skill", content)

	skill, err := ParseMarkdownSkill(path)
	if err != nil {
		t.Fatalf("ParseMarkdownSkill: unexpected error: %v", err)
	}
	if skill.Frontmatter.Name != "bom-skill" {
		t.Errorf("Name: expected %q, got %q", "bom-skill", skill.Frontmatter.Name)
	}
	if want := "Body\n"; skill.Body != want {
		t.Errorf("Body: expected %q, got %q", want, skill.Body)
	}
}

func TestParseMarkdownSkillNonStringMetadata(t *testing.T) {
	content := "---\nname: meta-skill\ndescription: metadata with a number\nmetadata:\n  version: 1\n  tags:\n    - a\n    - b\n---\nBody\n"
	path := writeSkill(t, "meta-skill", content)

	skill, err := ParseMarkdownSkill(path)
	if err != nil {
		t.Fatalf("ParseMarkdownSkill: unexpected error: %v", err)
	}
	if v, ok := skill.Frontmatter.Metadata["version"].(int); !ok || v != 1 {
		t.Errorf("Metadata[version]: expected int 1, got %v", skill.Frontmatter.Metadata["version"])
	}
	if tags, ok := skill.Frontmatter.Metadata["tags"].([]interface{}); !ok || len(tags) != 2 {
		t.Errorf("Metadata[tags]: expected 2-entry list, got %v", skill.Frontmatter.Metadata["tags"])
	}
}

func TestParseMarkdownSkillMetadataFallback(t *testing.T) {
	// A sequence metadata value cannot unmarshal into map[string]interface{};
	// per design the skill must survive with Metadata = nil via the minimal
	// Name/Description fallback.
	content := "---\nname: meta-fallback\ndescription: bad metadata tolerated\nmetadata: [1, 2]\n---\nBody\n"
	path := writeSkill(t, "meta-fallback", content)

	skill, err := ParseMarkdownSkill(path)
	if err != nil {
		t.Fatalf("ParseMarkdownSkill: unexpected error: %v", err)
	}
	if skill.Frontmatter.Name != "meta-fallback" {
		t.Errorf("Name: expected %q, got %q", "meta-fallback", skill.Frontmatter.Name)
	}
	if skill.Frontmatter.Metadata != nil {
		t.Errorf("Metadata: expected nil after fallback, got %v", skill.Frontmatter.Metadata)
	}
}

func TestParseMarkdownSkillMissingClosingDelimiter(t *testing.T) {
	path := writeSkill(t, "no-close", "---\nname: no-close\ndescription: never closes\n")
	_, err := ParseMarkdownSkill(path)
	if err == nil {
		t.Fatal("expected error for missing closing delimiter")
	}
	if !strings.Contains(err.Error(), "missing closing --- delimiter") {
		t.Errorf("error: got %q, want substring %q", err, "missing closing --- delimiter")
	}
}

func TestParseMarkdownSkillFrontmatterNotAtStart(t *testing.T) {
	path := writeSkill(t, "late-fm", "some text\n---\nname: late-fm\ndescription: too late\n---\nBody\n")
	_, err := ParseMarkdownSkill(path)
	if err == nil {
		t.Fatal("expected error for frontmatter not at byte 0")
	}
	if !strings.Contains(err.Error(), "frontmatter must start with ---") {
		t.Errorf("error: got %q, want substring %q", err, "frontmatter must start with ---")
	}
}

func TestParseMarkdownSkillScripts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scripted-skill")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "foo.py"), []byte("print(1)"), 0o644); err != nil {
		t.Fatalf("write foo.py: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "bar.sh"), []byte("#!/bin/sh"), 0o644); err != nil {
		t.Fatalf("write bar.sh: %v", err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: scripted-skill\ndescription: has scripts\n---\nBody\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	skill, err := ParseMarkdownSkill(path)
	if err != nil {
		t.Fatalf("ParseMarkdownSkill: unexpected error: %v", err)
	}
	want := []string{"scripts/bar.sh", "scripts/foo.py"} // glob is sorted lexically
	if !slices.Equal(skill.Scripts, want) {
		t.Errorf("Scripts: expected %v, got %v", want, skill.Scripts)
	}
}

func TestParseMarkdownSkillNoScripts(t *testing.T) {
	path := writeSkill(t, "no-scripts", "---\nname: no-scripts\ndescription: no scripts\n---\nBody\n")
	skill, err := ParseMarkdownSkill(path)
	if err != nil {
		t.Fatalf("ParseMarkdownSkill: unexpected error: %v", err)
	}
	if skill.Scripts == nil || len(skill.Scripts) != 0 {
		t.Errorf("Scripts: expected empty non-nil, got %v", skill.Scripts)
	}
}
