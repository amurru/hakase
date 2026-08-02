package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "skill" {
		os.Exit(runSkillCLI(os.Args[2:]))
	}

	if len(os.Args) > 1 && os.Args[1] == "task" {
		os.Exit(runTaskCLI(os.Args[2:]))
	}

	if len(os.Args) > 1 && os.Args[1] == "session" {
		os.Exit(runSessionCLI(os.Args[2:]))
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

	m := newModel(ctx, r, cfg.ChatBufferSize, cfg.ShowThinking, cfg.ModelName, cfg.ThinkingLevel)
	p = tea.NewProgram(&m)
	m.program = p

	// Run stale session cleanup on startup.
	go func() {
		if m.sessionService != nil {
			removed, err := m.sessionService.CleanupStale(30 * 24 * time.Hour)
			if err == nil && removed > 0 {
				logToUI(fmt.Sprintf("🧹 Cleaned up %d stale session(s)", removed))
			}
		}
	}()

	// Run periodic stale session cleanup every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if m.sessionService != nil {
				removed, err := m.sessionService.CleanupStale(30 * 24 * time.Hour)
				if err == nil && removed > 0 {
					logToUI(fmt.Sprintf("🧹 Cleaned up %d stale session(s)", removed))
				}
			}
		}
	}()

	// Fetch model capabilities (context window, thinking support) in the
	// background so the status bar can show them once available.
	go func() {
		info, err := FetchModelInfo(ctx, cfg)
		if err != nil {
			logToUI(fmt.Sprintf("⚠️ model info unavailable: %v", err))
			return
		}
		if p != nil {
			p.Send(ModelInfoMsg{Info: info})
		}
	}()

	if _, err := p.Run(); err != nil {
		log.Fatalf("Error running program: %v", err)
	}
}
