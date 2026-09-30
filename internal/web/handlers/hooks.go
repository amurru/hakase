// hooks.go exposes tool-lifecycle hooks over the web API so the UI can
// inspect both layers and review/trust project hooks without dropping to
// the CLI. Reads use the live agent runner (compiled user layer); project
// resolution is per-session (registered-project checkout) with a server-cwd
// fallback, mirroring the CLI.
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/hooks"
	"amurru/hakase/internal/project"
	hakasesession "amurru/hakase/internal/session"
)

// HooksRouter is the minimum interface needed by RegisterHooksRoutes.
type HooksRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}

// HookSnapshotDTO mirrors one hook handler for the API.
type HookSnapshotDTO struct {
	Event       string   `json:"event"`
	Matcher     string   `json:"matcher"`
	Name        string   `json:"name"`
	Command     []string `json:"command"`
	Timeout     int      `json:"timeout"`
	OnFailure   string   `json:"on_failure"`
	Fingerprint string   `json:"fingerprint"`
	Layer       string   `json:"layer"`
	Trusted     bool     `json:"trusted"`
}

// HooksProjectDTO is the project layer for one root.
type HooksProjectDTO struct {
	Root  string            `json:"root"`
	File  string            `json:"file"`
	Hooks []HookSnapshotDTO `json:"hooks"`
}

// HooksListDTO is the GET /api/hooks response.
type HooksListDTO struct {
	Enabled bool              `json:"enabled"`
	User    []HookSnapshotDTO `json:"user"`
	Project *HooksProjectDTO  `json:"project,omitempty"`
}

// HooksTrustRequest is POST /api/hooks/trust|untrust.
type HooksTrustRequest struct {
	SessionID    string   `json:"session_id"`
	Fingerprints []string `json:"fingerprints"`
}

// HooksTrustDTO is the trust/untrust response.
type HooksTrustDTO struct {
	Changed []string `json:"changed"`
}

// HooksAPI serves hook inspection and trust over HTTP.
type HooksAPI struct {
	runner      *hooks.Runner
	enabled     bool
	store       *hooks.TrustStore
	resolveRoot func(sessionID string) string
}

// RegisterHooksRoutes registers hooks routes on the given router.
// Routes are relative to /api (the caller places them inside the /api
// group, so trust actions sit behind auth like everything else).
func RegisterHooksRoutes(r HooksRouter, sessions *hakasesession.SessionService) {
	runner := hakaseagent.HooksRunner()
	enabled := false
	if path := resolveConfigPath("config.json"); path != "" {
		if cfg, err := config.LoadConfig(path); err == nil {
			enabled = config.HooksEnabled(cfg)
		}
	}
	// Project resolution mirrors the agent run: a session bound to a
	// registered project resolves to its checkout, otherwise the
	// server's own checkout (local mode).
	driver := agentrun.New(nil, sessions)
	store := hooks.OpenDefaultTrustStore()
	api := &HooksAPI{
		runner:  runner,
		enabled: enabled,
		store:   store,
		resolveRoot: func(sessionID string) string {
			if sessionID != "" {
				if root := driver.ProjectRoot(sessionID); root != "" {
					return root
				}
			}
			if cwd, err := os.Getwd(); err == nil {
				return project.FindRoot(cwd)
			}
			return ""
		},
	}
	r.Get("/hooks", api.List)
	r.Post("/hooks/trust", api.Trust)
	r.Post("/hooks/untrust", api.Untrust)
}

func toSnapshotDTO(s hooks.Snapshot) HookSnapshotDTO {
	return HookSnapshotDTO{
		Event: s.Event, Matcher: s.Matcher, Name: s.Name, Command: s.Command,
		Timeout: s.Timeout, OnFailure: s.OnFailure, Fingerprint: s.Fingerprint,
		Layer: s.Layer, Trusted: s.Trusted,
	}
}

// List handles GET /api/hooks[?session_id=...] - user hooks plus the
// project layer (with trust status) for the resolved root.
func (api *HooksAPI) List(w http.ResponseWriter, r *http.Request) {
	dto := HooksListDTO{Enabled: api.enabled, User: make([]HookSnapshotDTO, 0)}
	if api.runner != nil {
		for _, s := range api.runner.Snapshots() {
			dto.User = append(dto.User, toSnapshotDTO(s))
		}
	}
	root := ""
	if api.resolveRoot != nil {
		root = api.resolveRoot(r.URL.Query().Get("session_id"))
	}
	if root != "" && api.runner != nil {
		if path := hooks.ProjectHooksPath(root); path != "" {
			if _, err := os.Stat(path); err == nil {
				pdto := &HooksProjectDTO{Root: root, File: path, Hooks: make([]HookSnapshotDTO, 0)}
				for _, s := range api.runner.ProjectSnapshots(root) {
					pdto.Hooks = append(pdto.Hooks, toSnapshotDTO(s))
				}
				dto.Project = pdto
			}
		}
	}
	writeHooksJSON(w, dto)
}

// Trust handles POST /api/hooks/trust - trust exactly the listed
// fingerprints after matching each against the session's CURRENT project
// handlers (unique prefix). Anything unmatched fails the whole request:
// trust must never record a fingerprint the reviewer didn't see.
func (api *HooksAPI) Trust(w http.ResponseWriter, r *http.Request) {
	var req HooksTrustRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeHooksError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Fingerprints) == 0 {
		writeHooksError(w, http.StatusBadRequest, "fingerprints must be non-empty")
		return
	}
	root := ""
	if api.resolveRoot != nil {
		root = api.resolveRoot(req.SessionID)
	}
	if root == "" || api.runner == nil {
		writeHooksError(w, http.StatusBadRequest, "no project hooks scope for this session")
		return
	}
	cands := api.runner.ProjectSnapshots(root)
	if cands == nil {
		writeHooksError(w, http.StatusBadRequest, "no project hooks file (or it is broken)")
		return
	}
	store := api.store
	if store == nil || store.Path() == "" {
		writeHooksError(w, http.StatusInternalServerError, "no trust store (no hakase home resolves)")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var changed []string
	for _, fp := range req.Fingerprints {
		var match *hooks.Snapshot
		matches := 0
		for i := range cands {
			if strings.HasPrefix(cands[i].Fingerprint, fp) {
				matches++
				m := cands[i]
				match = &m
			}
		}
		if matches == 0 {
			writeHooksError(w, http.StatusBadRequest, fmt.Sprintf("no pending hook matches %q", fp))
			return
		}
		if matches > 1 {
			writeHooksError(w, http.StatusBadRequest, fmt.Sprintf("%q is ambiguous (%d matches); use a longer prefix", fp, matches))
			return
		}
		if err := store.Trust(hooks.TrustEntry{
			Fingerprint: match.Fingerprint, Name: match.Name, Event: match.Event,
			Matcher: match.Matcher, Command: match.Command, FirstSeen: now,
		}); err != nil {
			writeHooksError(w, http.StatusInternalServerError, fmt.Sprintf("trust failed: %v", err))
			return
		}
		changed = append(changed, match.Fingerprint)
	}
	writeHooksJSON(w, HooksTrustDTO{Changed: changed})
}

// Untrust handles POST /api/hooks/untrust - revoke the listed fingerprints.
func (api *HooksAPI) Untrust(w http.ResponseWriter, r *http.Request) {
	var req HooksTrustRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeHooksError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Fingerprints) == 0 {
		writeHooksError(w, http.StatusBadRequest, "fingerprints must be non-empty")
		return
	}
	store := api.store
	if store == nil || store.Path() == "" {
		writeHooksError(w, http.StatusInternalServerError, "no trust store (no hakase home resolves)")
		return
	}
	var changed []string
	for _, fp := range req.Fingerprints {
		var full string
		for _, e := range store.List() {
			if strings.HasPrefix(e.Fingerprint, fp) {
				if full != "" {
					writeHooksError(w, http.StatusBadRequest, fmt.Sprintf("%q is ambiguous; use a longer prefix", fp))
					return
				}
				full = e.Fingerprint
			}
		}
		if full == "" {
			writeHooksError(w, http.StatusBadRequest, fmt.Sprintf("no trusted hook matches %q", fp))
			return
		}
		removed, err := store.Untrust(full)
		if err != nil {
			writeHooksError(w, http.StatusInternalServerError, fmt.Sprintf("untrust failed: %v", err))
			return
		}
		if removed {
			changed = append(changed, full)
		}
	}
	writeHooksJSON(w, HooksTrustDTO{Changed: changed})
}

func writeHooksJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeHooksError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
