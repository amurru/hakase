// restore_test.go - the snapshots list + restore endpoints
// (docs/session-rewind/spec.md SR-004).
package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	hakasesession "amurru/hakase/internal/session"

	"github.com/go-chi/chi/v5"
)

func newRestoreRouter(api *ChatAPI) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/sessions/{id}/snapshots", api.GetSnapshots)
	r.Post("/sessions/{id}/restore", api.PostRestore)
	return r
}

// TestPostRestoreTruncatesAndCreatesUndo proves restore rewinds the message
// list to the snapshot and leaves a pre-restore undo snapshot behind.
func TestPostRestoreTruncatesAndCreatesUndo(t *testing.T) {
	api, _, svc, _ := newTestChatAPI(t)
	store := svc.Store()

	sess, err := svc.CreateSession("t")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess.AddMessage("user", "q1", "")
	sess.AddMessage("agent", "a1", "")
	if err := store.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Snapshot of the state BEFORE the bad turn.
	name, err := store.SaveSnapshot(sess, hakasesession.SnapshotTriggerPre)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// The bad turn.
	sess.AddMessage("user", "rm everything", "")
	sess.AddMessage("agent", "done, deleted", "")
	if err := store.Save(sess); err != nil {
		t.Fatalf("save 2: %v", err)
	}

	r := newRestoreRouter(api)
	body, _ := json.Marshal(map[string]string{"snapshot": name})
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/restore", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status   string `json:"status"`
		Messages int    `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "restored" || resp.Messages != 2 {
		t.Fatalf("response %+v, want restored/2", resp)
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(loaded.Messages) != 2 || loaded.Messages[1].Content != "a1" {
		t.Fatalf("session not rewound: %d messages", len(loaded.Messages))
	}

	// The undo point exists and holds the pre-restore state (4 messages).
	snaps, err := store.ListSnapshots(sess.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var undo *hakasesession.SnapshotInfo
	for i := range snaps {
		if snaps[i].Trigger == hakasesession.SnapshotTriggerPreRestore {
			undo = &snaps[i]
		}
	}
	if undo == nil || undo.Messages != 4 {
		t.Fatalf("pre-restore undo snapshot missing or wrong: %+v", snaps)
	}
}

// TestPostRestoreKeepsProjectBinding pins acceptance criterion 3: restore
// preserves the registered-project binding (sandbox pinning derives per run).
func TestPostRestoreKeepsProjectBinding(t *testing.T) {
	api, _, svc, _ := newTestChatAPI(t)
	store := svc.Store()

	sess, err := svc.CreateSession("bound")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess.ProjectID = "proj_1"
	sess.ProjectName = "checkout"
	sess.AddMessage("user", "q1", "")
	if err := store.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	name, err := store.SaveSnapshot(sess, hakasesession.SnapshotTriggerPre)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	sess.AddMessage("user", "q2", "")
	if err := store.Save(sess); err != nil {
		t.Fatalf("save 2: %v", err)
	}

	r := newRestoreRouter(api)
	body, _ := json.Marshal(map[string]string{"snapshot": name})
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/restore", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.ProjectID != "proj_1" || loaded.ProjectName != "checkout" {
		t.Fatalf("project binding lost on restore: %+v", loaded)
	}
}

func TestPostRestoreGuards(t *testing.T) {
	api, _, svc, _ := newTestChatAPI(t)
	store := svc.Store()
	sess, err := svc.CreateSession("t")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	r := newRestoreRouter(api)

	t.Run("missing snapshot name", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/restore", bytes.NewReader([]byte(`{}`)))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("unknown session", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"snapshot": "1234567890123456789-pre.json"})
		req := httptest.NewRequest(http.MethodPost, "/sessions/task_missing/restore", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("snapshots list of clean session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sessions/"+sess.ID+"/snapshots", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var resp struct {
			Snapshots []hakasesession.SnapshotInfo `json:"snapshots"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Snapshots == nil || len(resp.Snapshots) != 0 {
			t.Fatalf("expected empty (non-null) list, got %+v", resp.Snapshots)
		}
	})

	t.Run("unknown snapshot", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"snapshot": "1234567890123456789-pre.json"})
		req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/restore", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		// A validated-but-unknown snapshot is "not found", and — by design —
		// nothing was written for it: no undo snapshot churns the ring.
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		snaps, err := store.ListSnapshots(sess.ID)
		if err != nil || len(snaps) != 0 {
			t.Fatalf("failed restore must not write snapshots: %v (%d)", err, len(snaps))
		}
	})
}
