package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"amurru/hakase/internal/finops"

	"github.com/go-chi/chi/v5"
)

func statsTestRouter() *chi.Mux {
	api := &ChatAPI{}
	r := chi.NewRouter()
	r.Get("/stats", api.GetStats)
	return r
}

func TestGetStatsEmpty(t *testing.T) {
	finops.Configure(finops.Settings{})
	r := statsTestRouter()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad body: %v", err)
	}
	if resp["enabled"] != false || resp["turns"].(float64) != 0 {
		t.Errorf("empty ledger should report zeros: %v", resp)
	}
}

func TestGetStatsSummaryAndFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	finops.Configure(finops.Settings{Enabled: true, Path: path, Table: finops.DefaultPriceTable()})
	defer finops.Configure(finops.Settings{})
	finops.Record("sess-a", finops.UsageRecord{Prompt: 1000, Candidates: 500, Total: 1500, Model: "gemini-2.5-flash", Reason: finops.ReasonMain}, nil)
	finops.Record("sess-b", finops.UsageRecord{Prompt: 200, Total: 200, Model: "mystery", Reason: finops.ReasonSingle}, nil)

	r := statsTestRouter()
	get := func(target string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d: %s", target, rec.Code, rec.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad body: %v", err)
		}
		return resp
	}

	full := get("/stats")
	if full["enabled"] != true || full["turns"].(float64) != 2 {
		t.Errorf("full summary wrong: %v", full)
	}
	usage := full["usage"].(map[string]any)
	if usage["total_tokens"].(float64) != 1700 {
		t.Errorf("usage tokens wrong: %v", usage)
	}
	if _, ok := full["cost_details"]; !ok {
		t.Error("missing Langfuse-compatible cost_details")
	}

	one := get("/stats?session=sess-a")
	if one["turns"].(float64) != 1 {
		t.Errorf("session filter wrong: %v", one)
	}

	bad := httptest.NewRecorder()
	r.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/stats?since=soon", nil))
	if bad.Code != http.StatusBadRequest {
		t.Errorf("bad since should 400, got %d", bad.Code)
	}
}
