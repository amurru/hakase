// mcp.go - the `mcp` subcommand: `hakase mcp serve` runs a stdio MCP server
// exposing hakase's discovered markdown skills as skill:// resources
// (SEP-2640), so any MCP host can list and read them. Config follows the
// normal resolution (project config.json + skill_dirs extra dirs).
//
// With --agent, the same server also exposes hakase RUNS (`run`,
// `list_sessions`, `get_session` tools) via internal/mcp/runserver. The
// agent bootstrap cannot live in this package: it needs the full agent.Deps
// wiring whose factories are main-only (vision resolver, media setup) - the
// web/serve/tui pattern. Package main injects the real handler through
// MCPAgentServeFn at startup.
package cli

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/mcp"
	"context"
	"flag"
	"fmt"
	"os"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPAgentServeFn, when set by package main, serves the agent-flavored MCP
// server (skills + run tools + elicitation gates). It receives the `serve`
// args and returns the process exit code. Unset (tests, or the dispatcher
// used without the main wiring), `mcp serve --agent` is a usage error.
var MCPAgentServeFn func(args []string) int

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
	fmt.Fprint(os.Stderr, `Usage: hakase mcp serve [--agent]

Serve hakase over MCP (stdio transport).
Point your MCP host at: hakase mcp serve

  (default)    skills only: skill:// resources (SEP-2640)
  --agent      also expose hakase runs: the run, list_sessions and
               get_session tools drive the full agent (model config,
               sandbox, approval gates via MCP elicitation)

Resources:
  skill://index.json            index of every exposed skill
  skill://<name>/SKILL.md       a skill's entry point
  skill://<name>/<file>         supporting files of a skill
`)
}

// runMCPServe dispatches between the skills-only server (default) and the
// agent-flavored one (--agent, wired by package main).
func runMCPServe(args []string) int {
	fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentMode := fs.Bool("agent", false, "also expose hakase runs (run/list_sessions/get_session) alongside skills")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "hakase mcp serve: unexpected argument %q\n\n", fs.Arg(0))
		mcpUsage()
		return 2
	}
	if *agentMode {
		if MCPAgentServeFn == nil {
			fmt.Fprintln(os.Stderr,
				"hakase mcp serve --agent is wired by the main binary (package main sets cli.MCPAgentServeFn at startup); "+
					"it is not available in this context")
			return 2
		}
		return MCPAgentServeFn(fs.Args())
	}
	return runMCPServeSkills()
}

// runMCPServeSkills builds the skill resource server from the effective
// config and serves it on stdio until stdin closes.
func runMCPServeSkills() int {
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
