// task_archive_test.go - tests for the task archive store functions:
// archiving completed tasks, rejecting non-completed tasks, and deleting
// archived tasks.
package main

import (
	hakaseagent "amurru/hakase/internal/agent"
	"os"
	"strings"
	"testing"
)

// chdirTemp changes into a fresh temp dir for the duration of the test so the
// real ./tasks.json in the repo root is never touched.
func chdirTemp(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// markCompleted walks the legal status path (pending -> in_progress -> completed).
func markCompleted(t *testing.T, id string) {
	t.Helper()
	if _, err := hakaseagent.UpdateTask(hakaseagent.UpdateTaskInput{ID: id, Status: hakaseagent.TaskStatusInProgress}); err != nil {
		t.Fatalf("updateTask (in_progress): %v", err)
	}
	completed, err := hakaseagent.UpdateTask(hakaseagent.UpdateTaskInput{ID: id, Status: hakaseagent.TaskStatusCompleted})
	if err != nil {
		t.Fatalf("updateTask (completed): %v", err)
	}
	if completed.Status != hakaseagent.TaskStatusCompleted {
		t.Fatalf("expected completed status, got %s", completed.Status)
	}
}

func TestArchiveCompletedTask(t *testing.T) {
	chdirTemp(t)

	created, err := hakaseagent.CreateTask(hakaseagent.CreateTaskInput{Title: "t"})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}

	markCompleted(t, created.ID)

	archived, err := hakaseagent.ArchiveTask(created.ID)
	if err != nil {
		t.Fatalf("archiveTask: %v", err)
	}
	if archived.Status != hakaseagent.TaskStatusArchived {
		t.Errorf("expected archived status, got %s", archived.Status)
	}

	// The task must still exist (kept for reference) with status archived.
	got, err := hakaseagent.GetTask(created.ID)
	if err != nil {
		t.Fatalf("getTask: %v", err)
	}
	if got == nil {
		t.Fatal("archived task should still exist in the registry")
	}
	if got.Status != hakaseagent.TaskStatusArchived {
		t.Errorf("expected stored status archived, got %s", got.Status)
	}
}

func TestArchiveRejectsNonCompleted(t *testing.T) {
	chdirTemp(t)

	created, err := hakaseagent.CreateTask(hakaseagent.CreateTaskInput{Title: "t"})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	if created.Status != hakaseagent.TaskStatusPending {
		t.Fatalf("expected pending status, got %s", created.Status)
	}

	_, err = hakaseagent.ArchiveTask(created.ID)
	if err == nil {
		t.Fatal("expected error archiving a pending task, got nil")
	}
	if !strings.Contains(err.Error(), "only completed tasks can be archived") {
		t.Errorf("expected 'only completed tasks can be archived' error, got %v", err)
	}

	// The task status must be unchanged.
	got, err := hakaseagent.GetTask(created.ID)
	if err != nil {
		t.Fatalf("getTask: %v", err)
	}
	if got == nil {
		t.Fatal("task should still exist")
	}
	if got.Status != hakaseagent.TaskStatusPending {
		t.Errorf("expected status unchanged (pending), got %s", got.Status)
	}
}

func TestArchiveUnknownTask(t *testing.T) {
	chdirTemp(t)

	_, err := hakaseagent.ArchiveTask("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown task, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

func TestDeleteArchivedTask(t *testing.T) {
	chdirTemp(t)

	created, err := hakaseagent.CreateTask(hakaseagent.CreateTaskInput{Title: "t"})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	markCompleted(t, created.ID)
	if _, err := hakaseagent.ArchiveTask(created.ID); err != nil {
		t.Fatalf("archiveTask: %v", err)
	}

	// Archived tasks can still be deleted via the existing delete path.
	success, err := hakaseagent.DeleteTask(created.ID)
	if err != nil {
		t.Fatalf("deleteTask: %v", err)
	}
	if !success {
		t.Error("deleteTask: expected success for archived task")
	}

	got, err := hakaseagent.GetTask(created.ID)
	if err != nil {
		t.Fatalf("getTask: %v", err)
	}
	if got != nil {
		t.Errorf("expected task to be deleted, got %+v", got)
	}
}

func TestUpdateTaskToArchivedTransition(t *testing.T) {
	chdirTemp(t)

	// Completed tasks may transition to archived.
	created, err := hakaseagent.CreateTask(hakaseagent.CreateTaskInput{Title: "t"})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	markCompleted(t, created.ID)
	updated, err := hakaseagent.UpdateTask(hakaseagent.UpdateTaskInput{ID: created.ID, Status: hakaseagent.TaskStatusArchived})
	if err != nil {
		t.Fatalf("updateTask to archived: %v", err)
	}
	if updated.Status != hakaseagent.TaskStatusArchived {
		t.Errorf("expected archived status, got %s", updated.Status)
	}

	// Pending tasks may NOT transition to archived.
	created2, err := hakaseagent.CreateTask(hakaseagent.CreateTaskInput{Title: "t2"})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	_, err = hakaseagent.UpdateTask(hakaseagent.UpdateTaskInput{ID: created2.ID, Status: hakaseagent.TaskStatusArchived})
	if err == nil {
		t.Error("expected invalid transition error for pending -> archived, got nil")
	}
	if !strings.Contains(err.Error(), "invalid transition") {
		t.Errorf("expected 'invalid transition' error, got %v", err)
	}
}
