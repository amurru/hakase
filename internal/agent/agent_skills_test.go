// agent_skills_test.go - tests for the markdown skill integration in
// agent.go: getSkillsPrompt merge/collision behavior and the
// load_markdown_skill tool.
package agent

import (
	"amurru/hakase/internal/skill"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
)

// Test helpers (copied from root's skill_discovery_test.go)

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
// discovery tests are not polluted by real user-level skill directories.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// writePythonRegistry writes a skills.json registry into <dir>/skills/.
func writePythonRegistry(t *testing.T, dir string, skills []skill.SkillMeta) {
	t.Helper()
	skillsDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("writePythonRegistry: %v", err)
	}
	data, err := json.Marshal(skill.SkillRegistry{Skills: skills})
	if err != nil {
		t.Fatalf("writePythonRegistry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "skills.json"), data, 0o644); err != nil {
		t.Fatalf("writePythonRegistry: %v", err)
	}
}

// writeMarkdownSkillWithScripts writes <parent>/<name>/SKILL.md with the given
// body plus a scripts/foo.py file, and returns the skill directory.
func writeMarkdownSkillWithScripts(t *testing.T, parent, name, description, body string) string {
	t.Helper()
	skillDir := filepath.Join(parent, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("writeMarkdownSkillWithScripts: %v", err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n%s", name, description, body)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("writeMarkdownSkillWithScripts: %v", err)
	}
	scriptsDir := filepath.Join(skillDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("writeMarkdownSkillWithScripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "foo.py"), []byte("def main():\n    print('hi')\n"), 0o644); err != nil {
		t.Fatalf("writeMarkdownSkillWithScripts: %v", err)
	}
	return skillDir
}

func TestGetSkillsPromptMixed(t *testing.T) {
	isolateHome(t)
	sandbox := t.TempDir()
	t.Chdir(sandbox)
	makeGitDir(t, sandbox)

	writePythonRegistry(t, sandbox, []skill.SkillMeta{
		{Name: "render_card", Description: "Renders an HTML card to PNG", FileName: "render_card.py", SavedAt: "2026-08-01T00:00:00Z"},
		{Name: "extract_pdf", Description: "Extracts tables from PDFs", FileName: "extract_pdf.py", SavedAt: "2026-08-01T00:00:00Z"},
	})
	writeMarkdownSkillWithScripts(t, filepath.Join(sandbox, ".agents", "skills"), "data-cleaner", "Cleans raw datasets", "Steps:\n1. Load the CSV.\n2. Run scripts/foo.py.\n")

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	mdSkills := DiscoverMarkdownSkillsForTest(sandbox, nil, log)
	if len(mdSkills) != 1 {
		t.Fatalf("expected 1 markdown skill, got %d", len(mdSkills))
	}

	prompt := getSkillsPrompt(mdSkills, log)

	if got := strings.Count(prompt, "AVAILABLE PRE-LEARNED SKILLS:"); got != 1 {
		t.Errorf("expected exactly 1 header, got %d\nprompt:\n%s", got, prompt)
	}
	for _, want := range []string{
		"- Skill: 'render_card'\n  Description: \n<UNTRUSTED_DATA>\nRenders an HTML card to PNG\n</UNTRUSTED_DATA>\n\n  Import Usage: `from skills.render_card import ...` or `import render_card`\n\n",
		"- Skill: 'extract_pdf'\n  Description: \n<UNTRUSTED_DATA>\nExtracts tables from PDFs\n</UNTRUSTED_DATA>\n\n  Import Usage: `from skills.extract_pdf import ...` or `import extract_pdf`\n\n",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing python entry %q\nprompt:\n%s", want, prompt)
		}
	}
	for _, want := range []string{
		"- Skill: 'data-cleaner' (markdown)",
		"  Description: \n<UNTRUSTED_DATA>\nCleans raw datasets\n</UNTRUSTED_DATA>\n",
		"  Location: " + filepath.Join(sandbox, ".agents", "skills"),
		"  Load: call 'load_markdown_skill' with name 'data-cleaner' to read full instructions",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing markdown fragment %q\nprompt:\n%s", want, prompt)
		}
	}
	if len(msgs) != 0 {
		t.Errorf("expected no warnings, got %v", msgs)
	}
}

func TestGetSkillsPromptCollision(t *testing.T) {
	isolateHome(t)
	sandbox := t.TempDir()
	t.Chdir(sandbox)
	makeGitDir(t, sandbox)

	writePythonRegistry(t, sandbox, []skill.SkillMeta{
		{Name: "shared", Description: "Python version of shared", FileName: "shared.py", SavedAt: "2026-08-01T00:00:00Z"},
		{Name: "plain-skill", Description: "Unaffected python skill", FileName: "plain_skill.py", SavedAt: "2026-08-01T00:00:00Z"},
	})
	writeMarkdownSkillWithScripts(t, filepath.Join(sandbox, ".agents", "skills"), "shared", "Markdown version of shared", "Use the markdown version.\n")

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	mdSkills := DiscoverMarkdownSkillsForTest(sandbox, nil, log)
	if len(mdSkills) != 1 {
		t.Fatalf("expected 1 markdown skill, got %d", len(mdSkills))
	}

	prompt := getSkillsPrompt(mdSkills, log)

	if strings.Contains(prompt, "Import Usage: `from skills.shared import ...`") {
		t.Errorf("colliding python entry must be omitted from prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "- Skill: 'shared' (markdown)") {
		t.Errorf("markdown entry must win in prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "- Skill: 'plain-skill'") {
		t.Errorf("non-colliding python entry must stay in prompt:\n%s", prompt)
	}
	want := "[skills] Skipping Python skill 'shared' in prompt: collides with markdown skill"
	var warned bool
	for _, m := range msgs {
		if m == want {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected collision warning %q, got logs: %v", want, msgs)
	}
}

func TestGetSkillsPromptEmpty(t *testing.T) {
	isolateHome(t)
	sandbox := t.TempDir()
	t.Chdir(sandbox)

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	if got := getSkillsPrompt(nil, log); got != "No pre-existing skills currently saved." {
		t.Errorf("no skills.json: expected %q, got %q", "No pre-existing skills currently saved.", got)
	}

	writePythonRegistry(t, sandbox, nil)
	if got := getSkillsPrompt(nil, log); got != "No pre-existing skills currently saved." {
		t.Errorf("empty registry: expected %q, got %q", "No pre-existing skills currently saved.", got)
	}

	if err := os.WriteFile(filepath.Join(sandbox, "skills", "skills.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt skills.json: %v", err)
	}
	if got := getSkillsPrompt(nil, log); got != "No pre-existing skills currently saved." {
		t.Errorf("corrupt registry: expected %q, got %q", "No pre-existing skills currently saved.", got)
	}

	if len(msgs) != 0 {
		t.Errorf("expected no warnings, got %v", msgs)
	}
}

func TestLoadMarkdownSkillRoundTrip(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	skillDir := writeMarkdownSkillWithScripts(t, filepath.Join(root, ".agents", "skills"), "data-cleaner", "Cleans raw datasets", "Run these steps:\n1. Load raw.csv\n2. Run scripts/foo.py\n")

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	skills := DiscoverMarkdownSkillsForTest(root, nil, log)
	if len(skills) != 1 {
		t.Fatalf("expected 1 markdown skill, got %d", len(skills))
	}

	loadTool, err := CreateLoadMarkdownSkillTool(skills, root, nil, log)
	if err != nil {
		t.Fatalf("createLoadMarkdownSkillTool: %v", err)
	}
	if got := loadTool.Name(); got != "load_markdown_skill" {
		t.Errorf("tool name: expected %q, got %q", "load_markdown_skill", got)
	}

	runnable, ok := loadTool.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("tool %T does not expose Run", loadTool)
	}

	ctx := agent.NewContext(&agent.ContextMock{})
	result, err := runnable.Run(ctx, map[string]any{"name": "data-cleaner"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out LoadMarkdownSkillOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if out.Name != "data-cleaner" {
		t.Errorf("Name: expected %q, got %q", "data-cleaner", out.Name)
	}
	if out.Description != "\n<UNTRUSTED_DATA>\nCleans raw datasets\n</UNTRUSTED_DATA>\n" {
		t.Errorf("Description: expected wrapped description, got %q", out.Description)
	}
	if !strings.Contains(out.Content, "Run these steps") || !strings.Contains(out.Content, "scripts/foo.py") {
		t.Errorf("Content: expected skill body, got %q", out.Content)
	}
	if out.Location != filepath.Join(skillDir, "SKILL.md") {
		t.Errorf("Location: expected %q, got %q", filepath.Join(skillDir, "SKILL.md"), out.Location)
	}
	if len(out.Scripts) != 1 || out.Scripts[0] != "scripts/foo.py" {
		t.Errorf("Scripts: expected [scripts/foo.py], got %v", out.Scripts)
	}

	wantLog := "[skills] Loaded markdown skill 'data-cleaner'"
	var logged bool
	for _, m := range msgs {
		if m == wantLog {
			logged = true
		}
	}
	if !logged {
		t.Errorf("expected log %q, got logs: %v", wantLog, msgs)
	}
}

func TestBuildOrchestratorInstruction(t *testing.T) {
	isolateHome(t)
	sandbox := t.TempDir()
	t.Chdir(sandbox)
	makeGitDir(t, sandbox)

	writeMarkdownSkillWithScripts(t, filepath.Join(sandbox, ".agents", "skills"), "data-cleaner", "Cleans raw datasets", "Steps:\n1. Load the CSV.\n2. Run scripts/foo.py.\n")

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	mdSkills := DiscoverMarkdownSkillsForTest(sandbox, nil, log)
	if len(mdSkills) != 1 {
		t.Fatalf("expected 1 markdown skill, got %d", len(mdSkills))
	}

	installedSkills := getSkillsPrompt(mdSkills, log)
	instruction := buildOrchestratorInstruction(installedSkills)

	for _, want := range []string{
		"AVAILABLE PRE-LEARNED SKILLS:",
		"- Skill: 'data-cleaner' (markdown)",
		"load_markdown_skill",
		"web_researcher",
		"code_interpreter",
		"system_exec",
		"### CREATING NEW MARKDOWN SKILLS:",
		"prefer writing it to the project root's '.agents/skills/' directory",
		"may write to another valid discovery location",
		"a skill placed elsewhere will never be loaded",
		"skill directory name must match the 'name' in its SKILL.md frontmatter",
	} {
		if !strings.Contains(instruction, want) {
			t.Errorf("orchestrator instruction missing %q\ninstruction:\n%s", want, instruction)
		}
	}
}

func TestLoadMarkdownSkillNotFound(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	writeMarkdownSkillWithScripts(t, filepath.Join(root, ".agents", "skills"), "data-cleaner", "Cleans raw datasets", "Body\n")

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	skills := DiscoverMarkdownSkillsForTest(root, nil, log)
	loadTool, err := CreateLoadMarkdownSkillTool(skills, root, nil, log)
	if err != nil {
		t.Fatalf("createLoadMarkdownSkillTool: %v", err)
	}

	runnable, ok := loadTool.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("tool %T does not expose Run", loadTool)
	}

	ctx := agent.NewContext(&agent.ContextMock{})
	_, err = runnable.Run(ctx, map[string]any{"name": "no-such-skill"})
	if err == nil {
		t.Fatal("expected error for unknown skill name")
	}
	if !strings.Contains(err.Error(), "skill not found") {
		t.Errorf("expected error containing %q, got %q", "skill not found", err.Error())
	}
}

func TestGetSkillsPromptExcludesDisabled(t *testing.T) {
	isolateHome(t)
	sandbox := t.TempDir()
	t.Chdir(sandbox)
	makeGitDir(t, sandbox)

	writePythonRegistry(t, sandbox, []skill.SkillMeta{
		{Name: "render_card", Description: "Renders an HTML card to PNG", FileName: "render_card.py", SavedAt: "2026-08-01T00:00:00Z"},
		{Name: "extract_pdf", Description: "Extracts tables from PDFs", FileName: "extract_pdf.py", SavedAt: "2026-08-01T00:00:00Z"},
	})
	writeMarkdownSkillWithScripts(t, filepath.Join(sandbox, ".agents", "skills"), "data-cleaner", "Cleans raw datasets", "Steps:\n1. Load the CSV.\n")

	if err := skill.SetSkillDisabled(skill.KindPython, "render_card", true); err != nil {
		t.Fatalf("SetSkillDisabled(render_card): %v", err)
	}
	if err := skill.SetSkillDisabled(skill.KindMarkdown, "data-cleaner", true); err != nil {
		t.Fatalf("SetSkillDisabled(data-cleaner): %v", err)
	}

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	mdSkills := DiscoverMarkdownSkillsForTest(sandbox, nil, log)
	if len(mdSkills) != 1 {
		t.Fatalf("expected 1 discovered markdown skill, got %d", len(mdSkills))
	}

	prompt := getSkillsPrompt(mdSkills, log)

	if strings.Contains(prompt, "render_card") {
		t.Errorf("disabled python skill must be excluded from prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "data-cleaner") {
		t.Errorf("disabled markdown skill must be excluded from prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "extract_pdf") {
		t.Errorf("enabled python skill must stay in prompt:\n%s", prompt)
	}

	wantDisabledLogs := []string{
		"[skills] Skipping disabled markdown skill 'data-cleaner'",
		"[skills] Skipping disabled Python skill 'render_card'",
	}
	for _, want := range wantDisabledLogs {
		var logged bool
		for _, m := range msgs {
			if m == want {
				logged = true
			}
		}
		if !logged {
			t.Errorf("expected log %q, got logs: %v", want, msgs)
		}
	}
}

func TestGetSkillsPromptDisabledCollision(t *testing.T) {
	isolateHome(t)
	sandbox := t.TempDir()
	t.Chdir(sandbox)
	makeGitDir(t, sandbox)

	writePythonRegistry(t, sandbox, []skill.SkillMeta{
		{Name: "shared", Description: "Python version of shared", FileName: "shared.py", SavedAt: "2026-08-01T00:00:00Z"},
	})
	writeMarkdownSkillWithScripts(t, filepath.Join(sandbox, ".agents", "skills"), "shared", "Markdown version of shared", "Use the markdown version.\n")

	if err := skill.SetSkillDisabled(skill.KindMarkdown, "shared", true); err != nil {
		t.Fatalf("SetSkillDisabled(shared): %v", err)
	}

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	mdSkills := DiscoverMarkdownSkillsForTest(sandbox, nil, log)

	prompt := getSkillsPrompt(mdSkills, log)

	// Disabling the markdown skill lifts its shadow: the python twin becomes
	// visible again.
	if !strings.Contains(prompt, "- Skill: 'shared'") {
		t.Errorf("python twin should be visible once the markdown skill is disabled:\n%s", prompt)
	}
	if strings.Contains(prompt, "(markdown)") {
		t.Errorf("disabled markdown skill must be excluded from prompt:\n%s", prompt)
	}
}

func TestLoadMarkdownSkillDisabled(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	makeGitDir(t, root)
	writeMarkdownSkillWithScripts(t, filepath.Join(root, ".agents", "skills"), "data-cleaner", "Cleans raw datasets", "Body\n")

	if err := skill.SetSkillDisabled(skill.KindMarkdown, "data-cleaner", true); err != nil {
		t.Fatalf("SetSkillDisabled(data-cleaner): %v", err)
	}

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	skills := DiscoverMarkdownSkillsForTest(root, nil, log)
	loadTool, err := CreateLoadMarkdownSkillTool(skills, root, nil, log)
	if err != nil {
		t.Fatalf("createLoadMarkdownSkillTool: %v", err)
	}

	runnable, ok := loadTool.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("tool %T does not expose Run", loadTool)
	}

	ctx := agent.NewContext(&agent.ContextMock{})
	_, err = runnable.Run(ctx, map[string]any{"name": "data-cleaner"})
	if err == nil {
		t.Fatal("expected error for disabled skill")
	}
	if !strings.Contains(err.Error(), "skill is disabled") {
		t.Errorf("expected error containing %q, got %q", "skill is disabled", err.Error())
	}
}

func TestListSkillsToolExcludesDisabled(t *testing.T) {
	isolateHome(t)
	sandbox := t.TempDir()
	t.Chdir(sandbox)
	makeGitDir(t, sandbox)

	writePythonRegistry(t, sandbox, []skill.SkillMeta{
		{Name: "render_card", Description: "Renders an HTML card to PNG", FileName: "render_card.py", SavedAt: "2026-08-01T00:00:00Z"},
		{Name: "extract_pdf", Description: "Extracts tables from PDFs", FileName: "extract_pdf.py", SavedAt: "2026-08-01T00:00:00Z"},
	})
	writeMarkdownSkillWithScripts(t, filepath.Join(sandbox, ".agents", "skills"), "data-cleaner", "Cleans raw datasets", "Steps:\n")

	if err := skill.SetSkillDisabled(skill.KindPython, "render_card", true); err != nil {
		t.Fatalf("SetSkillDisabled(render_card): %v", err)
	}
	if err := skill.SetSkillDisabled(skill.KindMarkdown, "data-cleaner", true); err != nil {
		t.Fatalf("SetSkillDisabled(data-cleaner): %v", err)
	}

	var msgs []string
	log := func(msg string) { msgs = append(msgs, msg) }

	lsTool, err := createListSkillsTool(sandbox, nil, log)
	if err != nil {
		t.Fatalf("createListSkillsTool: %v", err)
	}
	runnable, ok := lsTool.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("tool %T does not expose Run", lsTool)
	}

	ctx := agent.NewContext(&agent.ContextMock{})
	result, err := runnable.Run(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ListSkillsOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(out.Skills) != 1 || out.Skills[0].Name != "extract_pdf" {
		t.Errorf("expected only extract_pdf python skill, got %+v", out.Skills)
	}
	if len(out.MarkdownSkills) != 0 {
		t.Errorf("expected no markdown skills (all disabled), got %+v", out.MarkdownSkills)
	}
}
