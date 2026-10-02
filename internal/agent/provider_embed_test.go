package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// embedStub serves shuffled-index embeddings responses for EmbedTexts tests.
func embedStub(t *testing.T, dim int, calls *atomic.Int32, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		if status != 0 {
			http.Error(w, "upstream exploded", status)
			return
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Model == "" || len(req.Input) == 0 {
			http.Error(w, "model/input required", http.StatusBadRequest)
			return
		}
		// Respond in reverse index order so tests prove index placement.
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		data := make([]datum, len(req.Input))
		for i, text := range req.Input {
			vec := make([]float32, dim)
			vec[0] = float32(len(text))
			data[len(req.Input)-1-i] = datum{Index: i, Embedding: vec}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

func TestEmbedTextsOrder(t *testing.T) {
	var calls atomic.Int32
	srv := embedStub(t, 4, &calls, 0)
	defer srv.Close()
	p := &OpenAIProvider{}
	vecs, err := p.EmbedTexts(context.Background(), "k", srv.URL, "m", []string{"a", "bb", "ccc"})
	if err != nil {
		t.Fatalf("EmbedTexts: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("got %d vectors, want 3", len(vecs))
	}
	for i, want := range []float32{1, 2, 3} {
		if vecs[i][0] != want {
			t.Errorf("vec[%d][0] = %v, want %v (index placement broken)", i, vecs[i][0], want)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestEmbedTextsBatching(t *testing.T) {
	var calls atomic.Int32
	srv := embedStub(t, 2, &calls, 0)
	defer srv.Close()
	p := &OpenAIProvider{}
	texts := make([]string, embedBatchSize+1)
	for i := range texts {
		texts[i] = fmt.Sprintf("t%d", i)
	}
	vecs, err := p.EmbedTexts(context.Background(), "k", srv.URL, "m", texts)
	if err != nil {
		t.Fatalf("EmbedTexts: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors, want %d", len(vecs), len(texts))
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (batch split)", calls.Load())
	}
}

func TestEmbedTextsError(t *testing.T) {
	srv := embedStub(t, 2, nil, http.StatusUnauthorized)
	defer srv.Close()
	p := &OpenAIProvider{}
	_, err := p.EmbedTexts(context.Background(), "k", srv.URL, "m", []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want HTTP 401 with body", err)
	}
}

func TestEmbedTextsEmpty(t *testing.T) {
	var calls atomic.Int32
	srv := embedStub(t, 2, &calls, 0)
	defer srv.Close()
	p := &OpenAIProvider{}
	vecs, err := p.EmbedTexts(context.Background(), "k", srv.URL, "m", nil)
	if err != nil || vecs != nil {
		t.Errorf("empty input: vecs=%v err=%v, want nil nil (no request)", vecs, err)
	}
	if calls.Load() != 0 {
		t.Errorf("calls = %d, want 0", calls.Load())
	}
}

func TestEmbedTextsMissingIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer srv.Close()
	p := &OpenAIProvider{}
	if _, err := p.EmbedTexts(context.Background(), "k", srv.URL, "m", []string{"hi"}); err == nil {
		t.Error("missing vector: expected error, got nil")
	}
}
