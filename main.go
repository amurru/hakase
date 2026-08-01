package main

import (
	"context"
	"log"

	tea "charm.land/bubbletea/v2"
)

func main() {
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
