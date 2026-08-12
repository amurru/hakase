package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/session"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// newTestSessionSvc creates a SessionService backed by a temp directory.
func newTestSessionSvc(t *testing.T) *session.SessionService {
	t.Helper()
	store, err := session.NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create session store: %v", err)
	}
	svc, err := session.NewSessionService(store)
	if err != nil {
		t.Fatalf("failed to create session service: %v", err)
	}
	return svc
}

// newTestSessionAPI creates a SessionAPI and registers routes on a chi router.
func newTestSessionAPI(t *testing.T) (*SessionAPI, *session.SessionService, chi.Router) {
	t.Helper()
	svc := newTestSessionSvc(t)
	api := &SessionAPI{svc: svc}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Get("/sessions", api.ListSessions)
		r.Post("/sessions", api.CreateSession)
		r.Get("/sessions/active", api.GetActiveSession)
		r.Get("/sessions/{id}", api.GetSession)
		r.Delete("/sessions/{id}", api.DeleteSession)
		r.Post("/sessions/{id}/archive", api.ArchiveSession)
		r.Post("/sessions/{id}/activate", api.ActivateSession)
	})
	return api, svc, r
}

// createTestSession is a helper that creates a session through the service.
func createTestSession(t *testing.T, svc *session.SessionService, title string) *session.Session {
	t.Helper()
	sess, err := svc.CreateSession(title)
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	return sess
}

func TestListSessionsEmpty(t *testing.T) {
	_, _, r := newTestSessionAPI(t)

	req := httptest.NewRequest("GET", "/api/sessions", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var sessions []SessionSummaryDTO
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected empty list, got %d sessions", len(sessions))
	}
}

func TestListSessionsWithData(t *testing.T) {
	_, svc, r := newTestSessionAPI(t)

	createTestSession(t, svc, "First session")
	createTestSession(t, svc, "Second session")

	req := httptest.NewRequest("GET", "/api/sessions", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var sessions []SessionSummaryDTO
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(sessions) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(sessions))
	}

	// Verify DTO fields
	for _, s := range sessions {
		if s.ID == "" {
			t.Fatal("session ID should not be empty")
		}
		if s.Title == "" {
			t.Fatal("session title should not be empty")
		}
		if s.UpdatedAt.IsZero() {
			t.Fatal("session UpdatedAt should not be zero")
		}
	}
}

func TestCreateSession(t *testing.T) {
	_, _, r := newTestSessionAPI(t)

	body := `{"title":"Test session"}`
	req := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var dto SessionDetailDTO
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if dto.ID == "" {
		t.Fatal("expected non-empty session ID")
	}
	if dto.Title != "Test session" {
		t.Fatalf("expected title 'Test session', got %q", dto.Title)
	}
	if dto.CreatedAt.IsZero() {
		t.Fatal("expected non-zero CreatedAt")
	}
	if dto.Messages == nil {
		t.Fatal("expected non-nil messages array")
	}
}

func TestCreateSessionDefaultTitle(t *testing.T) {
	_, _, r := newTestSessionAPI(t)

	body := `{}`
	req := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var dto SessionDetailDTO
	json.NewDecoder(w.Body).Decode(&dto)
	if dto.Title != "Untitled session" {
		t.Fatalf("expected default title 'Untitled session', got %q", dto.Title)
	}
}

func TestCreateSessionInvalidJSON(t *testing.T) {
	_, _, r := newTestSessionAPI(t)

	req := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetSession(t *testing.T) {
	_, svc, r := newTestSessionAPI(t)
	sess := createTestSession(t, svc, "My session")

	req := httptest.NewRequest("GET", "/api/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var dto SessionDetailDTO
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if dto.ID != sess.ID {
		t.Fatalf("expected ID %q, got %q", sess.ID, dto.ID)
	}
	if dto.Title != "My session" {
		t.Fatalf("expected title 'My session', got %q", dto.Title)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	_, _, r := newTestSessionAPI(t)

	req := httptest.NewRequest("GET", "/api/sessions/sess_nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "session not found" {
		t.Fatalf("expected error 'session not found', got: %v", resp)
	}
}

func TestDeleteSession(t *testing.T) {
	_, svc, r := newTestSessionAPI(t)
	sess := createTestSession(t, svc, "To delete")

	req := httptest.NewRequest("DELETE", "/api/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify session is gone
	req2 := httptest.NewRequest("GET", "/api/sessions/"+sess.ID, nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestDeleteSessionNotFound(t *testing.T) {
	_, _, r := newTestSessionAPI(t)

	req := httptest.NewRequest("DELETE", "/api/sessions/sess_nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestArchiveSession(t *testing.T) {
	_, svc, r := newTestSessionAPI(t)
	sess := createTestSession(t, svc, "To archive")

	req := httptest.NewRequest("POST", "/api/sessions/"+sess.ID+"/archive", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify session no longer appears in list
	req2 := httptest.NewRequest("GET", "/api/sessions", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("list failed: %d", w2.Code)
	}
	var sessions []SessionSummaryDTO
	json.NewDecoder(w2.Body).Decode(&sessions)
	for _, s := range sessions {
		if s.ID == sess.ID {
			t.Fatal("archived session should not appear in session list")
		}
	}
}

func TestArchiveSessionNotFound(t *testing.T) {
	_, _, r := newTestSessionAPI(t)

	req := httptest.NewRequest("POST", "/api/sessions/sess_nonexistent/archive", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestActivateSession(t *testing.T) {
	_, svc, r := newTestSessionAPI(t)
	sess := createTestSession(t, svc, "Session one")
	sess2 := createTestSession(t, svc, "Session two")

	// Activate session one
	req := httptest.NewRequest("POST", "/api/sessions/"+sess.ID+"/activate", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify it's the active session
	req2 := httptest.NewRequest("GET", "/api/sessions/active", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("active check failed: %d", w2.Code)
	}
	var active SessionDetailDTO
	json.NewDecoder(w2.Body).Decode(&active)
	if active.ID != sess.ID {
		t.Fatalf("expected active session %q, got %q", sess.ID, active.ID)
	}

	// Switch to session two
	req3 := httptest.NewRequest("POST", "/api/sessions/"+sess2.ID+"/activate", nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 for second activate, got %d", w3.Code)
	}

	req4 := httptest.NewRequest("GET", "/api/sessions/active", nil)
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req4)
	json.NewDecoder(w4.Body).Decode(&active)
	if active.ID != sess2.ID {
		t.Fatalf("expected active session %q after switch, got %q", sess2.ID, active.ID)
	}
}

func TestActivateSessionNotFound(t *testing.T) {
	_, _, r := newTestSessionAPI(t)

	req := httptest.NewRequest("POST", "/api/sessions/sess_nonexistent/activate", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetActiveSessionNone(t *testing.T) {
	_, _, r := newTestSessionAPI(t)

	req := httptest.NewRequest("GET", "/api/sessions/active", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	active, ok := resp["active"]
	if !ok {
		t.Fatal("expected 'active' field in response")
	}
	if active != false {
		t.Fatalf("expected active=false, got %v", active)
	}
}

func TestGetActiveSessionPresent(t *testing.T) {
	_, svc, r := newTestSessionAPI(t)
	sess := createTestSession(t, svc, "Active one")

	req := httptest.NewRequest("GET", "/api/sessions/active", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var dto SessionDetailDTO
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if dto.ID != sess.ID {
		t.Fatalf("expected active session %q, got %q", sess.ID, dto.ID)
	}
	if dto.Title != "Active one" {
		t.Fatalf("expected title 'Active one', got %q", dto.Title)
	}
}

func TestGetSessionWithMessages(t *testing.T) {
	_, svc, r := newTestSessionAPI(t)
	sess := createTestSession(t, svc, "Chat session")

	// Add messages to the session directly
	if err := svc.AddMessage("user", "Hello", ""); err != nil {
		t.Fatalf("failed to add user message: %v", err)
	}
	if err := svc.AddMessage("agent", "Hi there!", "I should greet the user"); err != nil {
		t.Fatalf("failed to add agent message: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var dto SessionDetailDTO
	if err := json.NewDecoder(w.Body).Decode(&dto); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(dto.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(dto.Messages))
	}

	// Verify message fields
	msg0 := dto.Messages[0]
	if msg0.Role != "user" {
		t.Fatalf("expected role 'user', got %q", msg0.Role)
	}
	if msg0.Content != "Hello" {
		t.Fatalf("expected content 'Hello', got %q", msg0.Content)
	}

	msg1 := dto.Messages[1]
	if msg1.Role != "agent" {
		t.Fatalf("expected role 'agent', got %q", msg1.Role)
	}
	if msg1.Content != "Hi there!" {
		t.Fatalf("expected content 'Hi there!', got %q", msg1.Content)
	}
	if msg1.Thinking != "I should greet the user" {
		t.Fatalf("expected thinking, got %q", msg1.Thinking)
	}
}

// newTestJWT2 generates a JWT for auth enforcement tests.
func newTestJWT2(username string, expiry time.Duration) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"username": username,
		"exp":      float64(now.Add(expiry).Unix()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokStr, _ := token.SignedString(testJWTSigningKey)
	return tokStr
}

// newTestAuthRouter creates a chi router with auth middleware and session routes
// for testing auth enforcement.
func newTestAuthRouter(t *testing.T) (*SessionAPI, *session.SessionService, chi.Router) {
	t.Helper()
	svc := newTestSessionSvc(t)
	api := &SessionAPI{svc: svc}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tok := extractToken(r)
				if tok == "" {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
					return
				}
				parsed, err := jwt.Parse(tok, func(t *jwt.Token) (interface{}, error) {
					return testJWTSigningKey, nil
				})
				if err != nil || !parsed.Valid {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		RegisterSessionRoutes(r, svc)
	})
	return api, svc, r
}

func TestSessionRoutesRequireAuth(t *testing.T) {
	_, _, r := newTestAuthRouter(t)

	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/api/sessions"},
		{"POST", "/api/sessions"},
		{"GET", "/api/sessions/active"},
		{"GET", "/api/sessions/sess_test"},
		{"DELETE", "/api/sessions/sess_test"},
		{"POST", "/api/sessions/sess_test/archive"},
		{"POST", "/api/sessions/sess_test/activate"},
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401 without token, got %d", ep.method, ep.path, w.Code)
		}
	}
}

func TestSessionRoutesWithValidAuth(t *testing.T) {
	_, svc, r := newTestAuthRouter(t)
	sess := createTestSession(t, svc, "Auth test")
	token := newTestJWT2("admin", time.Hour)

	// Test list with auth
	req := httptest.NewRequest("GET", "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with auth for list, got %d: %s", w.Code, w.Body.String())
	}

	// Test get with auth
	req2 := httptest.NewRequest("GET", "/api/sessions/"+sess.ID, nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 with auth for get, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestSessionRoutesWithInvalidAuth(t *testing.T) {
	_, _, r := newTestAuthRouter(t)

	req := httptest.NewRequest("GET", "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with invalid token, got %d", w.Code)
	}
}
