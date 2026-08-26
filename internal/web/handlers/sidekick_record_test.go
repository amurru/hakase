package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/config"
	hakasesession "amurru/hakase/internal/session"
	hakasesidekick "amurru/hakase/internal/sidekick"

	"github.com/go-chi/chi/v5"
	"google.golang.org/adk/v2/model"
)

// waitForMessages polls the store until the session has at least n messages
// or the deadline passes (Ask runs in a detached goroutine).
func waitForMessages(t *testing.T, store *hakasesession.SessionStore, id string, n int) *hakasesession.Session {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sess, err := store.Load(id)
		if err == nil && sess != nil && len(sess.Messages) >= n {
			return sess
		}
		time.Sleep(20 * time.Millisecond)
	}
	sess, err := store.Load(id)
	if err != nil {
		t.Fatalf("session never reached %d messages (load: %v)", n, err)
	}
	t.Fatalf("session never reached %d messages; has %d", n, len(sess.Messages))
	return nil
}

// TestPostSidekickRecordsError proves a failed Ask leaves an auditable trace:
// the recorded question is followed by an "[sidekick error]" answer record,
// so an unanswered question in sessions/<id>.json is never ambiguous.
func TestPostSidekickRecordsError(t *testing.T) {
	api, _, svc, _ := newTestChatAPI(t)

	on := true
	sk := hakasesidekick.New(
		&config.SidekickConfig{Enabled: &on, Mode: "on_demand", ModelName: "stub"},
		&stubSidekickLLM{fail: true},
		nil, nil,
	)
	rt := &hakaseagent.Runtime{}
	rt.SetSidekick(sk)
	api.runtime = rt

	sess, err := svc.CreateSession("t")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/sessions/{id}/sidekick", api.PostSidekick)

	body, _ := json.Marshal(map[string]string{"question": "will this fail?"})
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/sidekick", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}

	loaded := waitForMessages(t, svc.Store(), sess.ID, 2)
	q := loaded.Messages[0]
	a := loaded.Messages[1]
	if q.Role != "user" || q.Content != "will this fail?" {
		t.Fatalf("bad question record: %+v", q)
	}
	if a.Role != hakasesidekick.Role || a.Kind != hakasesession.MessageKindSidekick {
		t.Fatalf("bad answer record: %+v", a)
	}
	if !strings.HasPrefix(a.Content, "[sidekick error]") {
		t.Fatalf("expected error-prefixed answer, got %q", a.Content)
	}
}

// stubSidekickLLM is a minimal model.LLM double returning one fixed text
// response so Sidekick.Ask completes without network access. When fail is
// set, GenerateContent yields an error to exercise the failure path. lastReq
// captures the most recent request so tests can assert prompt content.
type stubSidekickLLM struct {
	reply   string
	fail    bool
	lastReq *model.LLMRequest
}

func (f *stubSidekickLLM) Name() string { return "stub-sidekick" }

func (f *stubSidekickLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	f.lastReq = req
	return func(yield func(*model.LLMResponse, error) bool) {
		if f.fail {
			yield(nil, errors.New("boom: provider unreachable"))
			return
		}
		yield(&model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{{Text: f.reply}}},
		}, nil)
	}
}

// reqText flattens the captured request's contents for assertions.
func reqText(t *testing.T, req *model.LLMRequest) string {
	t.Helper()
	if req == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p != nil {
				sb.WriteString(p.Text)
			}
		}
	}
	return sb.String()
}

// TestPostSidekickRecordsExchangeInSession proves the session JSON records a
// sidekick interaction with correct provenance: the question as a user turn
// tagged kind "sidekick", the answer with role "sidekick".
func TestPostSidekickRecordsExchangeInSession(t *testing.T) {
	api, _, svc, _ := newTestChatAPI(t)

	on := true
	sk := hakasesidekick.New(
		&config.SidekickConfig{Enabled: &on, Mode: "on_demand", ModelName: "stub"},
		&stubSidekickLLM{reply: "X is 42"},
		nil, nil,
	)
	rt := &hakaseagent.Runtime{}
	rt.SetSidekick(sk)
	api.runtime = rt

	sess, err := svc.CreateSession("t")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/sessions/{id}/sidekick", api.PostSidekick)

	body, _ := json.Marshal(map[string]string{"question": "what is X?"})
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/sidekick", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	// The Ask runs in a detached goroutine; poll until both turns land.
	store := svc.Store()
	var loaded *hakasesession.Session
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		loaded, err = store.Load(sess.ID)
		if err == nil && len(loaded.Messages) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if loaded == nil || len(loaded.Messages) < 2 {
		t.Fatalf("session recorded %d messages, want 2 (err=%v)", len(loaded.Messages), err)
	}

	q := loaded.Messages[0]
	if q.Role != "user" || q.Kind != hakasesession.MessageKindSidekick || q.Content != "what is X?" {
		t.Fatalf("bad question record: %+v", q)
	}
	a := loaded.Messages[1]
	if a.Role != hakasesidekick.Role || a.Kind != hakasesession.MessageKindSidekick || a.Content != "X is 42" {
		t.Fatalf("bad answer record: %+v", a)
	}
	if !q.InContext || !a.InContext {
		t.Fatal("records must be in-context like watchdog notes")
	}
}

// TestPostSidekickIncludesSessionHistory proves an on-demand ask frames the
// question with the recent session transcript, so follow-ups like "what's
// your take?" see the prior answer.
func TestPostSidekickIncludesSessionHistory(t *testing.T) {
	api, _, svc, _ := newTestChatAPI(t)

	llm := &stubSidekickLLM{reply: "it holds up"}
	on := true
	sk := hakasesidekick.New(
		&config.SidekickConfig{Enabled: &on, Mode: "on_demand", ModelName: "stub", TranscriptWindowChars: 6000},
		llm, nil, nil,
	)
	rt := &hakaseagent.Runtime{}
	rt.SetSidekick(sk)
	api.runtime = rt

	sess, err := svc.CreateSession("t")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess.AddMessage("user", "what is X?", "")
	sess.AddMessage("agent", "X is 42 and quite reliable", "")
	if err := svc.Store().Save(sess); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/sessions/{id}/sidekick", api.PostSidekick)

	body, _ := json.Marshal(map[string]string{"question": "what's your take?"})
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/sidekick", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}

	waitForMessages(t, svc.Store(), sess.ID, 3)

	prompt := reqText(t, llm.lastReq)
	for _, want := range []string{"[CONVERSATION SO FAR]", "X is 42 and quite reliable", "[YOUR TASK]", "what's your take?"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\ngot: %s", want, prompt)
		}
	}
}
