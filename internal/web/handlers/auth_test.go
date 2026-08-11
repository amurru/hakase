package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/auth"
	"github.com/golang-jwt/jwt/v5"
)

var testJWTSigningKey = []byte("test-secret-key-for-testing-only")

func newTestJWT(username string, expiry time.Duration) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"username": username,
		"exp":      float64(now.Add(expiry).Unix()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokStr, _ := token.SignedString(testJWTSigningKey)
	return tokStr
}

func writeTestCredsFile(t *testing.T, username, password string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := auth.SetPassword(path, username, password); err != nil {
		t.Fatalf("failed to set password: %v", err)
	}
	return path
}

func TestLoginNotConfigured(t *testing.T) {
	credsPath := filepath.Join(t.TempDir(), "nonexistent.json")

	handler := LoginHandler(testJWTSigningKey, credsPath, 24*time.Hour)

	body := `{"username":"admin","password":"testpass"}`
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing credentials file, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "not configured" {
		t.Fatalf("expected error='not configured', got: %v", resp)
	}
}

func TestLoginInvalidJSON(t *testing.T) {
	credsPath := writeTestCredsFile(t, "admin", "testpass")

	handler := LoginHandler(testJWTSigningKey, credsPath, 24*time.Hour)

	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestLoginSuccess(t *testing.T) {
	credsPath := writeTestCredsFile(t, "admin", "testpass123")

	handler := LoginHandler(testJWTSigningKey, credsPath, 24*time.Hour)

	body := `{"username":"admin","password":"testpass123"}`
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid login, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["username"] != "admin" {
		t.Fatalf("expected username=admin, got: %v", resp)
	}
	if resp["token"] == "" {
		t.Fatal("expected non-empty token in response")
	}

	// Verify token is valid
	token, err := jwt.Parse(resp["token"], func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return testJWTSigningKey, nil
	})
	if err != nil {
		t.Fatalf("response token is invalid: %v", err)
	}
	if !token.Valid {
		t.Fatal("response token is not valid")
	}

	// Check Set-Cookie header
	cookieHeader := w.Header().Get("Set-Cookie")
	if cookieHeader == "" {
		t.Fatal("expected Set-Cookie header in response")
	}
	if !strings.Contains(cookieHeader, "hakase_token=") {
		t.Fatalf("expected hakase_token cookie, got: %s", cookieHeader)
	}
	if !strings.Contains(cookieHeader, "HttpOnly") {
		t.Fatal("expected HttpOnly in cookie")
	}
	if !strings.Contains(cookieHeader, "SameSite=Strict") {
		t.Fatal("expected SameSite=Strict in cookie")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	credsPath := writeTestCredsFile(t, "admin", "testpass123")

	handler := LoginHandler(testJWTSigningKey, credsPath, 24*time.Hour)

	body := `{"username":"admin","password":"wrongpassword"}`
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "invalid credentials" {
		t.Fatalf("expected error='invalid credentials', got: %v", resp)
	}
}

func TestLoginWrongUsername(t *testing.T) {
	credsPath := writeTestCredsFile(t, "admin", "testpass123")

	handler := LoginHandler(testJWTSigningKey, credsPath, 24*time.Hour)

	body := `{"username":"wronguser","password":"testpass123"}`
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong username, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLogout(t *testing.T) {
	handler := LogoutHandler()
	req := httptest.NewRequest("POST", "/api/logout", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for logout, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Fatalf("expected status=ok, got: %v", resp)
	}

	// Check that cookie is cleared
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "hakase_token" && c.Value == "" && c.MaxAge < 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected hakase_token cookie to be cleared")
	}
}

func TestMeEndpoint(t *testing.T) {
	handler := MeHandler()

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("X-Hakase-User", "admin")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/me, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["username"] != "admin" {
		t.Fatalf("expected username=admin, got: %v", resp)
	}
}

func TestMeEndpointNoUser(t *testing.T) {
	handler := MeHandler()

	req := httptest.NewRequest("GET", "/api/me", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for /api/me without user header, got %d", w.Code)
	}
}

func TestHealthEndpoint(t *testing.T) {
	handler := HealthHandler()

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/health, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Fatalf("expected status=ok, got: %v", resp)
	}
}

func TestBearerTokenExtraction(t *testing.T) {
	token := newTestJWT("admin", time.Hour)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	extracted := extractToken(req)
	if extracted != token {
		t.Fatalf("expected extracted token to match, got %q", extracted)
	}
}

func TestCookieTokenExtraction(t *testing.T) {
	token := newTestJWT("admin", time.Hour)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "hakase_token", Value: token})

	extracted := extractToken(req)
	if extracted != token {
		t.Fatalf("expected extracted token to match, got %q", extracted)
	}
}

func TestBearerTakesPrecedenceOverCookie(t *testing.T) {
	bearerToken := newTestJWT("bearer-user", time.Hour)
	cookieToken := newTestJWT("cookie-user", time.Hour)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.AddCookie(&http.Cookie{Name: "hakase_token", Value: cookieToken})

	extracted := extractToken(req)
	if extracted != bearerToken {
		t.Fatalf("expected bearer token to take precedence, got %q", extracted)
	}
}

func TestNoTokenExtraction(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	extracted := extractToken(req)
	if extracted != "" {
		t.Fatalf("expected empty string for no token, got %q", extracted)
	}
}

func TestMalformedBearerExtraction(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer")
	extracted := extractToken(req)
	if extracted != "" {
		t.Fatalf("expected empty string for malformed bearer, got %q", extracted)
	}
}
