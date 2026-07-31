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

	r, err := setupRunner(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to setup agent runner: %v", err)
	}

	m := newModel(ctx, r)
	p := tea.NewProgram(&m)
	m.program = p

	if _, err := p.Run(); err != nil {
		log.Fatalf("Error running program: %v", err)
	}
}
