// Package handlers provides HTTP handlers for the hakase web API.
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	hakaseagent "amurru/hakase/internal/agent"
	"github.com/go-chi/chi/v5"
)

// TaskDTO is the API response for a task.
type TaskDTO struct {
	ID           string                     `json:"id"`
	Version      int                        `json:"version"`
	Title        string                     `json:"title"`
	Description  string                     `json:"description,omitempty"`
	Status       string                     `json:"status"`
	Priority     string                     `json:"priority"`
	Owner        string                     `json:"owner,omitempty"`
	Assignee     string                     `json:"assignee,omitempty"`
	Dependencies []string                   `json:"dependencies,omitempty"`
	BlockedBy    []string                   `json:"blocked_by,omitempty"`
	CreatedAt    string                     `json:"created_at"`
	UpdatedAt    string                     `json:"updated_at"`
	StartedAt    string                     `json:"started_at,omitempty"`
	CompletedAt  string                     `json:"completed_at,omitempty"`
	Attempts     int                        `json:"attempts"`
	MaxAttempts  int                        `json:"max_attempts"`
	LastError    string                     `json:"last_error,omitempty"`
	ParentID     string                     `json:"parent_id,omitempty"`
	Tags         []string                   `json:"tags,omitempty"`
	Metadata     map[string]any             `json:"metadata,omitempty"`
}

// TaskAPI wraps task CRUD operations for the web API layer.
// The underlying agent functions (CreateTask, UpdateTask, etc.) are already
// thread-safe via taskRegistryMu, so no additional mutex is needed here.
type TaskAPI struct{}

// TasksRouter is the minimum interface needed by RegisterTaskRoutes.
type TasksRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
	Patch(pattern string, handlerFn http.HandlerFunc)
}

// RegisterTaskRoutes registers all task API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
func RegisterTaskRoutes(r TasksRouter) {
	api := &TaskAPI{}

	r.Get("/tasks", api.ListTasks)
	r.Post("/tasks", api.CreateTask)
	r.Patch("/tasks/{id}", api.UpdateTask)
	r.Delete("/tasks/{id}", api.DeleteTask)
}

// taskID extracts the {id} URL parameter from the request.
func taskID(r *http.Request) string {
	return chi.URLParam(r, "id")
}

// ListTasks handles GET /tasks - returns all tasks.
func (api *TaskAPI) ListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := hakaseagent.ListTasks(hakaseagent.ListTasksInput{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	dtos := make([]TaskDTO, 0, len(tasks))
	for _, t := range tasks {
		dtos = append(dtos, taskToDTO(t))
	}

	writeJSON(w, http.StatusOK, dtos)
}

// CreateTask handles POST /tasks - creates a new task.
// Accepts JSON body: {title, description?, priority?, assignee?, dependencies?, tags?, parent_id?}
func (api *TaskAPI) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title        string   `json:"title"`
		Description  string   `json:"description"`
		Priority     string   `json:"priority"`
		Assignee     string   `json:"assignee"`
		Dependencies []string `json:"dependencies"`
		Tags         []string `json:"tags"`
		ParentID     string   `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}

	priority := hakaseagent.TaskPriorityMedium
	if req.Priority != "" {
		priority = hakaseagent.TaskPriority(req.Priority)
	}

	input := hakaseagent.CreateTaskInput{
		Title:        req.Title,
		Description:  req.Description,
		Priority:     priority,
		Assignee:     req.Assignee,
		Dependencies: req.Dependencies,
		Tags:         req.Tags,
		ParentID:     req.ParentID,
	}

	task, err := hakaseagent.CreateTask(input)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, taskToDTO(task))
}

// UpdateTask handles PATCH /tasks/{id} - updates a task (partial update).
// Accepts JSON body: {title?, description?, status?, priority?, assignee?, error?}
func (api *TaskAPI) UpdateTask(w http.ResponseWriter, r *http.Request) {
	id := taskID(r)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "task id is required"})
		return
	}

	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Status      string `json:"status"`
		Priority    string `json:"priority"`
		Assignee    string `json:"assignee"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	input := hakaseagent.UpdateTaskInput{ID: id}
	if req.Title != "" {
		input.Title = req.Title
	}
	if req.Description != "" {
		input.Description = req.Description
	}
	if req.Status != "" {
		input.Status = hakaseagent.TaskStatus(req.Status)
	}
	if req.Priority != "" {
		input.Priority = hakaseagent.TaskPriority(req.Priority)
	}
	if req.Assignee != "" {
		input.Assignee = req.Assignee
	}
	if req.Error != "" {
		input.Error = req.Error
	}

	task, err := hakaseagent.UpdateTask(input)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
			return
		}
		if strings.Contains(errMsg, "invalid transition") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": errMsg})
		return
	}

	writeJSON(w, http.StatusOK, taskToDTO(task))
}

// DeleteTask handles DELETE /tasks/{id} - deletes a task.
func (api *TaskAPI) DeleteTask(w http.ResponseWriter, r *http.Request) {
	id := taskID(r)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "task id is required"})
		return
	}

	success, err := hakaseagent.DeleteTask(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !success {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// taskToDTO converts an internal TaskMeta to a TaskDTO.
func taskToDTO(t hakaseagent.TaskMeta) TaskDTO {
	dto := TaskDTO{
		ID:           t.ID,
		Version:      t.Version,
		Title:        t.Title,
		Description:  t.Description,
		Status:       string(t.Status),
		Priority:     string(t.Priority),
		Owner:        t.Owner,
		Assignee:     t.Assignee,
		Dependencies: t.Dependencies,
		BlockedBy:    t.BlockedBy,
		CreatedAt:    t.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:    t.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		Attempts:     t.Attempts,
		MaxAttempts:  t.MaxAttempts,
		LastError:    t.LastError,
		ParentID:     t.ParentID,
		Tags:         t.Tags,
		Metadata:     t.Metadata,
	}
	if t.StartedAt != nil {
		dto.StartedAt = t.StartedAt.Format("2006-01-02T15:04:05Z")
	}
	if t.CompletedAt != nil {
		dto.CompletedAt = t.CompletedAt.Format("2006-01-02T15:04:05Z")
	}
	return dto
}


