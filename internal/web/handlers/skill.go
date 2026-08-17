// Package handlers provides HTTP handlers for the hakase web API.
package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"amurru/hakase/internal/skill"
	"github.com/go-chi/chi/v5"
)

// SkillRouter is the minimum interface needed by RegisterSkillRoutes.
type SkillRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}

// SkillDTO is the API response for a skill.
type SkillDTO struct {
	Name        string  `json:"name"`
	Type        string  `json:"type"` // "python" | "markdown"
	Description string  `json:"description"`
	Enabled     bool    `json:"enabled"`
	Path        string  `json:"path,omitempty"`
	FileName    string  `json:"fileName,omitempty"`
	Source      string  `json:"source,omitempty"`
	SavedAt     string  `json:"savedAt,omitempty"`
	Deprecated  bool    `json:"deprecated,omitempty"`
	EvalScore   float64 `json:"evalScore,omitempty"`
}

// SkillAPI wraps skill listing/enable/disable for the web API layer.
type SkillAPI struct {
	cwd       string
	extraDirs []string
}

// RegisterSkillRoutes registers all skill API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterSkillRoutes(r SkillRouter, cwd string, extraDirs []string) {
	api := &SkillAPI{cwd: cwd, extraDirs: extraDirs}

	r.Get("/skills", api.ListSkills)
	r.Post("/skills/{name}/enable", api.EnableSkill)
	r.Post("/skills/{name}/disable", api.DisableSkill)
}

// skillName extracts the {name} URL parameter from the request. It prefers
// the chi route context (normal HTTP flow) and falls back to parsing the path
// so handlers remain unit-testable without a router.
func skillName(r *http.Request) string {
	if name := chi.URLParam(r, "name"); name != "" {
		return name
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, p := range parts {
		if p == "skills" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// ListSkills handles GET /skills - returns all discovered skills (Python +
// markdown) with their enabled/disabled status.
func (api *SkillAPI) ListSkills(w http.ResponseWriter, r *http.Request) {
	disabled := skill.DisabledSkillsSet()

	dtos := make([]SkillDTO, 0, 16)

	// Python skills from the project library.
	registryPath := filepath.Join(api.cwd, "skills", "skills.json")
	if data, err := os.ReadFile(registryPath); err == nil {
		var reg skill.SkillRegistry
		if err := json.Unmarshal(data, &reg); err == nil {
			for _, s := range reg.Skills {
				dtos = append(dtos, SkillDTO{
					Name:        s.Name,
					Type:        skill.KindPython,
					Description: s.Description,
					Enabled:     !disabled[skill.SkillKey(skill.KindPython, s.Name)],
					FileName:    s.FileName,
					Path:        filepath.Join(api.cwd, "skills", s.FileName),
					SavedAt:     s.SavedAt,
					Deprecated:  s.Deprecated,
					EvalScore:   s.EvalScore,
				})
			}
		}
	}

	// Markdown skills from discovery.
	mdSkills := skill.DiscoverMarkdownSkills(api.cwd, api.extraDirs, nil)
	for _, s := range mdSkills {
		dtos = append(dtos, SkillDTO{
			Name:        s.Frontmatter.Name,
			Type:        skill.KindMarkdown,
			Description: s.Frontmatter.Description,
			Enabled:     !disabled[skill.SkillKey(skill.KindMarkdown, s.Frontmatter.Name)],
			Path:        s.Path,
			Source:      s.Source,
		})
	}

	sort.Slice(dtos, func(i, j int) bool { return dtos[i].Name < dtos[j].Name })

	writeJSON(w, http.StatusOK, dtos)
}

// skillKindFromRequest returns the skill kind from the "type" query
// parameter, validated against the known kinds. Empty return means invalid.
func skillKindFromRequest(r *http.Request) string {
	kind := r.URL.Query().Get("type")
	if kind != skill.KindPython && kind != skill.KindMarkdown {
		return ""
	}
	return kind
}

// EnableSkill handles POST /skills/{name}/enable?type=<kind> - enables a skill.
func (api *SkillAPI) EnableSkill(w http.ResponseWriter, r *http.Request) {
	name := skillName(r)
	kind := skillKindFromRequest(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill name is required"})
		return
	}
	if kind == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill type must be 'python' or 'markdown'"})
		return
	}

	if err := skill.SetSkillDisabled(kind, name, false); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "enabled", "name": name})
}

// DisableSkill handles POST /skills/{name}/disable?type=<kind> - disables a skill.
func (api *SkillAPI) DisableSkill(w http.ResponseWriter, r *http.Request) {
	name := skillName(r)
	kind := skillKindFromRequest(r)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill name is required"})
		return
	}
	if kind == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill type must be 'python' or 'markdown'"})
		return
	}

	if err := skill.SetSkillDisabled(kind, name, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled", "name": name})
}