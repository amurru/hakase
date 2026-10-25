// skillsconfine_test.go - S28 deny-first + skills confinement tests.
package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"

	"google.golang.org/adk/v2/agent"
)

// runFileTool drives one file-ops tool like the ADK would, returning the
// decoded output or the tool error. sandboxRoot "" keeps taskResolve on
// plain absolute resolution (no sandbox override in these tests).
func runFileTool(t *testing.T, tools []toolAlias, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() != name {
			continue
		}
		result, err := tl.Run(agent.NewContext(&agent.ContextMock{}), args)
		if err != nil {
			return nil, err
		}
		data, merr := json.Marshal(result)
		if merr != nil {
			t.Fatalf("marshal result: %v", merr)
		}
		var out map[string]any
		if merr := json.Unmarshal(data, &out); merr != nil {
			t.Fatalf("unmarshal result: %v", merr)
		}
		return out, nil
	}
	t.Fatalf("tool %q not found", name)
	return nil, nil
}

// toolAlias is the minimal runnable surface of an ADK function tool.
type toolAlias interface {
	Name() string
	Run(ctx agent.Context, args any) (map[string]any, error)
}

// fileOpsTools builds the toolset with no session scoping for tests.
func fileOpsTools(t *testing.T) []toolAlias {
	t.Helper()
	raw, err := CreateFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatalf("CreateFileOpsTools: %v", err)
	}
	out := make([]toolAlias, 0, len(raw))
	for _, tl := range raw {
		r, ok := tl.(interface {
			Name() string
			Run(ctx agent.Context, args any) (map[string]any, error)
		})
		if !ok {
			t.Fatalf("tool %T does not expose Run", tl)
		}
		out = append(out, r)
	}
	return out
}

// TestS28DenyFirstOrdering pins deny-first: when an allow rule and a deny
// rule both match a write path, the deny wins (deny > ask > allow), and
// the refusal names the policy.
func TestS28DenyFirstOrdering(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "blocked.txt")
	cp, err := permissions.Compile(permissions.Policy{Default: "allow", Rules: []permissions.Rule{
		{Action: "edit", Resource: filepath.Join(dir, "*"), Effect: permissions.EffectAllow},
		{Action: "edit", Resource: filepath.Join(dir, "blocked.txt"), Effect: permissions.EffectDeny},
	}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })

	if _, err := taskResolve(nil, target, true, ""); err == nil {
		t.Fatal("deny+allow match: want denial (deny-first)")
	} else if !strings.Contains(err.Error(), "denied by permissions policy") {
		t.Fatalf("denial must cite the policy, got: %v", err)
	}
	if _, err := taskResolve(nil, filepath.Join(dir, "free.txt"), true, ""); err != nil {
		t.Fatalf("allow-only path must resolve: %v", err)
	}
}

// TestS28WriteFileSkillsConfined pins the confinement: write_file into
// the Python skill library refuses with the save_skill alternative, and
// nothing is written.
func TestS28WriteFileSkillsConfined(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	dir := t.TempDir()
	t.Chdir(dir)
	tools := fileOpsTools(t)
	_, err := runFileTool(t, tools, "write_file", map[string]any{
		"path": filepath.Join(dir, "skills", "evil.py"), "content": "print(1)\n",
	})
	if err == nil {
		t.Fatal("write_file into skills/: want confinement refusal")
	} else if !strings.Contains(err.Error(), "save_skill") {
		t.Fatalf("refusal must name save_skill, got: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "skills", "evil.py")); !os.IsNotExist(serr) {
		t.Fatal("confined write must not create the file")
	}
	// The registry file is confined too.
	if _, err := runFileTool(t, tools, "write_file", map[string]any{
		"path": filepath.Join(dir, "skills", "skills.json"), "content": "{}\n",
	}); err == nil {
		t.Fatal("write_file to skills.json: want confinement refusal")
	}
}

// TestS28PatchSkillsConfined pins the patch leg: patching a skill file
// refuses the same way (deny-first, before any read-modify-write).
func TestS28PatchSkillsConfined(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	dir := t.TempDir()
	t.Chdir(dir)
	skillFile := filepath.Join(dir, "skills", "victim.py")
	if err := os.MkdirAll(filepath.Dir(skillFile), 0755); err != nil {
		t.Fatal(err)
	}
	before := "X = 1\n"
	if err := os.WriteFile(skillFile, []byte(before), 0644); err != nil {
		t.Fatal(err)
	}
	tools := fileOpsTools(t)
	if _, err := runFileTool(t, tools, "patch", map[string]any{
		"path": skillFile, "old_string": "X = 1", "new_string": "X = 2",
	}); err == nil {
		t.Fatal("patch of a skill file: want confinement refusal")
	} else if !strings.Contains(err.Error(), "save_skill") {
		t.Fatalf("refusal must name save_skill, got: %v", err)
	}
	got, _ := os.ReadFile(skillFile)
	if string(got) != before {
		t.Fatal("confined patch must leave the file untouched")
	}
	// M2: a confined path denies before the read - a missing skill file
	// surfaces the confinement refusal (save_skill), not a read error.
	if _, err := runFileTool(t, tools, "patch", map[string]any{
		"path": filepath.Join(dir, "skills", "new.py"), "old_string": "a", "new_string": "b",
	}); err == nil {
		t.Fatal("patch of a missing skill file: want confinement refusal")
	} else if !strings.Contains(err.Error(), "save_skill") {
		t.Fatalf("missing-file refusal must name save_skill, got: %v", err)
	}
}

// TestS28NonSkillsWriteAllowed pins the boundary: ordinary writes and
// non-code files under a skills dir still work (confinement targets
// executable persistence, not documentation).
func TestS28NonSkillsWriteAllowed(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	dir := t.TempDir()
	t.Chdir(dir)
	tools := fileOpsTools(t)
	out, err := runFileTool(t, tools, "write_file", map[string]any{
		"path": filepath.Join(dir, "notes.txt"), "content": "hello\n",
	})
	if err != nil {
		t.Fatalf("ordinary write must succeed: %v", err)
	}
	if out["bytes_written"] == float64(0) {
		t.Fatal("ordinary write must record bytes")
	}
	if _, err := runFileTool(t, tools, "write_file", map[string]any{
		"path": filepath.Join(dir, "skills", "README.md"), "content": "# docs\n",
	}); err != nil {
		t.Fatalf("non-code file under skills/ must succeed: %v", err)
	}
}

// TestS28NestedSkillsConfined pins M4: any path element named skills
// confines executable persistence - nested trees, .pyc/.pyd, and the
// registry/init files refuse with the save_skill alternative.
func TestS28NestedSkillsConfined(t *testing.T) {
	for _, p := range []string{
		"/ws/skills/subdir/nested.py",
		"/ws/a/skills/b/c/deep.pyc",
		"/ws/skills/mod.pyd",
		"/ws/x/skills/skills.json",
		"/ws/x/skills/__init__.py",
		"/ws/x/skills/__init__.pyc",
		"/WS/SKILLS/upper.PY",
	} {
		if err := denySkillsWrite(p); err == nil {
			t.Errorf("nested skills path %q: want confinement", p)
		} else if !strings.Contains(err.Error(), "save_skill") {
			t.Errorf("nested denial %q must name save_skill, got: %v", p, err)
		}
	}
	for _, p := range []string{
		"/ws/skills/README.md",
		"/ws/skills/fixture.json",
		"/ws/skills/subdir/notes.txt",
		"/ws/other/nonskills/file.py",
		"/ws/notes.txt",
	} {
		if err := denySkillsWrite(p); err != nil {
			t.Errorf("non-code path %q must stay writable, got: %v", p, err)
		}
	}
}

// TestS28DenyBeforeStat pins deny-first ordering: a confined skills path
// denies even when the file does not exist (no Stat bypass, deny wins
// before MkdirAll and before any write).
func TestS28DenyBeforeStat(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	dir := t.TempDir()
	t.Chdir(dir)
	tools := fileOpsTools(t)
	missing := filepath.Join(dir, "skills", "newdir", "fresh.py")
	if _, err := runFileTool(t, tools, "write_file", map[string]any{
		"path": missing, "content": "x=1\n",
	}); err == nil {
		t.Fatal("confined missing path: want denial")
	} else if !strings.Contains(err.Error(), "save_skill") {
		t.Fatalf("denial must name save_skill (deny-first), got: %v", err)
	}
	if _, serr := os.Stat(missing); !os.IsNotExist(serr) {
		t.Fatal("denied write must not create parents or files")
	}
}
