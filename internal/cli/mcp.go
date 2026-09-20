// mcp.go - the `mcp` subcommand: `hakase mcp serve` runs a stdio MCP server
// exposing hakase's discovered markdown skills as skill:// resources
// (SEP-2640), so any MCP host can list and read them. Config follows the
// normal resolution (project config.json + skill_dirs extra dirs).
package cli

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/mcp"
	"context"
	"fmt"
	"os"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunMCPCLI implements the mcp subcommand (serve).
func RunMCPCLI(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		mcpUsage()
		return 2
	}
	switch args[0] {
	case "serve":
		return runMCPServe(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "hakase: unknown mcp subcommand %q\n\n", args[0])
		mcpUsage()
		return 2
	}
}

func mcpUsage() {
	fmt.Fprint(os.Stderr, `Usage: hakase mcp serve

Serve hakase's markdown skills over MCP (stdio transport).
Point your MCP host at: hakase mcp serve

Resources:
  skill://index.json            index of every exposed skill
  skill://<name>/SKILL.md       a skill's entry point
  skill://<name>/<file>         supporting files of a skill
`)
}

// runMCPServe builds the skill resource server from the effective config and
// serves it on stdio until stdin closes.
func runMCPServe(_ []string) int {
	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp serve: loading config: %v\n", err)
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	log := func(msg string) { fmt.Fprintln(os.Stderr, msg) }
	srv := mcp.NewSkillsServer(cwd, cfg.SkillDirs, log, "dev")
	if err := srv.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp serve: %v\n", err)
		return 1
	}
	return 0
}
