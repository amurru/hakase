// skill_cli_test.go - tests for the `hakase skill` CLI (skill_cli.go).
//
// All tests drive RunSkillCLI directly (exit codes, not os.Exit) and always
// pass --dir with t.TempDir() so nothing is ever written outside the temp
// directory.
package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestSkillCLICreateValidateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if code := RunSkillCLI([]string{"create", "--dir", dir, "demo-skill"}); code != 0 {
		t.Fatalf("create: expected exit code 0, got %d", code)
	}
	// The freshly scaffolded skill must validate with no manual edits.
	if code := RunSkillCLI([]string{"validate", filepath.Join(dir, "demo-skill")}); code != 0 {
		t.Fatalf("validate: expected exit code 0, got %d", code)
	}

	skillPath := filepath.Join(dir, "demo-skill", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "name: demo-skill") {
		t.Errorf("SKILL.md: expected to contain %q, got:\n%s", "name: demo-skill", content)
	}
	if !strings.Contains(content, "description:") {
		t.Errorf("SKILL.md: expected a description field, got:\n%s", content)
	}
	if !strings.Contains(content, "description: '"+skillDescriptionPlaceholder+"'") {
		t.Errorf("SKILL.md: expected non-empty placeholder description, got:\n%s", content)
	}
}

func TestSkillCLICreateExistingDirFails(t *testing.T) {
	dir := t.TempDir()

	if code := RunSkillCLI([]string{"create", "--dir", dir, "demo-skill"}); code != 0 {
		t.Fatalf("first create: expected exit code 0, got %d", code)
	}
	if code := RunSkillCLI([]string{"create", "--dir", dir, "demo-skill"}); code != 1 {
		t.Fatalf("second create without --force: expected exit code 1, got %d", code)
	}
}

func TestSkillCLICreateForceOverwrite(t *testing.T) {
	dir := t.TempDir()

	if code := RunSkillCLI([]string{"create", "--dir", dir, "demo-skill"}); code != 0 {
		t.Fatalf("first create: expected exit code 0, got %d", code)
	}

	// Replace SKILL.md with a marker file; --force must overwrite it.
	skillPath := filepath.Join(dir, "demo-skill", "SKILL.md")
	const marker = "# CUSTOM MARKER LINE\n"
	if err := os.WriteFile(skillPath, []byte(marker), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if code := RunSkillCLI([]string{"create", "--dir", dir, "demo-skill", "--force"}); code != 0 {
		t.Fatalf("create with --force: expected exit code 0, got %d", code)
	}

	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "CUSTOM MARKER LINE") {
		t.Errorf("SKILL.md was not overwritten by --force, got:\n%s", content)
	}
	if !strings.Contains(content, "name: demo-skill") {
		t.Errorf("overwritten SKILL.md missing frontmatter, got:\n%s", content)
	}
}

func TestSkillCLICreateDescriptionOverride(t *testing.T) {
	dir := t.TempDir()

	// Name first, flags after: exercises the manual argument parsing order.
	if code := RunSkillCLI([]string{"create", "demo-skill", "--dir", dir, "--description", "Custom desc"}); code != 0 {
		t.Fatalf("create: expected exit code 0, got %d", code)
	}

	data, err := os.ReadFile(filepath.Join(dir, "demo-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "description: 'Custom desc'") {
		t.Errorf("SKILL.md: expected custom description, got:\n%s", content)
	}
}

func TestSkillCLICreateInvalidName(t *testing.T) {
	for _, name := range []string{"Bad Name!", "UPPER"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			if code := RunSkillCLI([]string{"create", "--dir", dir, name}); code != 1 {
				t.Fatalf("create %q: expected exit code 1, got %d", name, code)
			}
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Errorf("create %q: expected no directory to be created, stat err = %v", name, err)
			}
		})
	}
}

func TestSkillCLIValidateExitCodes(t *testing.T) {
	dir := t.TempDir()

	// An existing directory without a SKILL.md is not a valid skill.
	if code := RunSkillCLI([]string{"validate", dir}); code != 1 {
		t.Errorf("validate non-skill directory: expected exit code 1, got %d", code)
	}

	if code := RunSkillCLI([]string{"create", "--dir", dir, "demo-skill"}); code != 0 {
		t.Fatalf("create: expected exit code 0, got %d", code)
	}
	if code := RunSkillCLI([]string{"validate", filepath.Join(dir, "demo-skill")}); code != 0 {
		t.Errorf("validate round-trip skill: expected exit code 0, got %d", code)
	}
}

func TestSkillCLIList(t *testing.T) {
	dir := t.TempDir()

	// Create the skill under .agents/skills so the standard discovery walk
	// (from cwd up to the project root) finds it.
	skillsDir := filepath.Join(dir, ".agents", "skills")
	if code := RunSkillCLI([]string{"create", "--dir", skillsDir, "demo-skill"}); code != 0 {
		t.Fatalf("create: expected exit code 0, got %d", code)
	}

	t.Chdir(dir)
	out := captureStdout(t, func() {
		if code := RunSkillCLI([]string{"list"}); code != 0 {
			t.Errorf("list: expected exit code 0, got %d", code)
		}
	})
	if !strings.Contains(out, "demo-skill") {
		t.Errorf("list output missing skill name, got:\n%s", out)
	}
	if !strings.Contains(out, "Markdown skills:") {
		t.Errorf("list output missing markdown section, got:\n%s", out)
	}
}

func TestSkillCLIUsageAndHelp(t *testing.T) {
	// No subcommand or unknown subcommand: usage error, exit code 2.
	if code := RunSkillCLI(nil); code != 2 {
		t.Errorf("no subcommand: expected exit code 2, got %d", code)
	}
	if code := RunSkillCLI([]string{"bogus"}); code != 2 {
		t.Errorf("unknown subcommand: expected exit code 2, got %d", code)
	}

	// -h/--help must return 0 on every subcommand.
	for _, args := range [][]string{
		{"create", "-h"},
		{"create", "--help"},
		{"list", "-h"},
		{"list", "--help"},
		{"validate", "-h"},
		{"validate", "--help"},
	} {
		if code := RunSkillCLI(args); code != 0 {
			t.Errorf("%v: expected exit code 0 for help, got %d", args, code)
		}
	}
}
