// Package handlers provides HTTP handlers for the hakase web API.
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"amurru/hakase/internal/knowledge"
	"github.com/go-chi/chi/v5"
)

// KnowledgeRouter is the minimum interface needed by RegisterKnowledgeRoutes.
type KnowledgeRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
	Patch(pattern string, handlerFn http.HandlerFunc)
}

// KnowledgeDTO is the API response for a knowledge note.
type KnowledgeDTO struct {
	Slug        string                       `json:"slug"`
	Title       string                       `json:"title"`
	Summary     string                       `json:"summary,omitempty"`
	Status      string                       `json:"status,omitempty"`
	Confidence  string                       `json:"confidence,omitempty"`
	Tags        []string                     `json:"tags,omitempty"`
	Aliases     []string                     `json:"aliases,omitempty"`
	Created     string                       `json:"created,omitempty"`
	Updated     string                       `json:"updated,omitempty"`
	Sources     []KnowledgeSourceDTO         `json:"sources,omitempty"`
	Related     []string                     `json:"related,omitempty"`
	Metadata    map[string]string            `json:"metadata,omitempty"`
	Body        string                       `json:"body,omitempty"`
	Backlinks   []string                     `json:"backlinks,omitempty"`
	Dangling    []string                     `json:"dangling,omitempty"`
}

// KnowledgeSourceDTO is a source reference for a knowledge note.
type KnowledgeSourceDTO struct {
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
}

// KnowledgeSearchResultDTO is a scored search result.
type KnowledgeSearchResultDTO struct {
	Note     KnowledgeDTO `json:"note"`
	Score    float64      `json:"score"`
	Snippet  string       `json:"snippet,omitempty"`
}

// KnowledgeCreateRequest is the request body for POST /api/knowledge.
type KnowledgeCreateRequest struct {
	Title   string   `json:"title"`
	Body    string   `json:"body,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Status  string   `json:"status,omitempty"`
	Summary string   `json:"summary,omitempty"`
}

// KnowledgeUpdateRequest is the request body for PATCH /api/knowledge/{slug}.
type KnowledgeUpdateRequest struct {
	Title    string            `json:"title,omitempty"`
	Body     string            `json:"body,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
	Status   string            `json:"status,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Aliases  []string          `json:"aliases,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// KnowledgeAPI wraps knowledge operations for the web API layer.
type KnowledgeAPI struct {
	dir string
}

// RegisterKnowledgeRoutes registers all knowledge API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
// Search must be registered before {slug} to avoid route conflicts.
func RegisterKnowledgeRoutes(r KnowledgeRouter, knowledgeDir string) {
	api := &KnowledgeAPI{dir: knowledgeDir}

	// Search must come before the {slug} catch-all.
	r.Get("/knowledge/search", api.SearchNotes)
	r.Get("/knowledge", api.ListNotes)
	r.Get("/knowledge/{slug}", api.ReadNote)
	r.Post("/knowledge", api.CreateNote)
	r.Patch("/knowledge/{slug}", api.UpdateNote)
}

// slug extracts the {slug} URL parameter from the request.
func knowledgeSlug(r *http.Request) string {
	return chi.URLParam(r, "slug")
}

// noteToDTO converts an internal KnowledgeNote to a KnowledgeDTO.
func noteToDTO(n *knowledge.KnowledgeNote) KnowledgeDTO {
	dto := KnowledgeDTO{
		Slug:      n.Slug,
		Title:     n.Frontmatter.Title,
		Summary:   n.Frontmatter.Summary,
		Status:    n.Frontmatter.Status,
		Confidence: n.Frontmatter.Confidence,
		Tags:      n.Frontmatter.Tags,
		Aliases:   n.Frontmatter.Aliases,
		Created:   n.Frontmatter.Created,
		Updated:   n.Frontmatter.Updated,
		Related:   n.Frontmatter.Related,
		Metadata:  n.Frontmatter.Metadata,
		Body:      n.Body,
	}
	if len(n.Frontmatter.Sources) > 0 {
		dto.Sources = make([]KnowledgeSourceDTO, 0, len(n.Frontmatter.Sources))
		for _, s := range n.Frontmatter.Sources {
			dto.Sources = append(dto.Sources, KnowledgeSourceDTO{URL: s.URL, Path: s.Path})
		}
	}
	return dto
}

// ListNotes handles GET /knowledge - returns all notes.
func (api *KnowledgeAPI) ListNotes(w http.ResponseWriter, r *http.Request) {
	idx, err := knowledge.GetKnowledgeIndex(api.dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to build index: %v", err)})
		return
	}

	// Collect and sort by title.
	var notes []*knowledge.KnowledgeNote
	for _, n := range idx.BySlug {
		notes = append(notes, n)
	}
	for i := 0; i < len(notes); i++ {
		for j := i + 1; j < len(notes); j++ {
			if notes[i].Frontmatter.Title > notes[j].Frontmatter.Title {
				notes[i], notes[j] = notes[j], notes[i]
			}
		}
	}

	dtos := make([]KnowledgeDTO, 0, len(notes))
	for _, n := range notes {
		dto := noteToDTO(n)
		dto.Backlinks = idx.Backlinks[n.Slug]
		dtos = append(dtos, dto)
	}

	writeJSON(w, http.StatusOK, dtos)
}

// ReadNote handles GET /knowledge/{slug} - returns a single note.
func (api *KnowledgeAPI) ReadNote(w http.ResponseWriter, r *http.Request) {
	slug := knowledgeSlug(r)
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug is required"})
		return
	}

	idx, err := knowledge.GetKnowledgeIndex(api.dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to build index: %v", err)})
		return
	}

	note, ok := knowledge.ResolveTarget(idx, slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("note %q not found", slug)})
		return
	}

	dto := noteToDTO(note)
	dto.Backlinks = idx.Backlinks[note.Slug]

	// Find dangling links from this note.
	outlinks := knowledge.ExtractWikilinks(note.Body)
	for _, target := range outlinks {
		if _, resolved := knowledge.ResolveTarget(idx, target); !resolved {
			dto.Dangling = append(dto.Dangling, target)
		}
	}

	writeJSON(w, http.StatusOK, dto)
}

// SearchNotes handles GET /knowledge/search?q=<query> - searches notes.
func (api *KnowledgeAPI) SearchNotes(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q parameter is required"})
		return
	}

	tagsStr := r.URL.Query().Get("tags")
	var tags []string
	if tagsStr != "" {
		for _, t := range strings.Split(tagsStr, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}

	idx, err := knowledge.GetKnowledgeIndex(api.dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to build index: %v", err)})
		return
	}

	results := knowledge.SearchKnowledgeScored(idx, query, tags, false)
	dtos := make([]KnowledgeSearchResultDTO, 0, len(results))
	for _, s := range results {
		dto := noteToDTO(&s.Note)
		snippet := knowledge.FirstSnippet(s.Note.Body, query)
		dtos = append(dtos, KnowledgeSearchResultDTO{
			Note:    dto,
			Score:   s.Score,
			Snippet: snippet,
		})
	}

	writeJSON(w, http.StatusOK, dtos)
}

// CreateNote handles POST /knowledge - creates a new note.
func (api *KnowledgeAPI) CreateNote(w http.ResponseWriter, r *http.Request) {
	var req KnowledgeCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}

	slug := knowledge.Slugify(req.Title)
	if slug == "note" && req.Title != "note" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title produced invalid slug"})
		return
	}

	// Check if note already exists.
	idx, err := knowledge.GetKnowledgeIndex(api.dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to build index: %v", err)})
		return
	}
	if _, ok := idx.BySlug[slug]; ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("note %q already exists", slug)})
		return
	}

	today := time.Now().Format("2006-01-02")
	body := req.Body
	if body == "" {
		body = fmt.Sprintf("# %s\n\n", req.Title)
	}

	status := req.Status
	if status == "" {
		status = "draft"
	}

	note := &knowledge.KnowledgeNote{
		Slug: slug,
		Path: knowledge.NotePath(api.dir, slug),
		Frontmatter: knowledge.KnowledgeFrontmatter{
			Title:   req.Title,
			Tags:    req.Tags,
			Created: today,
			Updated: today,
			Status:  status,
			Summary: req.Summary,
		},
		Body: body,
	}
	knowledge.SerializeNote(note)

	if err := knowledge.SaveNote(api.dir, note); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to save note: %v", err)})
		return
	}

	// Rebuild index and update log.
	if newIdx, err := knowledge.BuildKnowledgeIndex(api.dir); err == nil {
		_ = knowledge.UpdateIndexFile(api.dir, newIdx)
	}
	_ = knowledge.AppendLog(api.dir, "create", req.Title)
	knowledge.InvalidateKnowledgeCache(api.dir)

	writeJSON(w, http.StatusCreated, noteToDTO(note))
}

// UpdateNote handles PATCH /knowledge/{slug} - updates an existing note.
func (api *KnowledgeAPI) UpdateNote(w http.ResponseWriter, r *http.Request) {
	slug := knowledgeSlug(r)
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug is required"})
		return
	}

	var req KnowledgeUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	idx, err := knowledge.GetKnowledgeIndex(api.dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to build index: %v", err)})
		return
	}

	note, ok := knowledge.ResolveTarget(idx, slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("note %q not found", slug)})
		return
	}

	// Apply partial updates.
	if req.Title != "" {
		note.Frontmatter.Title = req.Title
	}
	if req.Body != "" {
		note.Body = req.Body
	}
	if req.Tags != nil {
		note.Frontmatter.Tags = req.Tags
	}
	if req.Status != "" {
		note.Frontmatter.Status = req.Status
	}
	if req.Summary != "" {
		note.Frontmatter.Summary = req.Summary
	}
	if req.Aliases != nil {
		note.Frontmatter.Aliases = req.Aliases
	}
	if req.Metadata != nil {
		note.Frontmatter.Metadata = req.Metadata
	}
	note.Frontmatter.Updated = time.Now().Format("2006-01-02")
	knowledge.SerializeNote(note)

	if err := knowledge.UpdateNote(api.dir, note); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to update note: %v", err)})
		return
	}

	// Rebuild index and update log.
	if newIdx, err := knowledge.BuildKnowledgeIndex(api.dir); err == nil {
		_ = knowledge.UpdateIndexFile(api.dir, newIdx)
	}
	_ = knowledge.AppendLog(api.dir, "update", note.Frontmatter.Title)
	knowledge.InvalidateKnowledgeCache(api.dir)

	writeJSON(w, http.StatusOK, noteToDTO(note))
}
