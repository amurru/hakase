package handlers

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/registry"
	"amurru/hakase/internal/sandbox"
	hakasesession "amurru/hakase/internal/session"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// projectGitBin returns the git executable, skipping the test when git is
// absent. Git operations only happen inside test subprocesses, never through
// the sandbox engine's gate.
func projectGitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not installed: %v", err)
	}
	return p
}

func projectGitEnv() []string {
	// GIT_CONFIG_GLOBAL/SYSTEM pinning keeps the developer's ~/.gitconfig out
	// of test checkouts; explicit author/committer env keeps commits working
	// without it.
	return append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Hakase Test", "GIT_AUTHOR_EMAIL=hakase@test.local",
		"GIT_COMMITTER_NAME=Hakase Test", "GIT_COMMITTER_EMAIL=hakase@test.local")
}

func projectGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	g := projectGitBin(t)
	cmd := &exec.Cmd{
		Path: g,
		Args: append([]string{g}, args...),
		Dir:  dir,
		Env:  projectGitEnv(),
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// projectSeedRemote creates a seeded repo and returns a bare remote for it.
func projectSeedRemote(t *testing.T) string {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	projectGit(t, seed, "init", "-b", "main")
	projectGit(t, seed, "config", "user.name", "Hakase Test")
	projectGit(t, seed, "config", "user.email", "hakase@test.local")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectGit(t, seed, "add", ".")
	projectGit(t, seed, "commit", "-m", "initial")
	bare := filepath.Join(t.TempDir(), "remote.git")
	projectGit(t, t.TempDir(), "clone", "--bare", seed, bare)
	return bare
}

// projectStubGate installs the sandbox gate hooks the git engine requires in
// handler tests (main wires them in the real binary) and makes every command
// ask, exercising the operator-authorized registry path.
func projectStubGate(t *testing.T) {
	t.Helper()
	origGate, origApprove := sandbox.EvaluateCommandFunc, sandbox.ApproveFunc
	origAudit, origExpiry := sandbox.AuditCommandFunc, sandbox.ApprovalExpiryFunc
	sandbox.EvaluateCommandFunc = func(sb *sandbox.SandboxConfig, command string, args []string) sandbox.GateDecision {
		return sandbox.GateDecision{Action: sandbox.ActionAsk, Risk: sandbox.RiskMedium, Reason: "test: gate asks"}
	}
	sandbox.ApproveFunc = func(req interfaces.ApprovalRequest) (bool, error) { return true, nil }
	sandbox.AuditCommandFunc = func(entry sandbox.CommandAuditEntry) {}
	sandbox.ApprovalExpiryFunc = func() time.Duration { return 60 * time.Second }
	t.Cleanup(func() {
		sandbox.EvaluateCommandFunc, sandbox.ApproveFunc = origGate, origApprove
		sandbox.AuditCommandFunc, sandbox.ApprovalExpiryFunc = origAudit, origExpiry
	})
}

// installRegistry points registry.Current at a service backed by a store under
// home, restoring the previous value on cleanup. Git subprocesses (engine and
// helpers) get an isolated git config so the developer's ~/.gitconfig cannot
// leak into assertions.
func installRegistry(t *testing.T, home string) *registry.Service {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	orig := registry.Current
	st, err := registry.NewStore(filepath.Join(home, "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry.Current = registry.NewService(st, nil)
	t.Cleanup(func() { registry.Current = orig })
	return registry.Current
}

// TestWithBoundSandbox verifies the chat run helper pins the effective
// sandbox to the project checkout when the host sandbox is active, and leaves
// the context untouched when it is off (DP-7).
func TestWithBoundSandbox(t *testing.T) {
	origSB := sandbox.CurrentSandbox
	t.Cleanup(func() { sandbox.CurrentSandbox = origSB })
	checkout := t.TempDir()

	// Host sandbox off: no override is installed.
	sandbox.CurrentSandbox = nil
	plain := withBoundSandbox(context.Background(), checkout)
	if sb := sandbox.ConfigFrom(plain); sb != nil {
		t.Errorf("sandbox-off run got an override: %+v", sb)
	}

	// Host sandbox active: the run's sandbox is pinned to the checkout.
	procRoot := t.TempDir()
	sandbox.CurrentSandbox = &sandbox.SandboxConfig{
		Mode:           sandbox.SandboxModePaths,
		WorkspaceRoots: []string{procRoot},
		ReadRoots:      []string{procRoot},
	}
	bound := withBoundSandbox(context.Background(), checkout)
	sb := sandbox.ConfigFrom(bound)
	if sb == nil {
		t.Fatal("project-bound run did not install a sandbox override")
	}
	if len(sb.WorkspaceRoots) != 1 || sb.WorkspaceRoots[0] != checkout {
		t.Errorf("bound workspace roots = %v, want [%s]", sb.WorkspaceRoots, checkout)
	}
}

func doJSON(t *testing.T, h http.Handler, method, target string, body any) (*httptest.ResponseRecorder, ProjectDTO) {
	t.Helper()
	rd := io.Reader(strings.NewReader(""))
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var dto ProjectDTO
	if rec.Code >= 200 && rec.Code < 300 && len(rec.Body.Bytes()) > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	}
	return rec, dto
}

// TestProjectAPIEndpointsLifecycle covers register/list/sync/delete over HTTP
// against a local bare remote (sandbox-off, per D9).
func TestProjectAPIEndpointsLifecycle(t *testing.T) {
	projectStubGate(t)
	home := t.TempDir()
	installRegistry(t, home)
	router := chi.NewRouter()
	RegisterProjectRoutes(router)
	bare := projectSeedRemote(t)
	url := "file://" + bare

	// Register clones synchronously and returns a ready entry.
	rec, dto := doJSON(t, router, http.MethodPost, "/projects", map[string]string{
		"name": "demo", "url": url, "ref": "main",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status %d: %s", rec.Code, rec.Body.String())
	}
	if dto.Status != registry.StatusReady || dto.ID == "" {
		t.Fatalf("register dto = %+v", dto)
	}
	if _, err := os.Stat(filepath.Join(dto.Checkout, "README.md")); err != nil {
		t.Fatalf("checkout missing content: %v", err)
	}

	// Duplicate register is a conflict.
	rec, _ = doJSON(t, router, http.MethodPost, "/projects", map[string]string{"name": "demo", "url": url})
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate register status %d, want 409", rec.Code)
	}

	// Invalid source URL is a 400.
	rec, _ = doJSON(t, router, http.MethodPost, "/projects", map[string]string{"name": "bad", "url": "ftp://example.com/x.git"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid-url register status %d, want 400", rec.Code)
	}

	// List shows the registered project.
	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	router.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("list status %d", rec2.Code)
	}
	var list []ProjectDTO
	if err := json.Unmarshal(rec2.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].Name != "demo" {
		t.Fatalf("list = %s (err %v)", rec2.Body.String(), err)
	}

	// External push then sync fast-forwards the checkout.
	work := filepath.Join(t.TempDir(), "work")
	projectGit(t, filepath.Dir(work), "clone", url, work)
	if err := os.WriteFile(filepath.Join(work, "remote.txt"), []byte("remote work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectGit(t, work, "add", ".")
	projectGit(t, work, "commit", "-m", "external push")
	projectGit(t, work, "push", "origin", "main")

	rec, dto = doJSON(t, router, http.MethodPost, "/projects/"+dto.ID+"/sync", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status %d: %s", rec.Code, rec.Body.String())
	}
	if dto.Status != registry.StatusReady || dto.Error != "" {
		t.Fatalf("sync dto = %+v", dto)
	}
	if _, err := os.Stat(filepath.Join(dto.Checkout, "remote.txt")); err != nil {
		t.Fatalf("checkout did not receive pushed file: %v", err)
	}

	// Unknown id sync is a 404.
	rec, _ = doJSON(t, router, http.MethodPost, "/projects/nope/sync", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown sync status %d, want 404", rec.Code)
	}

	// Delete removes the entry + checkout; the bare remote survives.
	checkout := dto.Checkout
	del := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/projects/"+dto.ID, nil)
	router.ServeHTTP(del, req)
	if del.Code != http.StatusOK {
		t.Fatalf("delete status %d: %s", del.Code, del.Body.String())
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Errorf("checkout still present after delete (err=%v)", err)
	}
	if _, err := os.Stat(bare); err != nil {
		t.Errorf("bare remote removed by delete: %v", err)
	}
}

// TestProjectAPIUnavailable verifies the 503 path when the registry is not
// boot-configured.
func TestProjectAPIUnavailable(t *testing.T) {
	orig := registry.Current
	t.Cleanup(func() { registry.Current = orig })
	registry.Current = nil
	router := chi.NewRouter()
	RegisterProjectRoutes(router)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("list without registry status %d, want 503", rec.Code)
	}
}

// TestProjectStatusEndpointAndSyncGuards covers the project-ui.md additions:
// GET /projects/{id}/status reports branch + ahead/behind + dirty counts after
// a best-effort fetch, and the /sync endpoint refuses (409, status untouched)
// on an in-flight run or on uncommitted tracked changes.
func TestProjectStatusEndpointAndSyncGuards(t *testing.T) {
	projectStubGate(t)
	home := t.TempDir()
	installRegistry(t, home)
	router := chi.NewRouter()
	RegisterProjectRoutes(router)
	bare := projectSeedRemote(t)
	url := "file://" + bare

	rec, dto := doJSON(t, router, http.MethodPost, "/projects", map[string]string{
		"name": "demo", "url": url, "ref": "main",
	})
	if rec.Code != http.StatusCreated || dto.Status != registry.StatusReady {
		t.Fatalf("register status %d dto %+v", rec.Code, dto)
	}

	// External push, then the status endpoint (fetch on) reports behind.
	work := filepath.Join(t.TempDir(), "work")
	projectGit(t, filepath.Dir(work), "clone", url, work)
	if err := os.WriteFile(filepath.Join(work, "remote.txt"), []byte("remote work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectGit(t, work, "add", ".")
	projectGit(t, work, "commit", "-m", "external push")
	projectGit(t, work, "push", "origin", "main")

	getStatus := func(target string) (*httptest.ResponseRecorder, ProjectStatusDTO) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		router.ServeHTTP(rec, req)
		var st ProjectStatusDTO
		if rec.Code == http.StatusOK {
			_ = json.Unmarshal(rec.Body.Bytes(), &st)
		}
		return rec, st
	}

	rec2, st := getStatus("/projects/" + dto.ID + "/status")
	if rec2.Code != http.StatusOK {
		t.Fatalf("status code %d: %s", rec2.Code, rec2.Body.String())
	}
	if st.Branch != "main" {
		t.Errorf("branch = %q, want main", st.Branch)
	}
	if st.Behind != 1 {
		t.Errorf("behind = %d, want 1 (fetch must update refs)", st.Behind)
	}
	if st.Dirty || st.Modified != 0 || st.Untracked != 0 {
		t.Errorf("expected clean counts, got %+v", st)
	}
	if st.Error != "" {
		t.Errorf("unexpected status error: %s", st.Error)
	}

	// Unknown id is a 404.
	rec2, _ = getStatus("/projects/nope/status")
	if rec2.Code != http.StatusNotFound {
		t.Errorf("unknown status code %d, want 404", rec2.Code)
	}

	// Dirty tree: sync is refused as a 409 and the entry stays ready.
	checkout := dto.Checkout
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, _ = doJSON(t, router, http.MethodPost, "/projects/"+dto.ID+"/sync", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("dirty sync code %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "uncommitted tracked changes") {
		t.Errorf("dirty sync body missing reason: %s", rec.Body.String())
	}

	// The refused sync left the entry ready (never a sync_error transition).
	rec2, st = getStatus("/projects/" + dto.ID + "/status?fetch=0")
	if rec2.Code != http.StatusOK || st.ProjectStatus != registry.StatusReady {
		t.Errorf("status after refused sync = %d %s", rec2.Code, rec2.Body.String())
	}

	// Active agent run on the project refuses sync before any git work.
	id := projectListID(t, router)
	activeProjectRuns.begin(id)
	t.Cleanup(func() { activeProjectRuns.end(id) })
	rec, _ = doJSON(t, router, http.MethodPost, "/projects/"+id+"/sync", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("active-run sync code %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "agent run is active") {
		t.Errorf("active-run sync body missing reason: %s", rec.Body.String())
	}
}

// projectListID returns the id of the (single) registered project via the
// list endpoint.
func projectListID(t *testing.T, router http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projects", nil))
	var list []ProjectDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list for guard test: %s (err %v)", rec.Body.String(), err)
	}
	return list[0].ID
}

// TestCreateSessionBindsProject covers POST /sessions accepting project_id and
// persisting the binding (DP-7).
func TestCreateSessionBindsProject(t *testing.T) {
	home := t.TempDir()
	installRegistry(t, home)

	// A ready registry entry (no git needed): create then flip to ready.
	st := registry.Current.Store()
	p, err := st.Create("hakase-web", "https://github.com/amurru/hakase.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	p.Status = registry.StatusReady
	p.Checkout = filepath.Join(home, "projects", p.ID)
	if err := st.Update(p); err != nil {
		t.Fatal(err)
	}

	sessDir := filepath.Join(home, "sessions")
	store, err := hakasesession.NewSessionStore(sessDir)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := hakasesession.NewSessionService(store)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	RegisterSessionRoutes(router, svc)

	// Unknown project is a 400.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewBufferString(`{"title":"x","project_id":"proj_missing"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown project status %d, want 400", rec.Code)
	}

	// Valid binding returns the project fields and persists them.
	rec = httptest.NewRecorder()
	body := map[string]string{"title": "bound", "project_id": p.ID}
	b, _ := json.Marshal(body)
	req = httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("bind status %d: %s", rec.Code, rec.Body.String())
	}
	var got SessionDetailDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != p.ID || got.ProjectName != "hakase-web" {
		t.Errorf("session dto project fields = %+v", got)
	}
	sess, err := svc.Store().Load(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ProjectID != p.ID || sess.ProjectName != "hakase-web" {
		t.Errorf("persisted session project fields = %+v", sess)
	}
}
