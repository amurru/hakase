package web

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTManager handles JWT token generation and validation.
// This is a self-contained implementation for the web package.
// When internal/auth lands (task 15), this can be replaced with auth.JWTManager.
type JWTManager struct {
	signingKey []byte
	issuer     string
}

// Claims represents the JWT claims used by hakase web sessions.
type Claims struct {
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// NewJWTManager creates a JWT manager with the given signing key.
func NewJWTManager(signingKey []byte, issuer string) *JWTManager {
	if issuer == "" {
		issuer = "hakase"
	}
	return &JWTManager{
		signingKey: signingKey,
		issuer:     issuer,
	}
}

// SigningKey returns the signing key (used to reconstruct the manager).
func (m *JWTManager) SigningKey() []byte {
	return m.signingKey
}

// GenerateToken creates a signed JWT for the given username.
func (m *JWTManager) GenerateToken(username string, expiry time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.signingKey)
}

// ValidateToken parses and validates a JWT token string.
// Returns the claims on success, or an error on any failure.
func (m *JWTManager) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{},
		func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return m.signingKey, nil
		})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

// AuthMiddleware creates a chi middleware that validates JWT tokens.
// Tokens are accepted from either:
//   - Authorization: Bearer <token> header (API clients)
//   - hakase_token cookie (web browser)
//
// On failure, returns 401 JSON.
func AuthMiddleware(jwtKey []byte) func(http.Handler) http.Handler {
	mgr := NewJWTManager(jwtKey, "hakase")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := extractToken(r)
			if tokenStr == "" {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			claims, err := mgr.ValidateToken(tokenStr)
			if err != nil {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			// Store username in header for downstream handlers.
			// Use a custom header to avoid conflicts with real HTTP headers.
			r.Header.Set("X-Hakase-User", claims.Username)
			next.ServeHTTP(w, r)
		})
	}
}

// extractToken pulls the JWT from the Authorization header or hakase_token cookie.
func extractToken(r *http.Request) string {
	// Bearer token (API clients)
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		tok, ok := strings.CutPrefix(authHeader, "Bearer ")
		if ok {
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

// CORSMiddleware adds permissive CORS headers for development.
// In production, the SPA and API are served from the same origin so CORS is not needed.
func CORSMiddleware() func(http.Handler) http.Handler {
	allowedOrigins := map[string]bool{
		"http://localhost:5173":   true, // Vite dev server
		"http://localhost:3000":   true, // alternative
		"http://127.0.0.1:5173":  true,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			// For preflight requests, check if origin is allowed.
			if r.Method == "OPTIONS" {
				if allowedOrigins[origin] {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With")
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					w.Header().Set("Access-Control-Max-Age", "86400")
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}

			// For normal requests, set origin if allowed.
			if allowedOrigins[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogger returns a middleware that logs each request with method, path, status, and duration.
func RequestLogger() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(ww, r)
			duration := time.Since(start)
			log.Printf("web: %s %s %d %s", r.Method, r.URL.Path, ww.status, duration)
		})
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// writeJSON writes a JSON success response.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// constantTimeCompare is a wrapper around subtle.ConstantTimeCompare for testing.
var constantTimeCompare = subtle.ConstantTimeCompare
