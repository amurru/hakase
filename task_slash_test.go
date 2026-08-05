// task_slash_test.go - tests for the /board slash command (task_slash.go):
// subcommand dispatch, create/list/done flows, mutation blocking while the
// agent is processing, and unknown-subcommand handling.
package main

import (
	"strings"
	"testing"
)

func TestFindSlashCommandBoard(t *testing.T) {
	if cmd := findSlashCommand("board"); cmd == nil || cmd.Name != "board" {
		t.Fatalf("findSlashCommand(board) = %v", cmd)
	}
	if cmd := findSlashCommand("tasks"); cmd == nil || cmd.Name != "board" {
		t.Fatalf("findSlashCommand(tasks) should resolve to board via alias")
	}
	if cmd := findSlashCommand("task"); cmd == nil || cmd.Name != "board" {
		t.Fatalf("findSlashCommand(task) should resolve to board via alias")
	}
}

func TestBoardSlashSummary(t *testing.T) {
	chdirTemp(t)

	if _, err := createTask(CreateTaskInput{Title: "one"}); err != nil {
		t.Fatalf("createTask: %v", err)
	}
	if _, err := createTask(CreateTaskInput{Title: "two"}); err != nil {
		t.Fatalf("createTask: %v", err)
	}

	m := newModelWithSvc(t)
	model, cmd := m.sendInput("/board")
	mm := model
	if cmd != nil {
		t.Fatalf("/board returned unexpected cmd %v", cmd)
	}
	joined := strings.Join(mm.logLines, "\n")
	if !strings.Contains(joined, "📋 Task Board") {
		t.Fatalf("/board summary must log the task board header, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Total: 2") {
		t.Fatalf("/board summary must log Total: 2, got:\n%s", joined)
	}
}

func TestBoardSlashNewCreatesTask(t *testing.T) {
	chdirTemp(t)

	m := newModelWithSvc(t)
	model, cmd := m.sendInput("/board new write tests --priority high")
	mm := model
	if cmd != nil {
		t.Fatalf("/board new returned unexpected cmd %v", cmd)
	}
	joined := strings.Join(mm.logLines, "\n")
	if !strings.Contains(joined, "Created task") {
		t.Fatalf("/board new must log creation, got:\n%s", joined)
	}

	tasks, err := listTasks(ListTasksInput{})
	if err != nil {
		t.Fatalf("listTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Title != "write tests" {
		t.Errorf("expected title %q, got %q", "write tests", tasks[0].Title)
	}
	if tasks[0].Priority != TaskPriorityHigh {
		t.Errorf("expected priority high, got %s", tasks[0].Priority)
	}
}

func TestBoardSlashListShowsTasks(t *testing.T) {
	chdirTemp(t)

	if _, err := createTask(CreateTaskInput{Title: "visible task"}); err != nil {
		t.Fatalf("createTask: %v", err)
	}

	m := newModelWithSvc(t)
	model, cmd := m.sendInput("/board list")
	mm := model
	if cmd != nil {
		t.Fatalf("/board list returned unexpected cmd %v", cmd)
	}
	joined := strings.Join(mm.logLines, "\n")
	if !strings.Contains(joined, "visible task") {
		t.Fatalf("/board list must show the task title, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Tasks (1):") {
		t.Fatalf("/board list must log task count, got:\n%s", joined)
	}
}

func TestBoardSlashDoneCompletesTask(t *testing.T) {
	chdirTemp(t)

	created, err := createTask(CreateTaskInput{Title: "finish me"})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}

	m := newModelWithSvc(t)
	model, cmd := m.sendInput("/board done " + created.ID)
	mm := model
	if cmd != nil {
		t.Fatalf("/board done returned unexpected cmd %v", cmd)
	}
	joined := strings.Join(mm.logLines, "\n")
	if !strings.Contains(joined, "Completed task") {
		t.Fatalf("/board done must log completion, got:\n%s", joined)
	}

	got, err := getTask(created.ID)
	if err != nil {
		t.Fatalf("getTask: %v", err)
	}
	if got == nil {
		t.Fatal("task should still exist")
	}
	if got.Status != TaskStatusCompleted {
		t.Errorf("expected completed status, got %s", got.Status)
	}
}

func TestBoardSlashUnknownSubcommand(t *testing.T) {
	chdirTemp(t)

	m := newModelWithSvc(t)
	model, cmd := m.sendInput("/board bogus")
	mm := model
	if cmd != nil {
		t.Fatalf("/board bogus must not return a command, got %v", cmd)
	}
	found := false
	for _, l := range mm.logLines {
		if strings.Contains(l, "unknown /board subcommand") {
			found = true
		}
	}
	if !found {
		t.Fatalf("/board bogus must log an unknown-subcommand hint, got %v", mm.logLines)
	}
}

func TestBoardSlashMutationBlockedWhileProcessing(t *testing.T) {
	chdirTemp(t)

	created, err := createTask(CreateTaskInput{Title: "keep me"})
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}

	m := newModelWithSvc(t)
	m.isProcessing = true
	model, cmd := m.sendInput("/board delete " + created.ID)
	mm := model
	if cmd != nil {
		t.Fatalf("/board delete while processing must not run, got cmd %v", cmd)
	}

	got, err := getTask(created.ID)
	if err != nil {
		t.Fatalf("getTask: %v", err)
	}
	if got == nil {
		t.Fatal("task must still exist - mutation was blocked")
	}

	found := false
	for _, l := range mm.logLines {
		if strings.Contains(l, "while the agent is working") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blocked /board delete must log a warning, got %v", mm.logLines)
	}
}

func TestBoardSlashReadAllowedWhileProcessing(t *testing.T) {
	chdirTemp(t)

	if _, err := createTask(CreateTaskInput{Title: "readable"}); err != nil {
		t.Fatalf("createTask: %v", err)
	}

	m := newModelWithSvc(t)
	m.isProcessing = true
	model, cmd := m.sendInput("/board list")
	mm := model
	if cmd != nil {
		t.Fatalf("/board list while processing returned unexpected cmd %v", cmd)
	}
	joined := strings.Join(mm.logLines, "\n")
	if !strings.Contains(joined, "readable") {
		t.Fatalf("/board list must still work while processing, got:\n%s", joined)
	}
}
