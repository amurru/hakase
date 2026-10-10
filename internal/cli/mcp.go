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
	"strconv"
	"strings"
	"time"

	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPAgentServeFn, when set by package main, serves the agent-flavored MCP
// server (skills + run tools + elicitation gates). It receives the `serve`
// args and returns the process exit code. Unset (tests, or the dispatcher
// used without the main wiring), `mcp serve --agent` is a usage error.
var MCPAgentServeFn func(args []string) int

// RunMCPCLI implements the mcp subcommand (search, install, list, serve, doctor, audit, logout).
func RunMCPCLI(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		mcpUsage()
		return 2
	}
	switch args[0] {
	case "search":
		return runMCPSearch(args[1:])
	case "install":
		return runMCPInstall(args[1:])
	case "list":
		return runMCPList(args[1:])
	case "serve":
		return runMCPServe(args[1:])
	case "doctor":
		return runMCPDoctor(args[1:])
	case "audit":
		return runMCPAudit(args[1:])
	case "logout":
		return runMCPLogout(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "hakase: unknown mcp subcommand %q\n\n", args[0])
		mcpUsage()
		return 2
	}
}

func runMCPSearch(args []string) int {
	fs := flag.NewFlagSet("mcp search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	limit := fs.Int("limit", 20, "maximum results to return")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	query := strings.Join(fs.Args(), " ")
	client := mcp.NewRegistryClient("")
	servers, err := client.Search(context.Background(), query, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp search: %v\n", err)
		return 1
	}

	if len(servers) == 0 {
		if query != "" {
			fmt.Printf("No MCP servers found matching %q.\n", query)
		} else {
			fmt.Println("No MCP servers found in registry.")
		}
		return 0
	}

	nameW, verW, transW := 4, 7, 10
	for _, s := range servers {
		if len(s.Name) > nameW {
			nameW = len(s.Name)
		}
		v := s.Version
		if v == "" {
			v = "latest"
		}
		if len(v) > verW {
			verW = len(v)
		}
		trans := strings.Join(s.Transports, ",")
		if trans == "" {
			if len(s.Command) > 0 {
				trans = "stdio"
			} else if s.URL != "" {
				trans = "http"
			} else {
				trans = "-"
			}
		}
		if len(trans) > transW {
			transW = len(trans)
		}
	}

	fmt.Printf("%-*s  %-*s  %-*s  %s\n", nameW, "NAME", verW, "VERSION", transW, "TRANSPORTS", "DESCRIPTION")
	for _, s := range servers {
		v := s.Version
		if v == "" {
			v = "latest"
		}
		trans := strings.Join(s.Transports, ",")
		if trans == "" {
			if len(s.Command) > 0 {
				trans = "stdio"
			} else if s.URL != "" {
				trans = "http"
			} else {
				trans = "-"
			}
		}
		fmt.Printf("%-*s  %-*s  %-*s  %s\n", nameW, s.Name, verW, v, transW, trans, truncate(s.Description, 80))
	}
	return 0
}

func runMCPInstall(args []string) int {
	fs := flag.NewFlagSet("mcp install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stdio := fs.Bool("stdio", false, "force stdio transport")
	httpFlag := fs.Bool("http", false, "force http transport")
	pin := fs.String("pin", "", "version pin")
	scope := fs.String("scope", "user", "installation scope (user|project)")
	yes := fs.Bool("yes", false, "skip non-interactive prompts")
	var allowEnv envFlag
	fs.Var(&allowEnv, "allow-env", "environment variables in K=V format (can be specified multiple times)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "hakase mcp install: missing server reference or .mcpb file path")
		return 2
	}

	ref := fs.Arg(0)

	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp install: loading config: %v\n", err)
		return 1
	}

	mgr, err := mcp.NewMCPServerManager(cfg, func(s string) { fmt.Fprintln(os.Stderr, s) })
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp install: building manager: %v\n", err)
		return 1
	}

	opts := mcp.InstallOptions{
		Ref:      ref,
		Stdio:    *stdio,
		HTTP:     *httpFlag,
		Pin:      *pin,
		Scope:    *scope,
		AllowEnv: allowEnv,
		Yes:      *yes,
	}

	installedCfg, err := mcp.InstallServer(context.Background(), mgr, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp install: %v\n", err)
		return 1
	}

	fmt.Printf("Successfully installed MCP server %q (scope: %s)\n", config.SanitizeMCPServerName(ref), *scope)
	if installedCfg.URL != "" {
		fmt.Printf("  Transport: http (%s)\n", installedCfg.URL)
	} else if len(installedCfg.Command) > 0 {
		fmt.Printf("  Transport: stdio (%s)\n", strings.Join(installedCfg.Command, " "))
	}

	return 0
}

func runMCPList(args []string) int {
	fs := flag.NewFlagSet("mcp list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp list: loading config: %v\n", err)
		return 1
	}

	mgr, err := mcp.NewMCPServerManager(cfg, func(s string) { fmt.Fprintln(os.Stderr, s) })
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp list: building manager: %v\n", err)
		return 1
	}

	servers := mgr.ListServers()
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured.")
		return 0
	}

	nameW, typeW, statusW := 4, 4, 6
	for _, s := range servers {
		if len(s.Name) > nameW {
			nameW = len(s.Name)
		}
		if len(s.Type) > typeW {
			typeW = len(s.Type)
		}
		if len(s.Status) > statusW {
			statusW = len(s.Status)
		}
	}

	fmt.Printf("%-*s  %-*s  %-*s  %8s  %s\n", nameW, "NAME", typeW, "TYPE", statusW, "STATUS", "TOOLS", "ENDPOINT")
	for _, s := range servers {
		toolsStr := strconv.Itoa(s.ToolCount)
		if s.Disabled {
			toolsStr = "-"
		}
		fmt.Printf("%-*s  %-*s  %-*s  %8s  %s\n", nameW, s.Name, typeW, s.Type, statusW, s.Status, toolsStr, s.Transport)
	}
	return 0
}

func runMCPAudit(args []string) int {
	fs := flag.NewFlagSet("mcp audit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp audit: loading config: %v\n", err)
		return 1
	}

	mgr, err := mcp.NewMCPServerManager(cfg, func(s string) { fmt.Fprintln(os.Stderr, s) })
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp audit: building manager: %v\n", err)
		return 1
	}

	auditor := mcp.NewAuditor(mgr, cfg)
	res, err := auditor.Audit(mcpDoctorCtx{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp audit: %v\n", err)
		return 1
	}

	fmt.Println("MCP Gateway Audit Report")
	fmt.Println("========================")
	for _, check := range res.Checks {
		fmt.Printf("[%s] %s: %s\n", check.Status, check.Name, check.Summary)
		for _, detail := range check.Details {
			fmt.Printf("  - %s\n", detail)
		}
	}

	if res.Failed {
		fmt.Println("\nAudit Status: FAIL")
		return 1
	}

	fmt.Println("\nAudit Status: PASS")
	return 0
}

func runMCPLogout(args []string) int {
	fs := flag.NewFlagSet("mcp logout", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "hakase mcp logout: missing server name")
		return 2
	}

	serverName := fs.Arg(0)
	if err := mcp.RevokeToken(serverName); err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp logout: %v\n", err)
		return 1
	}

	fmt.Printf("Successfully logged out and revoked tokens for server %q\n", serverName)
	return 0
}

type envFlag map[string]string

func (e *envFlag) String() string {
	var parts []string
	for k, v := range *e {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ", ")
}

func (e *envFlag) Set(value string) error {
	if *e == nil {
		*e = make(map[string]string)
	}
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid environment variable format %q (expected K=V)", value)
	}
	(*e)[parts[0]] = parts[1]
	return nil
}

// runMCPDoctor measures every configured MCP server: how many tools it
// exposes, and how long the dial takes. The dial time is the number that
// matters - Tools() runs before every model call, so an unreachable server is
// charged to the first turn of every session.
func runMCPDoctor(args []string) int {
	fs := flag.NewFlagSet("mcp doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: loading config: %v\n", err)
		return 1
	}
	mgr, err := mcp.NewMCPServerManager(cfg, func(s string) { fmt.Fprintln(os.Stderr, s) })
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: building mcp manager: %v\n", err)
		return 1
	}

	diags := mgr.Diagnose(mcpDoctorCtx{})

	if len(diags) == 0 {
		fmt.Println("No MCP servers configured. Add them under \"mcp\": {\"servers\": {...}} in config.json")
		return 0
	}

	nameW, kindW, dialW := 4, 8, 6
	for _, d := range diags {
		if n := len(d.Name); n > nameW {
			nameW = n
		}
		if k := len(d.StatusWord()); k > kindW {
			kindW = k
		}
		if s := len(d.Dial.Round(time.Millisecond).String()); s > dialW {
			dialW = s
		}
	}

	fmt.Printf("%-*s  %-*s  %8s  %6s  %s\n", nameW, "SERVER", kindW, "STATUS", "TOOLS", "DIAL", "ENDPOINT")
	var unreachable []string
	for _, d := range diags {
		tools := "-"
		if !d.Disabled && d.ToolCount > 0 {
			tools = strconv.Itoa(d.ToolCount)
		}
		dial := "-"
		if !d.Disabled && d.ToolCount >= 0 && d.Dial > 0 {
			dial = d.Dial.Round(time.Millisecond).String()
		}
		fmt.Printf("%-*s  %-*s  %8s  %6s  %s\n", nameW, d.Name, kindW, d.StatusWord(), tools, dial, d.Transport)
		if !d.OK && !d.Disabled {
			unreachable = append(unreachable, d.Name)
		}
	}

	if len(unreachable) > 0 {
		fmt.Printf("\n%d of %d enabled servers unreachable: %s\n", len(unreachable), len(diags), strings.Join(unreachable, ", "))
		fmt.Println("Tools() runs before every model call, so each unreachable server is charged to the")
		fmt.Println("first turn of every session (up to its list budget), then skipped by the failure")
		fmt.Println("cooldown. Fix or remove them in config.json / ~/.hakase/mcp.json.")
		for _, d := range diags {
			if !d.OK && !d.Disabled && d.Error != "" {
				fmt.Printf("  %s: %s\n", d.Name, truncate(d.Error, 160))
			}
		}
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// mcpDoctorCtx is a minimal ReadonlyContext for a command that never runs an
// agent: Diagnose only needs somewhere to hang a deadline and the ADK
// accessors the MCP client reads.
type mcpDoctorCtx struct {
	context.Context
}

func (mcpDoctorCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (mcpDoctorCtx) Done() <-chan struct{}       { return nil }
func (mcpDoctorCtx) Err() error                  { return nil }
func (mcpDoctorCtx) Value(any) any               { return nil }

func (mcpDoctorCtx) UserContent() *genai.Content          { return nil }
func (mcpDoctorCtx) InvocationID() string                 { return "mcp-doctor" }
func (mcpDoctorCtx) AgentName() string                    { return "mcp-doctor" }
func (mcpDoctorCtx) ReadonlyState() session.ReadonlyState { return nil }
func (mcpDoctorCtx) UserID() string                       { return "" }
func (mcpDoctorCtx) AppName() string                      { return "hakase" }
func (mcpDoctorCtx) SessionID() string                    { return "" }
func (mcpDoctorCtx) Branch() string                       { return "" }

func mcpUsage() {
	fmt.Fprint(os.Stderr, `Usage: hakase mcp search <query> [--limit 20]
       hakase mcp install <ref> [--stdio|--http] [--pin ver] [--scope user|project]
       hakase mcp list
       hakase mcp serve [--agent]
       hakase mcp doctor
       hakase mcp audit
       hakase mcp logout <server>

  search  Search the official MCP server registry.
          Example: hakase mcp search github --limit 10

  install Install an MCP server from registry or local .mcpb file.
          Example: hakase mcp install github --allow-env GITHUB_TOKEN=xyz

  list    List all configured MCP servers and their statuses.

  serve   Serve hakase over MCP (stdio transport).
          Point your MCP host at: hakase mcp serve

            (default)    skills only: skill:// resources (SEP-2640)
            --agent      also expose hakase runs: the run, list_sessions and
                         get_session tools drive the full agent (model config,
                         sandbox, approval gates via MCP elicitation)

  doctor  Measure every configured MCP server: tool count and dial time.
          Tools() runs before every model call, so an unreachable server is
          charged to the first turn of every session. Use this to find one.

  audit   Perform a security and configuration audit across configured MCP servers.

  logout  Revoke stored OAuth tokens for a specific MCP server.

Resources (serve):
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
