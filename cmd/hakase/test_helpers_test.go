// test_helpers_test.go - shared test helpers previously in skill_discovery_test.go
// and knowledge_test.go (moved to internal/ packages in task 8). Recreated here
// for root test files that still reference them.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// captureStdout runs fn while capturing everything written to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureStdout: os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("captureStdout: close writer: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("captureStdout: read: %v", err)
	}
	return string(data)
}

// makeGitDir creates a ".git" marker inside dir so FindProjectRoot treats it
// as a project root.
func makeGitDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("makeGitDir: %v", err)
	}
	return dir
}

// isolateHome redirects $HOME and $XDG_CONFIG_HOME to fresh temp dirs so
// discovery tests are not polluted by real user-level directories. On
// Windows USERPROFILE is redirected too (os.UserHomeDir reads it).
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("USERPROFILE", home)
	return home
}

// writeNoteFile writes <dir>/<slug>.md with the given content, creating the
// directory if needed. Duplicated from internal/knowledge/knowledge_test.go
// for root test files (task 8).
func writeNoteFile(t *testing.T, dir, slug, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("writeNoteFile: mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeNoteFile: write %s: %v", path, err)
	}
}

// writeSkillAt writes <dir>/<name>/SKILL.md with valid frontmatter and
// returns the skill directory. Duplicated from internal/skill/skill_discovery_test.go
// for root test files (task 8).
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
