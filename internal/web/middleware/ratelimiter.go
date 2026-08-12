// Package middleware provides HTTP middleware for the hakase web server.
package middleware

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Default rate limiter tunables.
const (
	DefaultRate     rate.Limit     = 1   // tokens per second
	DefaultBurst    int            = 10  // max burst
	DefaultBaseDelay time.Duration = 1 * time.Second
	DefaultMaxDelay  time.Duration = 15 * time.Minute
)

// LoginRateLimiter provides per-IP exponential rate limiting for login attempts.
// It uses a token-bucket (golang.org/x/time/rate) as the base rate limiter,
// with exponential backoff on repeated failures.
//
// When a login fails, the failure counter for that IP is incremented.
// Subsequent attempts that hit the rate limit return HTTP 429 with a
// Retry-After header whose value grows exponentially with the failure count.
// A successful login resets the failure counter.
type LoginRateLimiter struct {
	mu        sync.Mutex
	entries   map[string]*ipEntry
	rateLimit rate.Limit
	burst     int
	baseDelay time.Duration
	maxDelay  time.Duration
}

// ipEntry holds per-IP rate limiting state.
type ipEntry struct {
	limiter  *rate.Limiter
	failures int
	lastSeen time.Time
}

// NewLoginRateLimiter creates a LoginRateLimiter with default settings.
func NewLoginRateLimiter() *LoginRateLimiter {
	return NewLoginRateLimiterWithConfig(DefaultRate, DefaultBurst, DefaultBaseDelay, DefaultMaxDelay)
}

// NewLoginRateLimiterWithConfig creates a LoginRateLimiter with custom settings.
// Use this in tests to control rate limiting behavior.
func NewLoginRateLimiterWithConfig(rateLimit rate.Limit, burst int, baseDelay, maxDelay time.Duration) *LoginRateLimiter {
	rl := &LoginRateLimiter{
		entries:   make(map[string]*ipEntry),
		rateLimit: rateLimit,
		burst:     burst,
		baseDelay: baseDelay,
		maxDelay:  maxDelay,
	}
	go rl.cleanupLoop(10 * time.Minute)
	return rl
}

// Allow checks whether the given IP is allowed to make a login attempt.
// Returns (true, 0) if allowed, or (false, retryAfter) if rate-limited.
func (rl *LoginRateLimiter) Allow(ip string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	e := rl.getOrCreateLocked(ip)

	if !e.limiter.Allow() {
		retryAfter := rl.computeRetryAfter(e.failures)
		return false, retryAfter
	}
	return true, 0
}

// RecordFailure records a failed login attempt, incrementing the
// failure counter for exponential backoff.
func (rl *LoginRateLimiter) RecordFailure(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	e := rl.getOrCreateLocked(ip)
	e.failures++
	// Cap failures to prevent integer overflow in backoff calculation.
	if e.failures > 20 {
		e.failures = 20
	}
}

// RecordSuccess resets the failure counter for the given IP,
// clearing the exponential backoff.
func (rl *LoginRateLimiter) RecordSuccess(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	e := rl.entries[ip]
	if e == nil {
		return
	}
	e.failures = 0
}

// Failures returns the number of consecutive failures for an IP.
// Exported for testing.
func (rl *LoginRateLimiter) Failures(ip string) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	e := rl.entries[ip]
	if e == nil {
		return 0
	}
	return e.failures
}

// computeRetryAfter calculates the Retry-After duration using exponential
// backoff: baseDelay * 2^(failures-1), capped at maxDelay.
// Must be called with rl.mu held.
func (rl *LoginRateLimiter) computeRetryAfter(failures int) time.Duration {
	if failures == 0 {
		return rl.baseDelay
	}
	delay := rl.baseDelay * time.Duration(int64(1)<<uint(min(failures-1, 20)))
	if delay > rl.maxDelay {
		return rl.maxDelay
	}
	return delay
}

// getOrCreateLocked returns the ipEntry for the given IP, creating one
// if it does not exist. Must be called with rl.mu held.
func (rl *LoginRateLimiter) getOrCreateLocked(ip string) *ipEntry {
	e, ok := rl.entries[ip]
	if !ok {
		e = &ipEntry{
			limiter:  rate.NewLimiter(rl.rateLimit, rl.burst),
			lastSeen: time.Now(),
		}
		rl.entries[ip] = e
	}
	e.lastSeen = time.Now()
	return e
}

// cleanupLoop periodically removes stale entries to prevent memory leaks.
func (rl *LoginRateLimiter) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		rl.cleanup(30 * time.Minute)
	}
}

// cleanup removes entries that haven't been seen for longer than maxAge.
func (rl *LoginRateLimiter) cleanup(maxAge time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for ip, e := range rl.entries {
		if e.lastSeen.Before(cutoff) {
			delete(rl.entries, ip)
		}
	}
}

// ExtractClientIP extracts the client IP from the request.
// Checks X-Forwarded-For header first (for reverse proxy support),
// then falls back to RemoteAddr.
func ExtractClientIP(r *http.Request) string {
	// Trust X-Forwarded-For if present (reverse proxy).
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may contain multiple IPs (chain).
		// The leftmost is the original client.
		if idx := indexByte(xff, ','); idx != -1 {
			xff = xff[:idx]
		}
		return xff
	}

	// Fall back to RemoteAddr (strip port).
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// If SplitHostPort fails, return RemoteAddr as-is
		// (e.g., for IPv6 addresses without port).
		return r.RemoteAddr
	}
	return host
}

// indexByte returns the index of the first occurrence of c in s, or -1.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// WriteRateLimitResponse writes a 429 Too Many Requests response
// with a Retry-After header and JSON error body.
func WriteRateLimitResponse(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int64(retryAfter.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	// Use fmt.Fprintf to avoid importing encoding/json in the middleware package.
	// The handlers package has its own writeJSON helper.
	msg := `{"error":"too many requests"}`
	w.Write([]byte(msg))
}
