// skill_test.go - tests for the skills web API handler (list/enable/disable).
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"amurru/hakase/internal/skill"
)

// setupSkillHandlerTest isolates HAKASE_HOME (skill state) and HOME (markdown
// discovery) to temp dirs, and provisions a git-rooted fake project with a
// python registry plus one markdown skill.
func setupSkillHandlerTest(t *testing.T) string {
	t.Helper()
	t.Setenv("HAKASE_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(project, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}

	reg := `{"skills":[{"name":"alpha","description":"Alpha skill","fileName":"alpha.py","savedAt":"2026-01-01T00:00:00Z"},{"name":"beta","description":"Beta skill","fileName":"beta.py"}]}`
	if err := os.WriteFile(filepath.Join(project, "skills", "skills.json"), []byte(reg), 0o644); err != nil {
		t.Fatalf("write skills.json: %v", err)
	}

	mdDir := filepath.Join(project, ".agents", "skills", "gamma")
	if err := os.MkdirAll(mdDir, 0o755); err != nil {
		t.Fatalf("mkdir markdown skill: %v", err)
	}
	md := "---\nname: gamma\nlicense: MIT\ndescription: Gamma markdown skill\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(mdDir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	return project
}

func TestSkillList(t *testing.T) {
	project := setupSkillHandlerTest(t)
	api := &SkillAPI{cwd: project}

	req := httptest.NewRequest("GET", "/skills", nil)
	w := httptest.NewRecorder()
	api.ListSkills(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var skills []SkillDTO
	if err := json.NewDecoder(w.Body).Decode(&skills); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills (2 python + 1 markdown), got %+v", skills)
	}

	byName := map[string]SkillDTO{}
	for _, s := range skills {
		byName[s.Name] = s
		if !s.Enabled {
			t.Errorf("skill %q should be enabled by default", s.Name)
		}
	}
	if byName["alpha"].Type != "python" {
		t.Errorf("alpha type: expected python, got %q", byName["alpha"].Type)
	}
	if byName["gamma"].Type != "markdown" {
		t.Errorf("gamma type: expected markdown, got %q", byName["gamma"].Type)
	}
	if byName["gamma"].Description != "Gamma markdown skill" {
		t.Errorf("gamma description: got %q", byName["gamma"].Description)
	}
}

func TestSkillEnableDisableRoundTrip(t *testing.T) {
	project := setupSkillHandlerTest(t)
	api := &SkillAPI{cwd: project}

	// Disable alpha (python kind).
	req := httptest.NewRequest("POST", "/skills/alpha/disable?type=python", nil)
	w := httptest.NewRecorder()
	api.DisableSkill(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !skill.IsSkillDisabled(skill.KindPython, "alpha") {
		t.Fatal("alpha should be disabled after handler call")
	}

	// List must reflect it.
	req = httptest.NewRequest("GET", "/skills", nil)
	w = httptest.NewRecorder()
	api.ListSkills(w, req)
	var skills []SkillDTO
	if err := json.NewDecoder(w.Body).Decode(&skills); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, s := range skills {
		want := !(s.Name == "alpha" && s.Type == skill.KindPython)
		if s.Enabled != want {
			t.Errorf("skill %q (%s) enabled=%v, want %v", s.Name, s.Type, s.Enabled, want)
		}
	}

	// Re-enable alpha.
	req = httptest.NewRequest("POST", "/skills/alpha/enable?type=python", nil)
	w = httptest.NewRecorder()
	api.EnableSkill(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enable: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if skill.IsSkillDisabled(skill.KindPython, "alpha") {
		t.Fatal("alpha should be enabled after re-enable")
	}
}

func TestSkillDisableWrongType(t *testing.T) {
	setupSkillHandlerTest(t)
	api := &SkillAPI{cwd: t.TempDir()}

	req := httptest.NewRequest("POST", "/skills/alpha/disable?type=not-a-kind", nil)
	w := httptest.NewRecorder()
	api.DisableSkill(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid type, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSkillEnableMissingName(t *testing.T) {
	setupSkillHandlerTest(t)
	api := &SkillAPI{cwd: t.TempDir()}

	req := httptest.NewRequest("POST", "/skills//enable", nil)
	w := httptest.NewRecorder()
	api.EnableSkill(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSkillRouterRegistration(t *testing.T) {
	var patterns []string
	fr := &recordingSkillRouter{patterns: &patterns}

	RegisterSkillRoutes(fr, ".", nil)

	want := []string{
		"GET /skills",
		"POST /skills/{name}/enable",
		"POST /skills/{name}/disable",
	}
	for _, w := range want {
		found := false
		for _, p := range patterns {
			if p == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("route %q not registered; got %v", w, patterns)
		}
	}
}

type recordingSkillRouter struct {
	patterns *[]string
}

func (r *recordingSkillRouter) Get(pattern string, h http.HandlerFunc) {
	*r.patterns = append(*r.patterns, "GET "+pattern)
}
func (r *recordingSkillRouter) Post(pattern string, h http.HandlerFunc) {
	*r.patterns = append(*r.patterns, "POST "+pattern)
}