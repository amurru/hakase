package main

import (
	"context"
	"log"
	"os"

	tea "charm.land/bubbletea/v2"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "skill" {
		os.Exit(runSkillCLI(os.Args[2:]))
	}

	if len(os.Args) > 1 && os.Args[1] == "task" {
		os.Exit(runTaskCLI(os.Args[2:]))
	}

	ctx := context.Background()

	cfg, err := loadConfig("config.json")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	var p *tea.Program

	// Define thread-safe logger function
	logToUI := func(msg string) {
		if p != nil {
			p.Send(StatusLogMsg{Text: msg})
		}
	}

	// Push task board refreshes to the TUI whenever an agent tool mutates tasks
	taskBoardNotify = func(action string, task TaskMeta) {
		if p != nil {
			p.Send(TaskUpdateMsg{Task: task, Action: action})
		}
	}

	r, err := setupRunner(ctx, cfg, logToUI)
	if err != nil {
		log.Fatalf("Failed to setup agent runner: %v", err)
	}

	m := newModel(ctx, r, cfg.ChatBufferSize, cfg.ShowThinking)
	p = tea.NewProgram(&m)
	m.program = p

	if _, err := p.Run(); err != nil {
		log.Fatalf("Error running program: %v", err)
	}
}
