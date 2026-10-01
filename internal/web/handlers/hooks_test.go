package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/hooks"

	"github.com/go-chi/chi/v5"
)

func newTestHooksAPI(t *testing.T, user hooks.Config, projectBody string) (*HooksAPI, string) {
	t.Helper()
	r, err := hooks.NewRunner(user)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	// One store shared by runner and API: production uses two handles on
	// the same default path (kept coherent by the mtime caches); tests
	// share the object to avoid filesystem-timing flakes.
	store := hooks.OpenTrustStore(filepath.Join(t.TempDir(), "hooks-trust.json"))
	r.SetTrustStore(store)
	root := t.TempDir()
	if projectBody != "" {
		dir := filepath.Join(root, ".hakase")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(projectBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	api := &HooksAPI{
		runner:      r,
		enabled:     true,
		store:       store,
		reload:      r.Reload,
		resolveRoot: func(sessionID string) string { return root },
	}
	return api, root
}

func serveHooks(t *testing.T, api *HooksAPI, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Get("/hooks", api.List)
		r.Post("/hooks/trust", api.Trust)
		r.Post("/hooks/untrust", api.Untrust)
		r.Post("/hooks/user/add", api.UserAdd)
		r.Post("/hooks/user/remove", api.UserRemove)
		r.Post("/hooks/user/set-enabled", api.UserSetEnabled)
		r.Post("/hooks/user/update", api.UserUpdate)
		r.Post("/hooks/master", api.Master)
	})
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, target, reader))
	return w
}

func TestHooksListLayers(t *testing.T) {
	user := hooks.Config{PreToolUse: []hooks.Group{{Hooks: []hooks.Handler{{Name: "u", Command: []string{"/bin/true"}}}}}}
	user.ApplyDefaults()
	api, _ := newTestHooksAPI(t, user, `{"PreToolUse":[{"hooks":[{"name":"p","command":["/bin/false"]}]}]}`)

	w := serveHooks(t, api, http.MethodGet, "/api/hooks", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	var dto HooksListDTO
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if !dto.Enabled || len(dto.User) != 1 || dto.User[0].Layer != "user" || !dto.User[0].Trusted {
		t.Errorf("user layer = %+v", dto.User)
	}
	if dto.Project == nil || len(dto.Project.Hooks) != 1 {
		t.Fatalf("project layer = %+v", dto.Project)
	}
	if dto.Project.Hooks[0].Trusted {
		t.Error("project hook must start untrusted")
	}
}

func TestHooksTrustFlow(t *testing.T) {
	api, _ := newTestHooksAPI(t, hooks.Config{}, `{"SessionStart":[{"hooks":[{"name":"s","command":["/bin/true"]}]}]}`)

	// Trust requires a fingerprint matching a current handler.
	w := serveHooks(t, api, http.MethodPost, "/api/hooks/trust", map[string]any{"fingerprints": []string{"sha256:zzz"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown fp status = %d, want 400", w.Code)
	}
	w = serveHooks(t, api, http.MethodPost, "/api/hooks/trust", map[string]any{"fingerprints": []string{}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty list status = %d, want 400", w.Code)
	}

	// Resolve the real fingerprint, trust by prefix, verify via list.
	lw := serveHooks(t, api, http.MethodGet, "/api/hooks", nil)
	var dto HooksListDTO
	if err := json.NewDecoder(lw.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	fp := dto.Project.Hooks[0].Fingerprint
	tw := serveHooks(t, api, http.MethodPost, "/api/hooks/trust", map[string]any{"fingerprints": []string{fp[:20]}})
	if tw.Code != http.StatusOK {
		t.Fatalf("trust status = %d: %s", tw.Code, tw.Body.String())
	}
	var td HooksTrustDTO
	if err := json.NewDecoder(tw.Body).Decode(&td); err != nil {
		t.Fatal(err)
	}
	if len(td.Changed) != 1 || td.Changed[0] != fp {
		t.Errorf("trusted = %v, want [%s]", td.Changed, fp)
	}
	lw = serveHooks(t, api, http.MethodGet, "/api/hooks", nil)
	dto = HooksListDTO{}
	if err := json.NewDecoder(lw.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if !dto.Project.Hooks[0].Trusted {
		t.Error("hook must read trusted after trust")
	}

	// Untrust revokes.
	uw := serveHooks(t, api, http.MethodPost, "/api/hooks/untrust", map[string]any{"fingerprints": []string{fp[:20]}})
	if uw.Code != http.StatusOK {
		t.Fatalf("untrust status = %d: %s", uw.Code, uw.Body.String())
	}
	lw = serveHooks(t, api, http.MethodGet, "/api/hooks", nil)
	dto = HooksListDTO{}
	if err := json.NewDecoder(lw.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if dto.Project.Hooks[0].Trusted {
		t.Error("hook must read untrusted after untrust")
	}

	// Untrusting again fails loudly.
	uw = serveHooks(t, api, http.MethodPost, "/api/hooks/untrust", map[string]any{"fingerprints": []string{fp[:20]}})
	if uw.Code != http.StatusBadRequest {
		t.Fatalf("double untrust status = %d, want 400", uw.Code)
	}
}

func TestHooksListNoProject(t *testing.T) {
	api, _ := newTestHooksAPI(t, hooks.Config{}, "")
	w := serveHooks(t, api, http.MethodGet, "/api/hooks?session_id=sess", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
	var dto HooksListDTO
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if dto.Project != nil {
		t.Errorf("project must be null without a hooks file, got %+v", dto.Project)
	}
	if dto.User == nil {
		t.Error("user must serialize as [] not null")
	}
}

func TestHooksListNilRunner(t *testing.T) {
	api := &HooksAPI{resolveRoot: func(string) string { return "" }}
	w := serveHooks(t, api, http.MethodGet, "/api/hooks", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("nil-runner list status = %d", w.Code)
	}
	var dto HooksListDTO
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if dto.Enabled || len(dto.User) != 0 || dto.Project != nil {
		t.Errorf("nil runner must yield empty DTO, got %+v", dto)
	}
}

func TestHooksTrustErrorBodies(t *testing.T) {
	api, _ := newTestHooksAPI(t, hooks.Config{}, "")
	w := serveHooks(t, api, http.MethodPost, "/api/hooks/trust", map[string]any{"fingerprints": []string{"sha256:x"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "error") {
		t.Errorf("trust without project = %d %s, want 400 + error", w.Code, w.Body.String())
	}
}

// newDiskBackedHooksAPI builds an API whose mutations hit a real config
// file under an isolated HAKASE_HOME, with reload wired to the runner
// (production wires hakaseagent.ReloadUserHooks, same signature).
func newDiskBackedHooksAPI(t *testing.T) *HooksAPI {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	cfgPath := filepath.Join(home, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"provider":"openai","model_name":"m","api_key":"k"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := hooks.NewRunner(hooks.Config{})
	if err != nil {
		t.Fatal(err)
	}
	r.SetTrustStore(hooks.OpenTrustStore(filepath.Join(home, "hooks-trust.json")))
	return &HooksAPI{
		runner:      r,
		enabled:     true,
		store:       hooks.OpenTrustStore(filepath.Join(home, "hooks-trust.json")),
		reload:      r.Reload,
		resolveRoot: func(sessionID string) string { return "" },
	}
}

func TestHooksUserCRUDCycle(t *testing.T) {
	api := newDiskBackedHooksAPI(t)

	// Add.
	w := serveHooks(t, api, http.MethodPost, "/api/hooks/user/add", map[string]any{
		"event": "PreToolUse", "matcher": "^system_exec$", "name": "web-add",
		"command": []string{"/bin/true"}, "timeout": 30, "on_failure": "allow",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("add status = %d: %s", w.Code, w.Body.String())
	}
	var dto HooksListDTO
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if len(dto.User) != 1 || dto.User[0].Name != "web-add" || !dto.User[0].Enabled {
		t.Fatalf("user after add = %+v", dto.User)
	}
	fp := dto.User[0].Fingerprint

	// Live reload took effect on the same runner (no restart): the new
	// hook is visible without rebuilding the API.
	if len(api.runner.Snapshots()) != 1 {
		t.Error("runner must see the added hook after reload")
	}

	// Disable.
	w = serveHooks(t, api, http.MethodPost, "/api/hooks/user/set-enabled", map[string]any{
		"fingerprints": []string{fp[:16]}, "enabled": false,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("disable status = %d: %s", w.Code, w.Body.String())
	}
	dto = HooksListDTO{}
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if dto.User[0].Enabled {
		t.Error("hook must read disabled")
	}

	// Update (rename only; fingerprint prefix still resolves the target).
	w = serveHooks(t, api, http.MethodPost, "/api/hooks/user/update", map[string]any{
		"fingerprint": fp[:16], "name": "web-renamed",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", w.Code, w.Body.String())
	}
	dto = HooksListDTO{}
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if dto.User[0].Name != "web-renamed" {
		t.Errorf("renamed = %+v", dto.User[0])
	}

	// Remove.
	w = serveHooks(t, api, http.MethodPost, "/api/hooks/user/remove", map[string]any{
		"fingerprints": []string{fp[:16]},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("remove status = %d: %s", w.Code, w.Body.String())
	}
	dto = HooksListDTO{}
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if len(dto.User) != 0 {
		t.Errorf("user after remove = %+v", dto.User)
	}
}

func TestHooksUserErrorPaths(t *testing.T) {
	api := newDiskBackedHooksAPI(t)

	w := serveHooks(t, api, http.MethodPost, "/api/hooks/user/add", map[string]any{
		"event": "Nope", "command": []string{"/bin/true"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad event status = %d, want 400", w.Code)
	}
	w = serveHooks(t, api, http.MethodPost, "/api/hooks/user/add", map[string]any{
		"event": "PreToolUse", "command": []string{},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty command status = %d, want 400", w.Code)
	}
	w = serveHooks(t, api, http.MethodPost, "/api/hooks/user/remove", map[string]any{
		"fingerprints": []string{"sha256:zzz"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown prefix status = %d, want 400", w.Code)
	}
	w = serveHooks(t, api, http.MethodPost, "/api/hooks/user/update", map[string]any{
		"fingerprint": "", "name": "x",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty fingerprint status = %d, want 400", w.Code)
	}
}

func TestHooksMasterToggle(t *testing.T) {
	api := newDiskBackedHooksAPI(t)

	w := serveHooks(t, api, http.MethodPost, "/api/hooks/master", map[string]any{"enabled": false})
	if w.Code != http.StatusOK {
		t.Fatalf("master off status = %d: %s", w.Code, w.Body.String())
	}
	var dto HooksListDTO
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if dto.Enabled {
		t.Error("DTO must reflect master off")
	}
	if api.runner.Enabled() {
		t.Error("runner must be disabled after master off + reload")
	}
	w = serveHooks(t, api, http.MethodPost, "/api/hooks/master", map[string]any{"enabled": true})
	if w.Code != http.StatusOK {
		t.Fatalf("master on status = %d: %s", w.Code, w.Body.String())
	}
	dto = HooksListDTO{}
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if !dto.Enabled {
		t.Error("DTO must reflect master on")
	}
}
