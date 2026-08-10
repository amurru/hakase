// skill_discovery_test.go - tests for markdown skill discovery
// (skill_discovery.go).
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeGitDir creates a ".git" marker inside dir so FindProjectRoot treats it
// as a project root.
func makeGitDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("makeGitDir: %v", err)
	}
	return dir
}

// writeSkillAt writes <dir>/<name>/SKILL.md with valid frontmatter and
// returns the skill directory.
func writeSkillAt(t *testing.T, dir, name, description string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("writeSkillAt: %v", err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\nBody of %s\n", name, description, name)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("writeSkillAt: %v", err)
	}
	return skillDir
}

// isolateHome redirects $HOME and $XDG_CONFIG_HOME to fresh temp dirs so
// discovery tests are not polluted by real user-level skill directories on
// the developer machine (e.g. ~/.claude/skills). The real home directory is
// not writable in tests, so user-level locations a test wants to control are
// simulated via extraDirs or by writing into the redirected HOME.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func TestFindProjectRoot(t *testing.T) {
	root := t.TempDir()
	makeGitDir(t, root)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if got := FindProjectRoot(sub); got != root {
		t.Errorf("FindProjectRoot(%q): expected %q, got %q", sub, root, got)
	}
	if got := FindProjectRoot(root); got != root {
		t.Errorf("FindProjectRoot(%q): expected %q, got %q", root, root, got)
	}
}

func TestFindProjectRootNoGit(t *testing.T) {
	dir := t.TempDir()
	if got := FindProjectRoot(dir); got != dir {
		t.Errorf("FindProjectRoot(%q): expected fallback to cwd %q, got %q", dir, dir, got)
	}
}

func TestDiscoverMarkdownSkillsNone(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)

	skills := DiscoverMarkdownSkills(root, nil, nil)
	if skills == nil {
		t.Fatal("expected non-nil slice")
	}
	if len(skills) != 0 {
		t.Fatalf("expected 0 skills, got %d", len(skills))
	}
}

func TestDiscoverMarkdownSkillsAgentsDir(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	source := filepath.Join(root, ".agents", "skills")
	skillDir := writeSkillAt(t, source, "demo-skill", "Does demo things")

	skills := DiscoverMarkdownSkills(root, nil, nil)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if s.Frontmatter.Name != "demo-skill" {
		t.Errorf("Name: expected %q, got %q", "demo-skill", s.Frontmatter.Name)
	}
	if s.Frontmatter.Description != "Does demo things" {
		t.Errorf("Description: expected %q, got %q", "Does demo things", s.Frontmatter.Description)
	}
	if s.Path != filepath.Join(skillDir, "SKILL.md") {
		t.Errorf("Path: expected %q, got %q", filepath.Join(skillDir, "SKILL.md"), s.Path)
	}
	if s.Dir != skillDir {
		t.Errorf("Dir: expected %q, got %q", skillDir, s.Dir)
	}
	if s.Source != source {
		t.Errorf("Source: expected %q, got %q", source, s.Source)
	}
	if !strings.HasPrefix(s.Body, "Body of demo-skill") {
		t.Errorf("Body: expected prefix %q, got %q", "Body of demo-skill", s.Body)
	}
}

func TestDiscoverMarkdownSkillsStandardDirs(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	writeSkillAt(t, filepath.Join(root, ".claude", "skills"), "claude-skill", "claude")
	writeSkillAt(t, filepath.Join(root, ".opencode", "skills"), "opencode-skill", "opencode")
	writeSkillAt(t, filepath.Join(root, ".gemini", "skills"), "gemini-skill", "gemini")

	skills := DiscoverMarkdownSkills(root, nil, nil)
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(skills))
	}
	got := []string{skills[0].Frontmatter.Name, skills[1].Frontmatter.Name, skills[2].Frontmatter.Name}
	want := []string{"claude-skill", "gemini-skill", "opencode-skill"} // sorted by name
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("skills[%d].Name: expected %q, got %q (all: %v)", i, want[i], got[i], got)
		}
	}
}

// TestDiscoverMarkdownSkillsDedupeUserLevel verifies that a project-level
// skill wins over a user-level skill with the same name (first match wins).
// The real home directory is not writable in tests, so the user-level dir is
// simulated by passing it via extraDirs, which is processed after the
// project locations - matching user-level precedence.
func TestDiscoverMarkdownSkillsDedupeUserLevel(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	projectSkill := writeSkillAt(t, filepath.Join(root, ".agents", "skills"), "dup-skill", "project version")
	userSim := t.TempDir()
	writeSkillAt(t, userSim, "dup-skill", "user version")

	skills := DiscoverMarkdownSkills(root, []string{userSim}, nil)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill after dedupe, got %d", len(skills))
	}
	if skills[0].Frontmatter.Name != "dup-skill" {
		t.Errorf("Name: expected %q, got %q", "dup-skill", skills[0].Frontmatter.Name)
	}
	if skills[0].Path != filepath.Join(projectSkill, "SKILL.md") {
		t.Errorf("Path: expected project one %q, got %q", filepath.Join(projectSkill, "SKILL.md"), skills[0].Path)
	}
}

// TestDiscoverMarkdownSkillsDuplicateSummary verifies that overlapping skill
// directories containing the same skill name produce exactly ONE summary log
// line mentioning the skipped duplicates, and the result contains the skill
// only once.
func TestDiscoverMarkdownSkillsDuplicateSummary(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	writeSkillAt(t, filepath.Join(root, ".agents", "skills"), "dup-skill", "first version")
	overlap := t.TempDir()
	writeSkillAt(t, overlap, "dup-skill", "overlapping version")

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	skills := DiscoverMarkdownSkills(root, []string{overlap}, log)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill after dedupe, got %d", len(skills))
	}
	if skills[0].Frontmatter.Name != "dup-skill" {
		t.Errorf("Name: expected %q, got %q", "dup-skill", skills[0].Frontmatter.Name)
	}

	var summaryCount int
	for _, m := range msgs {
		if strings.Contains(m, "[skills] Discovered") && strings.Contains(m, "skipped") && strings.Contains(m, "duplicate") {
			summaryCount++
			if !strings.Contains(m, "1 duplicate(s)") {
				t.Errorf("summary line should report 1 duplicate, got %q", m)
			}
		}
		if strings.Contains(m, "Skipping duplicate markdown skill") {
			t.Errorf("per-duplicate log line should be removed, got %q", m)
		}
	}
	if summaryCount != 1 {
		t.Errorf("expected exactly 1 summary line, got %d (logs: %v)", summaryCount, msgs)
	}
}

func TestDiscoverMarkdownSkillsNestedWalk(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	nearest := writeSkillAt(t, filepath.Join(sub, ".agents", "skills"), "nest-skill", "nearest")
	writeSkillAt(t, filepath.Join(root, ".agents", "skills"), "nest-skill", "root")

	skills := DiscoverMarkdownSkills(sub, nil, nil)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Path != filepath.Join(nearest, "SKILL.md") {
		t.Errorf("Path: expected nearest %q, got %q", filepath.Join(nearest, "SKILL.md"), skills[0].Path)
	}
}

func TestDiscoverMarkdownSkillsProjectLibrary(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	source := filepath.Join(root, "skills")
	writeSkillAt(t, source, "lib-skill", "library")

	skills := DiscoverMarkdownSkills(root, nil, nil)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Frontmatter.Name != "lib-skill" {
		t.Errorf("Name: expected %q, got %q", "lib-skill", skills[0].Frontmatter.Name)
	}
	if skills[0].Source != source {
		t.Errorf("Source: expected %q, got %q", source, skills[0].Source)
	}
}

func TestDiscoverMarkdownSkillsIgnoresNonSkillDirs(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	// Non-skill dirs without SKILL.md must be skipped naturally.
	if err := os.MkdirAll(filepath.Join(root, "skills", "__pycache__"), 0o755); err != nil {
		t.Fatalf("mkdir __pycache__: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "foo.py"), []byte("print('hi')"), 0o644); err != nil {
		t.Fatalf("write foo.py: %v", err)
	}
	writeSkillAt(t, filepath.Join(root, "skills"), "real-skill", "real")

	skills := DiscoverMarkdownSkills(root, nil, nil)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Frontmatter.Name != "real-skill" {
		t.Errorf("Name: expected %q, got %q", "real-skill", skills[0].Frontmatter.Name)
	}
}

func TestDiscoverMarkdownSkillsSkipsInvalid(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	badPath := filepath.Join(root, ".agents", "skills", "bad-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(badPath), 0o755); err != nil {
		t.Fatalf("mkdir bad skill: %v", err)
	}
	// Missing description: invalid per the parser.
	if err := os.WriteFile(badPath, []byte("---\nname: bad-skill\n---\nNo description here\n"), 0o644); err != nil {
		t.Fatalf("write bad skill: %v", err)
	}

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	skills := DiscoverMarkdownSkills(root, nil, log)
	if len(skills) != 0 {
		t.Fatalf("expected 0 skills, got %d", len(skills))
	}
	var warned bool
	for _, m := range msgs {
		if strings.Contains(m, "Skipping invalid markdown skill") && strings.Contains(m, badPath) {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected an invalid-skill warning mentioning %q, got logs: %v", badPath, msgs)
	}
}

func TestDiscoverMarkdownSkillsExtraDirs(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)

	// Relative extra dir, resolved against the project root.
	relSource := filepath.Join(root, "custom", "skills")
	writeSkillAt(t, relSource, "rel-skill", "relative")
	skills := DiscoverMarkdownSkills(root, []string{filepath.Join("custom", "skills")}, nil)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill from relative extra dir, got %d", len(skills))
	}
	if skills[0].Frontmatter.Name != "rel-skill" {
		t.Errorf("Name: expected %q, got %q", "rel-skill", skills[0].Frontmatter.Name)
	}
	if skills[0].Source != relSource {
		t.Errorf("Source: expected %q, got %q", relSource, skills[0].Source)
	}

	// Absolute extra dir.
	absSource := t.TempDir()
	writeSkillAt(t, absSource, "abs-skill", "absolute")
	skills2 := DiscoverMarkdownSkills(root, []string{absSource}, nil)
	if len(skills2) != 1 {
		t.Fatalf("expected 1 skill from absolute extra dir, got %d", len(skills2))
	}
	if skills2[0].Frontmatter.Name != "abs-skill" {
		t.Errorf("Name: expected %q, got %q", "abs-skill", skills2[0].Frontmatter.Name)
	}

	// Non-existent extra dir is skipped silently.
	skills3 := DiscoverMarkdownSkills(root, []string{filepath.Join("nope", "missing")}, nil)
	if len(skills3) != 0 {
		t.Fatalf("expected 0 skills from missing extra dir, got %d", len(skills3))
	}
}

func TestDiscoverMarkdownSkillsSymlinkedDir(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	target := t.TempDir()
	writeSkillAt(t, target, "linked-skill", "linked")

	link := filepath.Join(root, ".agents", "skills", "linked-skill")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir .agents/skills: %v", err)
	}
	if err := os.Symlink(filepath.Join(target, "linked-skill"), link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	skills := DiscoverMarkdownSkills(root, nil, nil)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill via symlink, got %d", len(skills))
	}
	if skills[0].Frontmatter.Name != "linked-skill" {
		t.Errorf("Name: expected %q, got %q", "linked-skill", skills[0].Frontmatter.Name)
	}
}

func TestDiscoverMarkdownSkillsSorted(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	for _, n := range []string{"zebra-skill", "alpha-skill", "mid-skill"} {
		writeSkillAt(t, filepath.Join(root, ".agents", "skills"), n, "desc for "+n)
	}

	skills := DiscoverMarkdownSkills(root, nil, nil)
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(skills))
	}
	want := []string{"alpha-skill", "mid-skill", "zebra-skill"}
	for i, name := range want {
		if skills[i].Frontmatter.Name != name {
			t.Errorf("skills[%d].Name: expected %q, got %q", i, name, skills[i].Frontmatter.Name)
		}
	}
}

// TestDiscoverMarkdownSkillsHakaseHome verifies that the user-level
// ~/.hakase/skills directory (or $HAKASE_HOME/skills) is scanned for markdown
// skills, mirroring the Claude-style user home convention.
func TestDiscoverMarkdownSkillsHakaseHome(t *testing.T) {
	isolateHome(t)
	t.Setenv("HAKASE_HOME", "")
	root := t.TempDir()
	makeGitDir(t, root)
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSkillAt(t, filepath.Join(home, ".hakase", "skills"), "user-home-skill", "user home")

	skills := DiscoverMarkdownSkills(root, nil, nil)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill from ~/.hakase/skills, got %d", len(skills))
	}
	if skills[0].Frontmatter.Name != "user-home-skill" {
		t.Errorf("Name: expected %q, got %q", "user-home-skill", skills[0].Frontmatter.Name)
	}
	if skills[0].Source != filepath.Join(home, ".hakase", "skills") {
		t.Errorf("Source: expected %q, got %q", filepath.Join(home, ".hakase", "skills"), skills[0].Source)
	}
}

// TestDiscoverMarkdownSkillsHakaseHomeEnv verifies that $HAKASE_HOME redirects
// the user-level skill directory.
func TestDiscoverMarkdownSkillsHakaseHomeEnv(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	override := t.TempDir()
	t.Setenv("HAKASE_HOME", override)

	writeSkillAt(t, filepath.Join(override, "skills"), "env-home-skill", "env home")

	skills := DiscoverMarkdownSkills(root, nil, nil)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill from $HAKASE_HOME/skills, got %d", len(skills))
	}
	if skills[0].Frontmatter.Name != "env-home-skill" {
		t.Errorf("Name: expected %q, got %q", "env-home-skill", skills[0].Frontmatter.Name)
	}
	if skills[0].Source != filepath.Join(override, "skills") {
		t.Errorf("Source: expected %q, got %q", filepath.Join(override, "skills"), skills[0].Source)
	}
}

// TestDiscoverMarkdownSkillsProjectBeatsHakaseHome verifies that a
// project-level skill wins over a same-named skill in ~/.hakase/skills (first
// match wins, project scanned before user level).
func TestDiscoverMarkdownSkillsProjectBeatsHakaseHome(t *testing.T) {
	isolateHome(t)
	t.Setenv("HAKASE_HOME", "")
	root := t.TempDir()
	makeGitDir(t, root)
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectSkill := writeSkillAt(t, filepath.Join(root, ".agents", "skills"), "dup-skill", "project version")
	writeSkillAt(t, filepath.Join(home, ".hakase", "skills"), "dup-skill", "user version")

	skills := DiscoverMarkdownSkills(root, nil, nil)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill after dedupe, got %d", len(skills))
	}
	if skills[0].Path != filepath.Join(projectSkill, "SKILL.md") {
		t.Errorf("Path: expected project one %q, got %q", filepath.Join(projectSkill, "SKILL.md"), skills[0].Path)
	}
}
