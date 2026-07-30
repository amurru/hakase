package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type Config struct {
	Provider    string `json:"provider"`
	ModelName   string `json:"model_name"`
	APIKey      string `json:"api_key"`
	Instruction string `json:"instruction"`
}

func loadConfig(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func main() {
	ctx := context.Background()

	cfg, err := loadConfig("config.json")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	model, err := gemini.NewModel(ctx, cfg.ModelName, &genai.ClientConfig{
		APIKey: cfg.APIKey,
	})
	if err != nil {
		log.Fatalf("Failed to initialize model: %v", err)
	}

	rootAgent, err := llmagent.New(llmagent.Config{
		Name:        "hermes_core",
		Description: "Main orchestrator agent handling tasks and external tool execution",
		Model:       model,
		Instruction: cfg.Instruction,
	})
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// 1. Create Runner with In-Memory Session storage
	r, err := runner.New(runner.Config{
		AppName:           "hermes_harness",
		Agent:             rootAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		log.Fatalf("Failed to create runner: %v", err)
	}

	// 2. Prepare user input
	prompt := "Hello! Introduce yourself and state your purpose."
	msg := genai.NewContentFromText(prompt, genai.RoleUser)

	// 3. Run execution stream
	fmt.Println("🤖 Agent Output:")
	for ev, err := range r.Run(ctx, "user-1", "session-1", msg, agent.RunConfig{}) {
		if err != nil {
			log.Fatalf("Run error: %v", err)
		}
		if ev != nil && ev.Content != nil {
			for _, part := range ev.Content.Parts {
				if part.Text != "" {
					fmt.Print(part.Text)
				}
			}
		}
	}
	fmt.Println()
}
