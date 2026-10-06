package session

import (
	"testing"
	"time"
)

func TestPauseRegistry_Lifecycle(t *testing.T) {
	r, err := NewPauseRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewPauseRegistry: %v", err)
	}
	id, err := r.Record(PauseRecord{
		HakaseSessionID: "sess-1",
		ADKSessionID:    "task-1",
		Gate:            PauseGateApproval,
		Summary:         "approval: system_exec rm -rf /tmp/x",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if id == "" {
		t.Fatal("empty pause ID")
	}
	got, err := r.ListForSession("sess-1")
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	if len(got) != 1 || got[0].PauseID != id || got[0].ADKSessionID != "task-1" {
		t.Fatalf("ListForSession = %+v", got)
	}
	if err := r.Unrecord(id); err != nil {
		t.Fatalf("Unrecord: %v", err)
	}
	got, err = r.ListForSession("sess-1")
	if err != nil {
		t.Fatalf("ListForSession after remove: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("records after remove = %+v", got)
	}
	// Double remove is a no-op.
	if err := r.Unrecord(id); err != nil {
		t.Errorf("second Unrecord: %v", err)
	}
	if err := r.Unrecord(""); err != nil {
		t.Errorf("Unrecord empty: %v", err)
	}
}

// TestPauseRegistry_RestartRecovery verifies records persist across
// service instances over the same directory (the crash-restart case).
func TestPauseRegistry_RestartRecovery(t *testing.T) {
	dir := t.TempDir()
	r1, err := NewPauseRegistry(dir)
	if err != nil {
		t.Fatalf("NewPauseRegistry: %v", err)
	}
	if _, err := r1.Record(PauseRecord{
		HakaseSessionID: "sess-9",
		ADKSessionID:    "task-9",
		Gate:            PauseGateClarify,
		Summary:         "which region?",
		Detail:          map[string]any{"question": "which region?"},
		CreatedAt:       time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	r2, err := NewPauseRegistry(dir)
	if err != nil {
		t.Fatalf("NewPauseRegistry (restart): %v", err)
	}
	all, err := r2.List()
	if err != nil {
		t.Fatalf("List after restart: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("records after restart = %d, want 1", len(all))
	}
	rec := all[0]
	if rec.Gate != PauseGateClarify || rec.ADKSessionID != "task-9" {
		t.Errorf("record = %+v", rec)
	}
	if q, _ := rec.Detail["question"].(string); q != "which region?" {
		t.Errorf("detail question = %v", rec.Detail)
	}
}
