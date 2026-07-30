package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/genai"
)

type Config struct {
	Provider     string `json:"provider"`
	ModelName    string `json:"model_name"`
	APIKey       string `json:"api_key"`
	Instruction  string `json:"instruction"`
	MCPServerURL string `json:"mcp_server_url"`
}

type WebSearchArgs struct {
	Query string `json:"query" jsonschema:"The search term to look up online"`
}

type WebSearchResult struct {
	Summary string `json:"summary"`
}

// Change context.Context to tool.Context
func executeWebSearch(ctx tool.Context, args WebSearchArgs) (WebSearchResult, error) {
	fmt.Println("---> [TOOL EXECUTED] Searching for:", args.Query)
	return WebSearchResult{
		Summary: fmt.Sprintf("Found mock search results for: %s", args.Query),
	}, nil
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

	// tool wrappers

	// searchTool, err := functiontool.New[WebSearchArgs, WebSearchResult](
	// 	functiontool.Config{
	// 		Name:        "web_search",
	// 		Description: "Searches the web for given query terms to find current information.",
	// 	},
	// 	executeWebSearch,
	// )
	// if err != nil {
	// 	log.Fatalf("Failed to create search tool: %v", err)
	// }

	mcpToolset, err := mcptoolset.New(mcptoolset.Config{
		Transport: &mcp.StreamableClientTransport{
			Endpoint: cfg.MCPServerURL,
		},
	})
	if err != nil {
		log.Fatalf("Failed to initialize MCP toolset: %v", err)
	}

	rootAgent, err := llmagent.New(llmagent.Config{
		Name:        "hermes_core",
		Description: "Main orchestrator agent handling tasks and external tool execution",
		Model:       model,
		Instruction: cfg.Instruction,
		Toolsets:    []tool.Toolset{mcpToolset},
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
	prompt := "Go to https://amurru.dev/en/blog, extract the main content using markdown, and list the top 3 headline titles."
	msg := genai.NewContentFromText(prompt, genai.RoleUser)

	// 3. Run execution stream
	fmt.Println("🤖 Agent Output:")
	for ev, err := range r.Run(ctx, "user-1", "session-1", msg, agent.RunConfig{}) {
		if err != nil {
			log.Fatalf("Run error: %v", err)
		}
		if ev != nil && ev.Content != nil {
			for _, part := range ev.Content.Parts {
				// Printed text output from the model
				if part.Text != "" {
					fmt.Print(part.Text)
				}
				// log when the model requests a tool call
				if part.FunctionCall != nil {
					fmt.Printf(
						"\n [TOOL CALL]: Function '%s' invoked with args: %v\n",
						part.FunctionCall.Name,
						part.FunctionCall.Args,
					)
				}
				// log when a tool completes and returns output
				if part.FunctionResponse != nil {
					fmt.Printf("📥 [TOOL RESPONSE]: Function '%s'\n", part.FunctionResponse.Name)
				}
			}
		}
	}
	fmt.Println()
}
