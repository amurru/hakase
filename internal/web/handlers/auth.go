package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"amurru/hakase/internal/auth"
	"github.com/golang-jwt/jwt/v5"
)

// Router is a minimal interface for registering HTTP routes.
type Router interface {
	Use(middlewares ...func(http.Handler) http.Handler)
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
	Route(pattern string, fn func(r Router))
}

// RegisterAuthRoutes registers auth-related API routes on the given router.
// Unauthenticated routes: /api/login, /api/health
// Authenticated routes (inside an auth-protected group): /api/me, /api/logout
func RegisterAuthRoutes(r Router, jwtKey []byte, credentialsPath string) {
	// Unauthenticated routes
	r.Get("/api/health", HealthHandler())
	r.Post("/api/login", LoginHandler(jwtKey, credentialsPath, 24*time.Hour))

	// Authenticated routes group
	r.Route("/api", func(r Router) {
		// Future auth middleware will be applied here by the caller (spa.go)
		r.Get("/me", MeHandler())
		r.Post("/logout", LogoutHandler())
	})
}

// LoginHandler returns a handler for POST /api/login.
// Accepts {username, password}, verifies via auth.VerifyPassword,
// returns JWT in HttpOnly cookie + JSON body {username, token}.
func LoginHandler(jwtKey []byte, credentialsPath string, tokenExpiry time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		creds, err := auth.LoadCredentials(credentialsPath)
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "not configured"})
			return
		}

		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		if req.Username != creds.Username || !auth.VerifyPassword(creds, req.Password) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
			return
		}

		// Generate JWT using the same library as middleware (jwt/v5)
		now := time.Now()
		claims := jwt.MapClaims{
			"username": req.Username,
			"iss":      "hakase",
			"sub":      req.Username,
			"iat":      float64(now.Unix()),
			"exp":      float64(now.Add(tokenExpiry).Unix()),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tokenStr, err := token.SignedString(jwtKey)
		if err != nil {
			log.Printf("auth: failed to sign JWT: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}

		// Set HttpOnly cookie
		// NOTE: no Secure flag - reverse proxy (nginx/Caddy) handles HTTPS termination.
		http.SetCookie(w, &http.Cookie{
			Name:     "hakase_token",
			Value:    tokenStr,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			Expires:  now.Add(tokenExpiry),
		})

		writeJSON(w, http.StatusOK, map[string]string{
			"username": req.Username,
			"token":    tokenStr,
		})
	}
}

// LogoutHandler returns a handler for POST /api/logout.
// Clears the auth cookie.
func LogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Clear the cookie by setting it with an expired date
		http.SetCookie(w, &http.Cookie{
			Name:     "hakase_token",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// MeHandler returns a handler for GET /api/me.
// Returns current user info from the X-Hakase-User header set by middleware.
func MeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.Header.Get("X-Hakase-User")
		if username == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"username": username})
	}
}

// HealthHandler returns a handler for GET /api/health.
// Unauthenticated health check.
func HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// extractToken pulls the JWT from the Authorization header or hakase_token cookie.
// Exported for reuse by middleware and tests.
func extractToken(r *http.Request) string {
	// Bearer token (API clients)
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		if tok, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
			return strings.TrimSpace(tok)
		}
	}

	// Cookie (web clients)
	cookie, err := r.Cookie("hakase_token")
	if err == nil && cookie.Value != "" {
		return cookie.Value
	}

	return ""
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("auth: failed to encode JSON response: %v", err)
	}
}
