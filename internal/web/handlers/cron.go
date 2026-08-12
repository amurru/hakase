// Package handlers provides HTTP handlers for the hakase web API.
package handlers

import (
	"net/http"
	"sort"
	"time"

	"amurru/hakase/internal/cli"
	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

// CronJobDTO is the API response for a cron job.
type CronJobDTO struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Prompt     string   `json:"prompt,omitempty"`
	Schedule   string   `json:"schedule"`
	Skills     []string `json:"skills,omitempty"`
	Repeat     int      `json:"repeat,omitempty"`
	State      string   `json:"state"`
	Enabled    bool     `json:"enabled"`
	Native     string   `json:"native,omitempty"`
	NextRunAt  *string  `json:"next_run_at,omitempty"`
	LastRunAt  *string  `json:"last_run_at,omitempty"`
	LastStatus string   `json:"last_status,omitempty"`
	RunCount   int      `json:"run_count"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// API struct
// ---------------------------------------------------------------------------

// CronAPI wraps cron job operations for the web API layer.
type CronAPI struct{}

// CronRoutes is the minimum interface needed by RegisterCronRoutes.
type CronRoutes interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}

// RegisterCronRoutes registers all cron API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterCronRoutes(r CronRoutes) {
	api := &CronAPI{}

	r.Get("/cron/jobs", api.ListJobs)
	r.Post("/cron/jobs/{id}/pause", api.PauseJob)
	r.Post("/cron/jobs/{id}/resume", api.ResumeJob)
	r.Post("/cron/jobs/{id}/run", api.RunJob)
}

// cronJobID extracts the {id} URL parameter from the request.
func cronJobID(r *http.Request) string {
	return chi.URLParam(r, "id")
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// ListJobs handles GET /cron/jobs - returns all cron jobs sorted by next run.
func (api *CronAPI) ListJobs(w http.ResponseWriter, r *http.Request) {
	reg, err := cli.CronLoadRegistry()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Sort by NextRunAt: nil last, otherwise chronological.
	sort.Slice(reg.Jobs, func(i, j int) bool {
		ni, nj := reg.Jobs[i].NextRunAt, reg.Jobs[j].NextRunAt
		if ni == nil && nj == nil {
			return false
		}
		if ni == nil {
			return false
		}
		if nj == nil {
			return true
		}
		return ni.Before(*nj)
	})

	dtos := make([]CronJobDTO, 0, len(reg.Jobs))
	for _, job := range reg.Jobs {
		dtos = append(dtos, cronJobToDTO(job))
	}

	writeJSON(w, http.StatusOK, dtos)
}

// PauseJob handles POST /cron/jobs/{id}/pause - pauses a cron job.
func (api *CronAPI) PauseJob(w http.ResponseWriter, r *http.Request) {
	id := cronJobID(r)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "job id is required"})
		return
	}

	reg, err := cli.CronLoadRegistry()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	job, err := cli.CronGetJob(reg, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	job.State = cli.CronStatePaused
	job.Enabled = false
	job.NextRunAt = nil
	job.UpdatedAt = time.Now().UTC()

	if err := cli.CronSaveRegistry(reg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, cronJobToDTO(*job))
}

// ResumeJob handles POST /cron/jobs/{id}/resume - resumes a paused cron job.
func (api *CronAPI) ResumeJob(w http.ResponseWriter, r *http.Request) {
	id := cronJobID(r)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "job id is required"})
		return
	}

	reg, err := cli.CronLoadRegistry()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	job, err := cli.CronGetJob(reg, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	if job.State == cli.CronStateCompleted {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot resume a completed job"})
		return
	}

	next, err := cli.CronParseSchedule(job.Schedule)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot parse stored schedule: " + err.Error()})
		return
	}

	job.State = cli.CronStateScheduled
	job.Enabled = true
	job.NextRunAt = &next
	job.UpdatedAt = time.Now().UTC()

	if err := cli.CronSaveRegistry(reg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, cronJobToDTO(*job))
}

// RunJob handles POST /cron/jobs/{id}/run - triggers a cron job immediately.
func (api *CronAPI) RunJob(w http.ResponseWriter, r *http.Request) {
	id := cronJobID(r)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "job id is required"})
		return
	}

	job, err := cli.CronTriggerJob(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, cronJobToDTO(job))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// cronJobToDTO converts an internal CronJob to a CronJobDTO.
func cronJobToDTO(job cli.CronJob) CronJobDTO {
	dto := CronJobDTO{
		ID:         job.ID,
		Name:       job.Name,
		Prompt:     job.Prompt,
		Schedule:   job.Schedule,
		Skills:     job.Skills,
		Repeat:     job.Repeat,
		State:      string(job.State),
		Enabled:    job.Enabled,
		Native:     job.Native,
		LastStatus: job.LastStatus,
		RunCount:   job.RunCount,
		CreatedAt:  job.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  job.UpdatedAt.Format(time.RFC3339),
	}
	if job.NextRunAt != nil {
		s := job.NextRunAt.Format(time.RFC3339)
		dto.NextRunAt = &s
	}
	if job.LastRunAt != nil {
		s := job.LastRunAt.Format(time.RFC3339)
		dto.LastRunAt = &s
	}
	return dto
}
