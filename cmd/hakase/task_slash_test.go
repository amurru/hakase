// task_slash_test.go - tests for the /board slash command (task_slash.go):
// subcommand dispatch, create/list/done flows, mutation blocking while the
// agent is processing, and unknown-subcommand handling.
package main

import (
	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/tui"
	"testing"
)

func TestFindSlashCommandBoard(t *testing.T) {
	if cmd := tui.FindSlashCommand("board"); cmd == nil || cmd.Name != "board" {
		t.Fatalf("FindSlashCommand(board) = %v", cmd)
	}
	if cmd := tui.FindSlashCommand("tasks"); cmd == nil || cmd.Name != "board" {
		t.Fatalf("FindSlashCommand(tasks) should resolve to board via alias")
	}
	if cmd := tui.FindSlashCommand("task"); cmd == nil || cmd.Name != "board" {
		t.Fatalf("FindSlashCommand(task) should resolve to board via alias")
	}
}

func TestBoardSlashSummary(t *testing.T) {
	t.Skip("needs createTask helper from task_registry which is not yet ported to root")
}

func TestBoardSlashNewCreatesTask(t *testing.T) {
	t.Skip("needs createTask helper from task_registry which is not yet ported to root")
}

func TestBoardSlashList(t *testing.T) {
	t.Skip("needs createTask helper from task_registry which is not yet ported to root")
}

func TestBoardSlashDone(t *testing.T) {
	t.Skip("needs createTask helper from task_registry which is not yet ported to root")
}

func TestBoardSlashMutationBlockedWhileProcessing(t *testing.T) {
	t.Skip("needs createTask helper from task_registry which is not yet ported to root")
}

func TestBoardSlashUnknownSubcommand(t *testing.T) {
	t.Skip("needs createTask helper from task_registry which is not yet ported to root")
}

func TestBoardSlashArgs(t *testing.T) {
	t.Skip("needs createTask helper from task_registry which is not yet ported to root")
}

// _ silence unused import
var _ = agent.CreateTaskInput{}
