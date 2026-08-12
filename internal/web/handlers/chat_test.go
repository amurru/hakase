package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/web/sse"
	"github.com/go-chi/chi/v5"
	"google.golang.org/adk/v2/runner"
)

// newTestChatAPI creates a ChatAPI registered on a chi router for testing.
func newTestChatAPI(t *testing.T) (*ChatAPI, *sse.EventBridge, *hakasesession.SessionService, chi.Router) {
	t.Helper()
	bridge := sse.NewEventBridge()
	store, err := hakasesession.NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create session store: %v", err)
	}
	svc, err := hakasesession.NewSessionService(store)
	if err != nil {
		t.Fatalf("failed to create session service: %v", err)
	}
	r := chi.NewRouter()
	api := &ChatAPI{
		bridge:        bridge,
		sessionSvc:    svc,
		runner:        nil, // set per-test
		runtime:       nil,
		runSemaphores: make(map[string]*sessionSem),
	}
	r.Post("/sessions/{id}/messages", api.PostMessage)
	return api, bridge, svc, r
}

// newTestSessionSvc is a helper reused from session_test.go; redeclared here
// for self-contained test setup.
func chatTestSessionSvc(t *testing.T) *hakasesession.SessionService {
	t.Helper()
	store, err := hakasesession.NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create session store: %v", err)
	}
	svc, err := hakasesession.NewSessionService(store)
	if err != nil {
		t.Fatalf("failed to create session service: %v", err)
	}
	return svc
}

func postMessage(t *testing.T, router chi.Router, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sessionID+"/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// ---------------------------------------------------------------------------
// sessionSem unit tests
// ---------------------------------------------------------------------------

func TestSessionSemAcquireRelease(t *testing.T) {
	s := &sessionSem{}
	max := 3

	for i := 0; i < max; i++ {
		if !s.acquire(max) {
			t.Fatalf("acquire %d should succeed", i+1)
		}
	}
	if s.acquire(max) {
		t.Fatal("acquire should fail when saturated")
	}

	// Release one, should be able to acquire again.
	s.release()
	if !s.acquire(max) {
		t.Fatal("acquire should succeed after release")
	}
}

func TestSessionSemConcurrent(t *testing.T) {
	s := &sessionSem{}
	max := 5
	var wg sync.WaitGroup
	acquired := make(chan bool, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acquired <- s.acquire(max)
		}()
	}
	wg.Wait()
	close(acquired)

	got := 0
	for ok := range acquired {
		if ok {
			got++
		}
	}
	if got != max {
		t.Fatalf("expected %d concurrent acquires, got %d", max, got)
	}

	// Release all and verify counter resets.
	for i := 0; i < max; i++ {
		s.release()
	}
	for i := 0; i < max; i++ {
		if !s.acquire(max) {
			t.Fatalf("acquire %d should succeed after full release", i+1)
		}
	}
}

func TestSessionSemNegativeCounter(t *testing.T) {
	s := &sessionSem{}
	// Release on empty counter should not panic.
	s.release()
	if s.counter != -1 {
		t.Fatalf("counter should be -1 after release on empty, got %d", s.counter)
	}
	// Acquire should still work after over-release.
	if !s.acquire(1) {
		t.Fatal("acquire should succeed even after over-release")
	}
}

// ---------------------------------------------------------------------------
// Handler integration tests
// ---------------------------------------------------------------------------

func TestPostMessageNoRunner(t *testing.T) {
	api, _, _, router := newTestChatAPI(t)
	_ = api

	w := postMessage(t, router, "ses-1", `{"content":"hello"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// Without a runner, no semaphore is acquired, and 429 should never fire.
	// Pre-saturate the semaphore manually to prove it's independent.
	api.semMu.Lock()
	sem := api.getOrCreateSem("ses-1")
	for i := 0; i < maxConcurrentAgentRuns; i++ {
		sem.acquire(maxConcurrentAgentRuns)
	}
	api.semMu.Unlock()

	// Still returns 202 because the runner is nil (block skipped).
	w2 := postMessage(t, router, "ses-1", `{"content":"hello again"}`)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with nil runner even with saturated sem, got %d", w2.Code)
	}
}

func TestPostMessageRejectsWhenSaturated(t *testing.T) {
	api, _, _, router := newTestChatAPI(t)
	// Set a runner so the semaphore path is exercised.
	api.runner = &runner.Runner{}

	// Pre-saturate the session's semaphore by acquiring all slots.
	api.semMu.Lock()
	sem := api.getOrCreateSem("ses-1")
	for i := 0; i < maxConcurrentAgentRuns; i++ {
		if !sem.acquire(maxConcurrentAgentRuns) {
			t.Fatalf("pre-acquire %d should succeed", i+1)
		}
	}
	api.semMu.Unlock()

	w := postMessage(t, router, "ses-1", `{"content":"hello"}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when saturated, got %d: %s", w.Code, w.Body.String())
	}

	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] == "" {
		t.Fatal("expected error message in 429 response")
	}
}

func TestPostMessageOkWhenNotSaturated(t *testing.T) {
	api, _, _, router := newTestChatAPI(t)
	api.runner = &runner.Runner{}

	w := postMessage(t, router, "ses-1", `{"content":"hello"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDifferentSessionsIndependentLimits(t *testing.T) {
	api, _, _, router := newTestChatAPI(t)
	api.runner = &runner.Runner{}

	// Saturate session A.
	api.semMu.Lock()
	semA := api.getOrCreateSem("ses-a")
	for i := 0; i < maxConcurrentAgentRuns; i++ {
		semA.acquire(maxConcurrentAgentRuns)
	}
	api.semMu.Unlock()

	// Session A returns 429.
	wA := postMessage(t, router, "ses-a", `{"content":"hi"}`)
	if wA.Code != http.StatusTooManyRequests {
		t.Fatalf("ses-a: expected 429, got %d", wA.Code)
	}

	// Session B should still accept (independent limit).
	wB := postMessage(t, router, "ses-b", `{"content":"hi"}`)
	if wB.Code != http.StatusAccepted {
		t.Fatalf("ses-b: expected 202 (independent limit), got %d: %s", wB.Code, wB.Body.String())
	}
}

func TestSemaphoreReleasedAfterRun(t *testing.T) {
	api, _, _, router := newTestChatAPI(t)
	api.runner = &runner.Runner{}

	// Send a message to acquire the semaphore.
	w := postMessage(t, router, "ses-1", `{"content":"run me"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}

	// The agent goroutine runs and completes quickly (zero-value runner).
	// Poll until the semaphore is released.
	deadline := time.After(2 * time.Second)
	for {
		api.semMu.Lock()
		sem, ok := api.runSemaphores["ses-1"]
		released := !ok || sem.counter == 0
		api.semMu.Unlock()

		if released {
			break
		}

		select {
		case <-deadline:
			api.semMu.Lock()
			c := 0
			if s, ok := api.runSemaphores["ses-1"]; ok {
				c = s.counter
			}
			api.semMu.Unlock()
			t.Fatalf("semaphore was not released after agent run completed, counter=%d", c)
		case <-time.After(10 * time.Millisecond):
		}
	}

	// After release, another message should succeed.
	w2 := postMessage(t, router, "ses-1", `{"content":"second run"}`)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("expected 202 after release, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestPostMessageBadRequest(t *testing.T) {
	_, _, _, router := newTestChatAPI(t)

	// Missing content.
	w := postMessage(t, router, "ses-1", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty content, got %d", w.Code)
	}

	// Missing session ID (chi won't match, but for direct call).
	w2 := postMessage(t, router, "", `{"content":"hi"}`)
	if w2.Code == http.StatusAccepted || w2.Code == http.StatusTooManyRequests {
		t.Fatalf("expected error for missing session, got %d", w2.Code)
	}
}
