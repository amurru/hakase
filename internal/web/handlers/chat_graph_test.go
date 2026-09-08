package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/web/sse"

	"github.com/go-chi/chi/v5"
)

// graphTestRouter mounts GetGraph on a fresh router for the given bridge.
func graphTestRouter(b *sse.EventBridge) *chi.Mux {
	api := &ChatAPI{bridge: b}
	r := chi.NewRouter()
	r.Get("/sessions/{id}/graph", api.GetGraph)
	return r
}

func TestGetGraphReturnsRetainedEvents(t *testing.T) {
	b := sse.NewEventBridge()
	b.SendGraph("sess-1", interfaces.GraphEvent{Type: interfaces.GraphAgentStart, NodeID: "task_root", Agent: "orchestrator"})
	b.SendGraph("sess-1", interfaces.GraphEvent{Type: interfaces.GraphToolStart, NodeID: "task_root", CallID: "c1", Tool: "download"})
	// A second session must not leak into sess-1's backfill.
	b.SendGraph("sess-2", interfaces.GraphEvent{Type: interfaces.GraphAgentStart, NodeID: "other"})

	r := graphTestRouter(b)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sessions/sess-1/graph", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response body: %v", err)
	}
	if len(resp.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(resp.Events))
	}
	if resp.Events[0]["type"] != interfaces.GraphAgentStart || resp.Events[1]["seq"] != float64(2) {
		t.Errorf("unexpected events: %v", resp.Events)
	}
}

func TestGetGraphEmptyHistoryIsEmptyArray(t *testing.T) {
	r := graphTestRouter(sse.NewEventBridge())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sessions/sess-fresh/graph", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != `{"events":[]}`+"\n" {
		t.Errorf("expected empty events array (not null), got %q", got)
	}
}
