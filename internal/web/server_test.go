package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testJWTSigningKey is a fixed key for test-only token generation.
var testJWTSigningKey = []byte("test-secret-key-for-testing-only")

// newTestServer creates a Server with a test JWT key wired into the router.
func newTestServer() *Server {
	s := NewServer(testJWTSigningKey, nil)
	s.RegisterDefaults(getTestFrontendAssets())
	return s
}

// getTestFrontendAssets returns a minimal in-memory filesystem for tests.
func getTestFrontendAssets() http.FileSystem {
	return http.Dir("testdata")
}

func newTestToken(jwtKey []byte) string {
	claims := jwt.MapClaims{
		"username": "testuser",
		"exp":      float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokStr, _ := token.SignedString(jwtKey)
	return tokStr
}

func TestSPAFallbackRoot(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if body == "" || !containsAny(body, "<!DOCTYPE", "<html") {
		t.Fatalf("expected HTML at /, got: %s", body)
	}
}

func TestSPAFallbackDeepRoute(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/some/deep/route", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for SPA fallback, got %d", w.Code)
	}
	body := w.Body.String()
	if body == "" || !containsAny(body, "<!DOCTYPE", "<html") {
		t.Fatalf("expected index.html for /some/deep/route, got: %s", body)
	}
}

func TestServerStaticAssets(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/assets/app.js", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for static asset, got %d", w.Code)
	}
}

func TestMissingAsset404(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/assets/nonexistent.js", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing asset, got %d", w.Code)
	}
}

func TestAuthMiddlewareNoToken(t *testing.T) {
	srv := newTestServer()
	// /api/health is unauthenticated now, so test /api/me instead
	req := httptest.NewRequest("GET", "/api/me", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/me request, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["error"] != "unauthorized" {
		t.Fatalf("expected error=unauthorized, got: %v", body)
	}
}

func TestAuthMiddlewareBearerToken(t *testing.T) {
	srv := newTestServer()
	token := newTestToken(srv.jwtKey)
	// /api/me is auth-protected; /api/health is unauthenticated
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid bearer token, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareCookieToken(t *testing.T) {
	srv := newTestServer()
	token := newTestToken(srv.jwtKey)
	// /api/me is auth-protected
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: "hakase_token", Value: token})
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid cookie token, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareInvalidToken(t *testing.T) {
	srv := newTestServer()
	// /api/me is auth-protected
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer invalid-token-here")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid token, got %d", w.Code)
	}
}

func TestAuthMiddlewareExpiredToken(t *testing.T) {
	srv := newTestServer()
	claims := jwt.MapClaims{
		"username": "testuser",
		"exp":      float64(time.Now().Add(-time.Hour).Unix()),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokStr, _ := tok.SignedString(srv.jwtKey)

	// /api/me is auth-protected
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokStr)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired token, got %d", w.Code)
	}
}

func TestAPIHealthUnauthenticated(t *testing.T) {
	srv := newTestServer()

	// /api/health is now unauthenticated - should return 200 without a token
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for unauthenticated /api/health, got %d", w.Code)
	}

	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Fatalf("expected status=ok, got: %v", body)
	}
}

func TestConfigRouteRequiresAuth(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/config, got %d", w.Code)
	}
}

func TestFilesInlineRouteRequiresAuth(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/files/inline?path=outputs/x.png", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/files/inline, got %d", w.Code)
	}
}

func TestConfigRouteAuthed(t *testing.T) {
	srv := newTestServer()
	token := newTestToken(srv.jwtKey)

	req := httptest.NewRequest("GET", "/api/config", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for authenticated /api/config, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPIRouteNotSPAFallback(t *testing.T) {
	srv := newTestServer()
	token := newTestToken(srv.jwtKey)

	// /api/nonexistent is under the auth group - needs a valid token to reach the router
	req := httptest.NewRequest("GET", "/api/nonexistent", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown API route, got %d", w.Code)
	}
	body := w.Body.String()
	if containsAny(body, "<html", "<!DOCTYPE") {
		t.Fatalf("API routes should not fall back to SPA, got HTML: %s", body)
	}
}

func TestCORSMiddleware(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("OPTIONS", "/api/health", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for preflight, got %d", w.Code)
	}
	allowOrigin := w.Header().Get("Access-Control-Allow-Origin")
	if allowOrigin != "http://localhost:5173" {
		t.Fatalf("expected CORS origin http://localhost:5173, got %q", allowOrigin)
	}
}

func TestCacheHeadersIndexHTML(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	cacheControl := w.Header().Get("Cache-Control")
	if cacheControl == "" {
		t.Fatal("index.html should have Cache-Control header")
	}
	if containsAny(cacheControl, "immutable", "max-age=31536000") {
		t.Fatalf("index.html should NOT have immutable cache, got: %s", cacheControl)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}


