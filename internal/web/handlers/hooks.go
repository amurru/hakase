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
	Enabled     bool     `json:"enabled"`
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

// HooksUserAddRequest is POST /api/hooks/user/add.
type HooksUserAddRequest struct {
	Event     string   `json:"event"`
	Matcher   string   `json:"matcher"`
	Name      string   `json:"name"`
	Command   []string `json:"command"`
	Timeout   int      `json:"timeout"`
	OnFailure string   `json:"on_failure"`
}

// HooksUserSetEnabledRequest is POST /api/hooks/user/set-enabled.
type HooksUserSetEnabledRequest struct {
	Fingerprints []string `json:"fingerprints"`
	Enabled      bool     `json:"enabled"`
}

// HooksUserUpdateRequest is POST /api/hooks/user/update. Pointer fields
// distinguish "absent" (leave alone) from zero values; Command nil means
// unchanged (an explicit empty argv is rejected, never a wipe).
type HooksUserUpdateRequest struct {
	Fingerprint string   `json:"fingerprint"`
	Matcher     *string  `json:"matcher"`
	Name        *string  `json:"name"`
	Command     []string `json:"command"`
	Timeout     *int     `json:"timeout"`
	OnFailure   *string  `json:"on_failure"`
	Enabled     *bool    `json:"enabled"`
}

// HooksMasterRequest is POST /api/hooks/master.
type HooksMasterRequest struct {
	Enabled bool `json:"enabled"`
}

// HooksAPI serves hook inspection and trust over HTTP.
type HooksAPI struct {
	runner  *hooks.Runner
	enabled bool
	store   *hooks.TrustStore
	// reload swaps the live runner after a validated config mutation.
	// Production wires hakaseagent.ReloadUserHooks; tests inject a fake.
	reload      func(hooks.Config) error
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
		reload:  hakaseagent.ReloadUserHooks,
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
	r.Post("/hooks/user/add", api.UserAdd)
	r.Post("/hooks/user/remove", api.UserRemove)
	r.Post("/hooks/user/set-enabled", api.UserSetEnabled)
	r.Post("/hooks/user/update", api.UserUpdate)
	r.Post("/hooks/master", api.Master)
}

func toSnapshotDTO(s hooks.Snapshot) HookSnapshotDTO {
	return HookSnapshotDTO{
		Event: s.Event, Matcher: s.Matcher, Name: s.Name, Command: s.Command,
		Timeout: s.Timeout, OnFailure: s.OnFailure, Fingerprint: s.Fingerprint,
		Layer: s.Layer, Trusted: s.Trusted, Enabled: s.Enabled,
	}
}

// List handles GET /api/hooks[?session_id=...] - user hooks plus the
// project layer (with trust status) for the resolved root.
func (api *HooksAPI) List(w http.ResponseWriter, r *http.Request) {
	writeHooksJSON(w, api.buildListDTO(r.URL.Query().Get("session_id")))
}

func (api *HooksAPI) buildListDTO(sessionID string) HooksListDTO {
	enabled := api.enabled
	if api.runner != nil {
		enabled = api.runner.MasterEnabled()
	}
	dto := HooksListDTO{Enabled: enabled, User: make([]HookSnapshotDTO, 0)}
	if api.runner != nil {
		for _, s := range api.runner.Snapshots() {
			dto.User = append(dto.User, toSnapshotDTO(s))
		}
	}
	root := ""
	if api.resolveRoot != nil {
		root = api.resolveRoot(sessionID)
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
	return dto
}

// mutateUserHook applies a user-layer mutation to the resolved config
// file, reloads the live runner, and answers with the refreshed list.
// The whole chain is one unit: a reload failure (impossible after a
// validated write, but guarded anyway) is a 500, never a silent split
// between disk and the running agent. CRUD targets the user layer only
// (spec HK-111): project files are repo-owned, trust-managed.
func (api *HooksAPI) mutateUserHook(w http.ResponseWriter, r *http.Request, mutate func(*hooks.Config) error) {
	path := resolveConfigPath("config.json")
	if path == "" {
		writeHooksError(w, http.StatusInternalServerError, "no config path resolves")
		return
	}
	block, err := hooks.WriteUserHooks(path, mutate)
	if err != nil {
		writeHooksError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := api.reload(block); err != nil {
		writeHooksError(w, http.StatusInternalServerError, fmt.Sprintf("saved, but live reload failed: %v", err))
		return
	}
	var sessionID string
	if r.URL != nil {
		sessionID = r.URL.Query().Get("session_id")
	}
	writeHooksJSON(w, api.buildListDTO(sessionID))
}

// UserAdd handles POST /api/hooks/user/add - append one user hook.
func (api *HooksAPI) UserAdd(w http.ResponseWriter, r *http.Request) {
	var req HooksUserAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeHooksError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Command) == 0 {
		writeHooksError(w, http.StatusBadRequest, "command must be a non-empty argv array")
		return
	}
	api.mutateUserHook(w, r, func(c *hooks.Config) error {
		return hooks.AddUserHook(c, req.Event, req.Matcher, hooks.Handler{
			Name: req.Name, Command: req.Command, Timeout: req.Timeout, OnFailure: req.OnFailure,
		})
	})
}

// UserRemove handles POST /api/hooks/user/remove - delete user hooks by
// fingerprint prefix (reuses HooksTrustRequest; session_id is ignored,
// the user layer is process-global).
func (api *HooksAPI) UserRemove(w http.ResponseWriter, r *http.Request) {
	var req HooksTrustRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeHooksError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Fingerprints) == 0 {
		writeHooksError(w, http.StatusBadRequest, "fingerprints must be non-empty")
		return
	}
	api.mutateUserHook(w, r, func(c *hooks.Config) error {
		for _, fp := range req.Fingerprints {
			if _, err := hooks.RemoveUserHook(c, fp); err != nil {
				return err
			}
		}
		return nil
	})
}

// UserSetEnabled handles POST /api/hooks/user/set-enabled - flip user
// hooks without touching fingerprints (trust unaffected, HK-109).
func (api *HooksAPI) UserSetEnabled(w http.ResponseWriter, r *http.Request) {
	var req HooksUserSetEnabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeHooksError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Fingerprints) == 0 {
		writeHooksError(w, http.StatusBadRequest, "fingerprints must be non-empty")
		return
	}
	api.mutateUserHook(w, r, func(c *hooks.Config) error {
		for _, fp := range req.Fingerprints {
			if _, err := hooks.SetUserHookEnabled(c, fp, req.Enabled); err != nil {
				return err
			}
		}
		return nil
	})
}

// UserUpdate handles POST /api/hooks/user/update - patch one user hook's
// fields (absent fields are left alone; the target resolves by its
// CURRENT fingerprint before mutation).
func (api *HooksAPI) UserUpdate(w http.ResponseWriter, r *http.Request) {
	var req HooksUserUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeHooksError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Fingerprint) == "" {
		writeHooksError(w, http.StatusBadRequest, "fingerprint must be non-empty")
		return
	}
	api.mutateUserHook(w, r, func(c *hooks.Config) error {
		_, err := hooks.UpdateUserHook(c, req.Fingerprint, hooks.HookUpdate{
			Matcher: req.Matcher, Name: req.Name, Command: req.Command,
			Timeout: req.Timeout, OnFailure: req.OnFailure, Enabled: req.Enabled,
		})
		return err
	})
}

// Master handles POST /api/hooks/master - flip the top-level
// hooks.enabled switch (spec HK-112). The cached enabled flag follows
// the mutation so the list DTO stays truthful.
func (api *HooksAPI) Master(w http.ResponseWriter, r *http.Request) {
	var req HooksMasterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeHooksError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	path := resolveConfigPath("config.json")
	if path == "" {
		writeHooksError(w, http.StatusInternalServerError, "no config path resolves")
		return
	}
	block, err := hooks.WriteUserHooks(path, func(c *hooks.Config) error {
		hooks.SetMasterEnabled(c, req.Enabled)
		return nil
	})
	if err != nil {
		writeHooksError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := api.reload(block); err != nil {
		writeHooksError(w, http.StatusInternalServerError, fmt.Sprintf("saved, but live reload failed: %v", err))
		return
	}
	writeHooksJSON(w, api.buildListDTO(r.URL.Query().Get("session_id")))
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
