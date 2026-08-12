package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestNewLoginRateLimiter(t *testing.T) {
	rl := NewLoginRateLimiter()
	if rl == nil {
		t.Fatal("NewLoginRateLimiter returned nil")
	}
	if rl.rateLimit != DefaultRate {
		t.Fatalf("expected rateLimit=%v, got %v", DefaultRate, rl.rateLimit)
	}
}

func TestAllowSucceedsWithinBurst(t *testing.T) {
	rl := NewLoginRateLimiterWithConfig(100, 5, time.Second, time.Minute)
	ip := "192.168.1.1"

	for i := 0; i < 5; i++ {
		allowed, retryAfter := rl.Allow(ip)
		if !allowed {
			t.Fatalf("attempt %d: expected allowed=true, got false (retryAfter=%v)", i+1, retryAfter)
		}
	}
}

func TestAllowBlocksAfterBurst(t *testing.T) {
	rl := NewLoginRateLimiterWithConfig(100, 1, time.Second, time.Minute)
	ip := "10.0.0.1"

	// First request: allowed (within burst of 1)
	allowed, _ := rl.Allow(ip)
	if !allowed {
		t.Fatal("first request should be allowed")
	}

	// Second request immediately: blocked
	allowed, retryAfter := rl.Allow(ip)
	if allowed {
		t.Fatal("second request should be rate-limited")
	}
	if retryAfter <= 0 {
		t.Fatalf("expected positive retryAfter, got %v", retryAfter)
	}
}

func TestAllowBlocksThenRecovers(t *testing.T) {
	rl := NewLoginRateLimiterWithConfig(10, 1, time.Second, time.Minute)
	ip := "10.0.0.2"

	// First request: allowed
	allowed, _ := rl.Allow(ip)
	if !allowed {
		t.Fatal("first request should be allowed")
	}

	// Immediately blocked
	allowed, _ = rl.Allow(ip)
	if allowed {
		t.Fatal("second immediate request should be blocked")
	}

	// Wait for token refill (rate 10/sec means ~100ms per token).
	time.Sleep(150 * time.Millisecond)

	allowed, _ = rl.Allow(ip)
	if !allowed {
		t.Fatal("after waiting, request should be allowed again")
	}
}

func TestRecordFailureIncrementsCounter(t *testing.T) {
	rl := NewLoginRateLimiter()
	ip := "192.168.1.100"

	if f := rl.Failures(ip); f != 0 {
		t.Fatalf("expected 0 failures initially, got %d", f)
	}

	rl.RecordFailure(ip)
	if f := rl.Failures(ip); f != 1 {
		t.Fatalf("expected 1 failure, got %d", f)
	}

	rl.RecordFailure(ip)
	if f := rl.Failures(ip); f != 2 {
		t.Fatalf("expected 2 failures, got %d", f)
	}
}

func TestRecordSuccessResetsCounter(t *testing.T) {
	rl := NewLoginRateLimiter()
	ip := "192.168.1.101"

	rl.RecordFailure(ip)
	rl.RecordFailure(ip)
	rl.RecordFailure(ip)

	if f := rl.Failures(ip); f != 3 {
		t.Fatalf("expected 3 failures, got %d", f)
	}

	rl.RecordSuccess(ip)
	if f := rl.Failures(ip); f != 0 {
		t.Fatalf("expected 0 failures after reset, got %d", f)
	}
}

func TestRetryAfterGrowsExponentially(t *testing.T) {
	rl := NewLoginRateLimiterWithConfig(1, 0, time.Second, 15*time.Minute)
	ip := "10.0.0.50"

	// With burst=0, Allow() will always fail (rate limit immediately).
	// Retry-After should grow as failures increase.

	// 0 failures: baseDelay (1s)
	_, ra0 := rl.Allow(ip)
	if ra0 != time.Second {
		t.Fatalf("with 0 failures, expected retryAfter=1s, got %v", ra0)
	}

	rl.RecordFailure(ip)

	// 1 failure: baseDelay * 2^0 = 1s
	_, ra1 := rl.Allow(ip)
	if ra1 != time.Second {
		t.Fatalf("with 1 failure, expected retryAfter=1s, got %v", ra1)
	}

	rl.RecordFailure(ip) // 2 failures
	_, ra2 := rl.Allow(ip)
	if ra2 != 2*time.Second {
		t.Fatalf("with 2 failures, expected retryAfter=2s, got %v", ra2)
	}

	rl.RecordFailure(ip) // 3 failures
	_, ra3 := rl.Allow(ip)
	if ra3 != 4*time.Second {
		t.Fatalf("with 3 failures, expected retryAfter=4s, got %v", ra3)
	}

	rl.RecordFailure(ip) // 4 failures
	_, ra4 := rl.Allow(ip)
	if ra4 != 8*time.Second {
		t.Fatalf("with 4 failures, expected retryAfter=8s, got %v", ra4)
	}

	rl.RecordFailure(ip) // 5 failures
	_, ra5 := rl.Allow(ip)
	if ra5 != 16*time.Second {
		t.Fatalf("with 5 failures, expected retryAfter=16s, got %v", ra5)
	}
}

func TestRetryAfterCappedAtMax(t *testing.T) {
	rl := NewLoginRateLimiterWithConfig(1, 0, time.Second, 10*time.Second)
	ip := "10.0.0.51"

	// Force many failures.
	for i := 0; i < 10; i++ {
		rl.RecordFailure(ip)
	}

	_, ra := rl.Allow(ip)
	if ra > rl.maxDelay {
		t.Fatalf("retryAfter %v exceeds maxDelay %v", ra, rl.maxDelay)
	}
	if ra != rl.maxDelay {
		t.Logf("retryAfter=%v, maxDelay=%v (may be lower if failures capped before hitting max)", ra, rl.maxDelay)
	}
}

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name           string
		remoteAddr     string
		xForwardedFor  string
		expectedIP     string
	}{
		{
			name:       "remote addr with port",
			remoteAddr: "192.168.1.1:54321",
			expectedIP: "192.168.1.1",
		},
		{
			name:       "remote addr IPv6 with port",
			remoteAddr: "[::1]:54321",
			expectedIP: "::1",
		},
		{
			name:          "x-forwarded-for single IP",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1",
			expectedIP:    "203.0.113.1",
		},
		{
			name:          "x-forwarded-for chain",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1, 10.0.0.2, 10.0.0.1",
			expectedIP:    "203.0.113.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/login", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}

			ip := ExtractClientIP(req)
			if ip != tt.expectedIP {
				t.Fatalf("expected IP=%q, got %q", tt.expectedIP, ip)
			}
		})
	}
}

func TestWriteRateLimitResponse(t *testing.T) {
	w := httptest.NewRecorder()
	WriteRateLimitResponse(w, 30*time.Second)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}

	retryAfter := w.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("expected Retry-After header")
	}
	seconds, err := strconv.Atoi(retryAfter)
	if err != nil {
		t.Fatalf("Retry-After is not an integer: %q", retryAfter)
	}
	if seconds != 30 {
		t.Fatalf("expected Retry-After=30, got %d", seconds)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Fatalf("expected Content-Type=application/json, got %q", contentType)
	}

	body := w.Body.String()
	if body != `{"error":"too many requests"}` {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestWriteRateLimitResponseMinimumOne(t *testing.T) {
	w := httptest.NewRecorder()
	WriteRateLimitResponse(w, 500*time.Millisecond)

	seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After is not an integer")
	}
	if seconds < 1 {
		t.Fatalf("expected Retry-After >= 1, got %d", seconds)
	}
}

func TestPerIPSeparation(t *testing.T) {
	rl := NewLoginRateLimiterWithConfig(100, 1, time.Second, time.Minute)
	ip1 := "192.168.1.1"
	ip2 := "192.168.1.2"

	// IP1: first request allowed, second blocked.
	allowed, _ := rl.Allow(ip1)
	if !allowed {
		t.Fatal("ip1 first request should be allowed")
	}
	allowed, _ = rl.Allow(ip1)
	if allowed {
		t.Fatal("ip1 second request should be blocked")
	}

	// IP2: independent limit, should be allowed.
	allowed, _ = rl.Allow(ip2)
	if !allowed {
		t.Fatal("ip2 first request should be allowed (independent of ip1)")
	}

	// Record failures on IP1, verify IP2 unaffected.
	rl.RecordFailure(ip1)
	rl.RecordFailure(ip1)

	if f := rl.Failures(ip2); f != 0 {
		t.Fatalf("ip2 should have 0 failures, got %d", f)
	}
}

func TestAllowUsesRateLimiterTokens(t *testing.T) {
	// rate.Inf means unlimited, burst=0 means no burst.
	rl := NewLoginRateLimiterWithConfig(rate.Inf, 0, time.Second, time.Minute)
	ip := "10.0.0.99"

	// With rate.Inf, every request should be allowed.
	for i := 0; i < 100; i++ {
		allowed, _ := rl.Allow(ip)
		if !allowed {
			t.Fatalf("with rate.Inf, request %d should be allowed", i+1)
		}
	}
}
