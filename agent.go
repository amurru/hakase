package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/genai"
)

func setupRunner(ctx context.Context, cfg *Config) (*runner.Runner, error) {
	model, err := gemini.NewModel(ctx, cfg.ModelName, &genai.ClientConfig{
		APIKey: cfg.APIKey,
	})
	if err != nil {
		return nil, err
	}

	mcpToolset, err := mcptoolset.New(mcptoolset.Config{
		Transport: &mcp.StreamableClientTransport{
			Endpoint: cfg.MCPServerURL,
		},
	})
	if err != nil {
		return nil, err
	}

	rootAgent, err := llmagent.New(llmagent.Config{
		Name:        "hermes_core",
		Description: "Main orchestrator agent handling web tasks via Lightpanda",
		Model:       model,
		Instruction: cfg.Instruction,
		Toolsets:    []tool.Toolset{mcpToolset},
	})
	if err != nil {
		return nil, err
	}

	return runner.New(runner.Config{
		AppName:           "hermes_harness",
		Agent:             rootAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
}
