package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSecurityHeadersMiddleware verifies that the SecurityHeaders middleware
// sets the four required security headers on every response: a strict
// Content-Security-Policy, X-Content-Type-Options: nosniff, X-Frame-Options:
// DENY, and Referrer-Policy. It also confirms the downstream handler still
// runs (the middleware is additive, not a gate).
func TestSecurityHeadersMiddleware(t *testing.T) {
	handler := SecurityHeaders()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	checks := map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
	}
	for header, wantSubstr := range checks {
		got := rec.Header().Get(header)
		if got == "" {
			t.Errorf("missing header %q", header)
			continue
		}
		if wantSubstr != "" && !contains(got, wantSubstr) {
			t.Errorf("header %q = %q, want it to contain %q", header, got, wantSubstr)
		}
	}
}

// TestSecurityHeadersCSPBlocksFraming verifies the CSP includes
// frame-ancestors 'none' so the app cannot be embedded in a frame.
func TestSecurityHeadersCSPBlocksFraming(t *testing.T) {
	handler := SecurityHeaders()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q, want it to contain frame-ancestors 'none'", csp)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
