package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"amurru/hakase/internal/sandbox"
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

// ---------------------------------------------------------------------------
// attachment handling
// ---------------------------------------------------------------------------

// testPNGBase64 is a 1x1 transparent PNG.
const testPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+ip1sAAAAASUVORK5CYII="

func TestBuildAttachmentPartsInlineImage(t *testing.T) {
	parts, refs, manifest, err := buildAttachmentParts([]incomingAttachment{
		{Name: "paste.png", MIME: "image/png", Data: testPNGBase64, Label: "[image 1]"},
	})
	if err != nil {
		t.Fatalf("buildAttachmentParts: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if parts[0].InlineData == nil || parts[0].InlineData.MIMEType != "image/png" {
		t.Fatalf("expected inline png part, got %+v", parts[0])
	}
	if len(parts[0].InlineData.Data) == 0 {
		t.Fatal("expected decoded bytes")
	}
	if len(refs) != 1 || refs[0].Name != "paste.png" || refs[0].Path != "" {
		t.Fatalf("unexpected refs: %+v", refs)
	}
	if len(manifest) != 1 || !strings.Contains(manifest[0], "paste.png (pasted image/png)") {
		t.Fatalf("unexpected manifest: %v", manifest)
	}
}

func TestBuildAttachmentPartsWorkspaceFiles(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(txt, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	imgBytes, _ := base64.StdEncoding.DecodeString(testPNGBase64)
	img := filepath.Join(dir, "img.png")
	if err := os.WriteFile(img, imgBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	prev := sandbox.CurrentSandbox
	defer func() { sandbox.CurrentSandbox = prev }()
	sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{
		WorkspaceRoots: []string{dir},
	})
	// Browse returns paths relative to the workspace root; the sandbox
	// resolves them against the process cwd (the workspace in production),
	// so pin the test cwd to the fake workspace.
	t.Chdir(dir)

	parts, refs, manifest, err := buildAttachmentParts([]incomingAttachment{
		{Name: "hello.txt", Path: "hello.txt", Label: "@hello.txt"},
		{Name: "img.png", Path: "img.png", MIME: "image/png", Label: "[image 1]"},
	})
	if err != nil {
		t.Fatalf("buildAttachmentParts: %v", err)
	}
	if len(parts) != 2 || len(refs) != 2 {
		t.Fatalf("expected 2 parts + 2 refs, got %d/%d", len(parts), len(refs))
	}
	if len(manifest) != 2 {
		t.Fatalf("expected 2 manifest lines, got %v", manifest)
	}
	if !strings.Contains(manifest[0], "hello.txt") || !strings.Contains(manifest[0], "text/plain") {
		t.Fatalf("text manifest line = %q", manifest[0])
	}
	// The image manifest line must carry the workspace path so the agent can
	// reference the file (e.g. as generate_video's image argument).
	if !strings.Contains(manifest[1], "img.png") || !strings.Contains(manifest[1], "image/png") {
		t.Fatalf("image manifest line = %q", manifest[1])
	}
	// Text attachments are wrapped in UNTRUSTED_DATA markers before reaching
	// the model; the raw content must still be present inside the wrapper.
	if !strings.Contains(parts[0].Text, "hello world") {
		t.Fatalf("text file part = %q", parts[0].Text)
	}
	if !strings.Contains(parts[0].Text, "<UNTRUSTED_DATA>") {
		t.Fatalf("text file part not wrapped as untrusted data: %q", parts[0].Text)
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MIMEType != "image/png" {
		t.Fatalf("expected inline image part, got %+v", parts[1])
	}
	if refs[0].Path == "" || !filepath.IsAbs(refs[0].Path) {
		t.Fatalf("ref should persist the resolved absolute path, got %q", refs[0].Path)
	}
}

func TestBuildAttachmentPartsRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	prev := sandbox.CurrentSandbox
	defer func() { sandbox.CurrentSandbox = prev }()
	sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{
		WorkspaceRoots: []string{dir},
	})

	cases := []struct {
		name string
		att  incomingAttachment
		want string
	}{
		{"bad base64", incomingAttachment{Name: "a.png", Data: "!!!not-base64!!!"}, "invalid base64"},
		{"outside workspace", incomingAttachment{Name: "evil.txt", Path: "/etc/hostname"}, ""},
		{"missing file", incomingAttachment{Name: "ghost.txt", Path: "nope.txt"}, ""},
		{"nil sandbox", func() incomingAttachment {
			sandbox.CurrentSandbox = nil
			return incomingAttachment{Name: "x.txt", Path: "x.txt"}
		}(), "sandbox is not initialized"},
	}
	for _, tc := range cases {
		sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{WorkspaceRoots: []string{dir}})
		if tc.name == "nil sandbox" {
			sandbox.CurrentSandbox = nil
		}
		_, _, _, err := buildAttachmentParts([]incomingAttachment{tc.att})
		if err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q missing %q", tc.name, err.Error(), tc.want)
		}
	}

	// Oversize text file (200KB cap).
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, make([]byte, 201*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{WorkspaceRoots: []string{dir}})
	if _, _, _, err := buildAttachmentParts([]incomingAttachment{{Name: "big.txt", Path: "big.txt"}}); err == nil {
		t.Fatal("expected oversize text rejection")
	}
}

func TestPostMessageWithAttachments(t *testing.T) {
	api, _, svc, router := newTestChatAPI(t)
	api.runner = &runner.Runner{}

	sess, err := svc.CreateSession("attachment test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	body := fmt.Sprintf(`{"content":"look at this","attachments":[{"name":"a.png","mime":"image/png","data":%q,"label":"[image 1]"}]}`, testPNGBase64)
	w := postMessage(t, router, sess.ID, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	msgs, err := svc.GetMessages(sess.ID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) == 0 || msgs[0].Role != "user" {
		t.Fatalf("expected persisted user message, got %+v", msgs)
	}
	if !strings.HasPrefix(msgs[0].Content, "look at this") {
		t.Fatalf("persisted content = %q, want it to start with the bare prompt", msgs[0].Content)
	}
	// The persisted content must carry the attachment manifest (path info the
	// agent needs on non-vision models) and stay byte-identical to the
	// in-flight request text for the history dedup.
	if !strings.Contains(msgs[0].Content, "[attachments]") || !strings.Contains(msgs[0].Content, "a.png (pasted image/png)") {
		t.Fatalf("persisted content missing attachment manifest: %q", msgs[0].Content)
	}
	if len(msgs[0].Attachments) != 1 {
		t.Fatalf("expected 1 persisted attachment ref, got %+v", msgs[0].Attachments)
	}
	if msgs[0].Attachments[0].MIME != "image/png" {
		t.Fatalf("ref mime = %q", msgs[0].Attachments[0].MIME)
	}
}

func TestPostMessageAttachmentsOnly(t *testing.T) {
	api, _, svc, router := newTestChatAPI(t)
	api.runner = &runner.Runner{}

	sess, err := svc.CreateSession("attachments only")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	body := fmt.Sprintf(`{"content":"","attachments":[{"name":"a.png","mime":"image/png","data":%q}]}`, testPNGBase64)
	w := postMessage(t, router, sess.ID, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for attachments-only message, got %d: %s", w.Code, w.Body.String())
	}
	msgs, _ := svc.GetMessages(sess.ID)
	if len(msgs) != 1 || len(msgs[0].Attachments) != 1 {
		t.Fatalf("expected one user message with a ref, got %+v", msgs)
	}
}

func TestPostMessageBadAttachmentRejected(t *testing.T) {
	api, _, svc, router := newTestChatAPI(t)
	api.runner = &runner.Runner{}

	sess, err := svc.CreateSession("bad attachment")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	w := postMessage(t, router, sess.ID, `{"content":"hi","attachments":[{"name":"a.png","data":"%%%bad%%%"}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid attachment, got %d: %s", w.Code, w.Body.String())
	}
	// Nothing may be persisted on rejection.
	msgs, _ := svc.GetMessages(sess.ID)
	for _, m := range msgs {
		if m.Role == "user" {
			t.Fatalf("user message must not persist when attachment conversion fails, got %+v", m)
		}
	}
}
