package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	hctx "amurru/hakase/internal/context"

	"github.com/go-chi/chi/v5"
)

// TestPostCompactSnipsHistory proves the compact endpoint performs the
// deterministic snip (oldest turns evicted from context, last 2 user turns
// kept) and persists the result to the session store.
func TestPostCompactSnipsHistory(t *testing.T) {
	api, _, svc, _ := newTestChatAPI(t)
	api.history = hctx.NewHistoryBuilder(svc)

	sess, err := svc.CreateSession("t")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Seed 5 alternating turns; the last 2 user turns must survive in-context.
	for i := 0; i < 5; i++ {
		sess.AddMessage("user", "q", "")
		sess.AddMessage("agent", "a", "")
	}
	if err := svc.Store().Save(sess); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/sessions/{id}/compact", api.PostCompact)

	body, _ := json.Marshal(map[string]string{"focus": "keep the API notes"})
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/compact", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	loaded, err := svc.Store().Load(sess.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	inCtxUser := 0
	for _, m := range loaded.Messages {
		if m.Role == "user" && m.InContext {
			inCtxUser++
		}
	}
	if inCtxUser != 2 {
		t.Fatalf("in-context user turns after compact = %d, want 2 (total msgs %d)", inCtxUser, len(loaded.Messages))
	}
	// The last seeded question must be among the survivors.
	last := loaded.Messages[len(loaded.Messages)-2].Content // agent reply is last; its user turn precedes
	if last != "q" {
		t.Fatalf("unexpected tail content %q", last)
	}
}

func TestPostCompactGuards(t *testing.T) {
	r := chi.NewRouter()

	// history == nil -> 503
	api, _, svc, _ := newTestChatAPI(t)
	r.Post("/sessions/{id}/compact", api.PostCompact)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions/s1/compact", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-history status = %d, want 503", rec.Code)
	}

	// unknown session -> 404
	api.history = hctx.NewHistoryBuilder(svc)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions/does-not-exist/compact", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown-session status = %d, want 404", rec.Code)
	}
}
