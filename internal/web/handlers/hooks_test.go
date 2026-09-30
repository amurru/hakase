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
