// mcpserver.go - the `hakase mcp serve --agent` bootstrap: the full agent
// wiring (config, sandbox, sessions, registry, runner, gates) mounted as MCP
// run tools next to the skill resources on one stdio server. Lives in
// package main for the same reason as web.go: agent.Deps' factories reach
// main-only helpers (vision resolver, media setup), and internal/cli cannot
// import them back.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/cli"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/knowledge"
	"amurru/hakase/internal/mcp"
	"amurru/hakase/internal/mcp/runserver"
	"amurru/hakase/internal/registry"
	"amurru/hakase/internal/sandbox"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/skill"
	"amurru/hakase/internal/tracing"
	"amurru/hakase/internal/vision"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/tool"
)

// runMCPAgentServe is the real handler behind cli.MCPAgentServeFn (set in
// main.go): boots the agent exactly like the web server does, minus the HTTP
// surfaces, and serves skills + runs over stdio until stdin closes.
func runMCPAgentServe(_ []string) int {
	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp serve --agent: loading config: %v\n", err)
		return 1
	}

	// Tracing (#18): install the OTLP provider before anything runs; no-op
	// unless tracing.enabled. Span export flushes on shutdown.
	shutdownTracing, err := tracing.Install(tracing.Options{
		Enabled:     cfg.Tracing.Enabled,
		Endpoint:    cfg.Tracing.Endpoint,
		Headers:     cfg.Tracing.Headers,
		SampleRatio: config.TracingSampleRatio(cfg),
		Version:     cli.Version,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp serve --agent: %v\n", err)
		return 1
	}
	defer shutdownTracing()

	// Init sandbox before any file or exec operations (same validation as
	// the web bootstrap; landlock mode was already refused at config load).
	sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(cfg.Sandbox)
	if err := sandbox.ValidateSandboxConfig(sandbox.CurrentSandbox); err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp serve --agent: invalid sandbox config: %v\n", err)
		return 1
	}
	if warn := sandbox.SandboxStartupWarning(sandbox.CurrentSandbox); warn != "" {
		fmt.Fprintf(os.Stderr, "hakase mcp serve --agent: WARNING: %s\n", warn)
	}

	home := config.HakaseHome()
	_ = os.MkdirAll(filepath.Join(home, "logs"), 0o755)
	logToFile := func(msg string) { log.Printf("mcp: %s", msg) }

	// Vision hooks (same rationale as web.go: without the config hook,
	// image parts crash the OpenAI-compatible adapter).
	vision.CurrentConfig = func() *config.Config { return cfg }

	// Session service (snapshot ring from config, issue #21).
	var sessionSvc *hakasesession.SessionService
	if store, err := hakasesession.NewSessionStoreWithSnapshotLimit(hakasesession.Dir, config.SessionSnapshotsMax(cfg)); err == nil {
		if svc, err := hakasesession.NewSessionService(store); err == nil {
			svc.SetSnapshotsEnabled(config.SessionSnapshotsEnabled(cfg))
			sessionSvc = svc
		}
	}

	// Project registry (fail-soft, same as web.go: a corrupt file disables
	// project features without blocking the server).
	if st, err := registry.NewStore(registry.DefaultPath()); err == nil {
		registry.Current = registry.NewService(st, interfaces.LogFunc(logToFile))
	} else {
		log.Printf("mcp: project registry unavailable: %v", err)
	}

	// Build agent Deps (same wiring as web.go's runServer).
	deps := &agent.Deps{
		Config:         cfg,
		Log:            interfaces.LogFunc(logToFile),
		SessionService: sessionSvc,

		NewMCPServerManagerFn: func(cfgAny any, logFn interfaces.LogFunc) (any, error) {
			c, ok := cfgAny.(*config.Config)
			if !ok {
				return nil, fmt.Errorf("NewMCPServerManagerFn: config is not *config.Config")
			}
			return mcp.NewMCPServerManager(c, logFn)
		},

		DiscoverMarkdownSkillsFn: func(cwd string, extraDirs []string, logFn interfaces.LogFunc) any {
			return skill.DiscoverMarkdownSkills(cwd, extraDirs, logFn)
		},

		CreateKnowledgeToolsFn: func(logFn interfaces.LogFunc, dir string, expansion bool) ([]tool.Tool, error) {
			return knowledge.CreateKnowledgeTools(knowledge.LogFunc(logFn), dir, expansion)
		},

		CreateCronjobToolFn: func(logFn interfaces.LogFunc) (tool.Tool, error) {
			return cli.CreateCronjobTool(logFn)
		},
		StartCronSchedulerFn: func(logFn interfaces.LogFunc) {
			// Headless cron parity with the web server: scheduled jobs fire
			// only when the deployment opts in.
			if cfg.Channels.EnableCronScheduler {
				cli.StartCronScheduler(logFn)
			}
		},

		BuildQueryExpansionPromptFn: knowledge.BuildQueryExpansionPrompt,
		ParseQueryExpansionsFn:      knowledge.ParseQueryExpansions,

		ResolveVisionProviderFn: resolveVisionProvider,
	}

	// Media generation (zero-config guarantee, same as TUI and web).
	setupMedia(cfg, deps, interfaces.LogFunc(logToFile))

	// Gates: hakase's approvals/clarifications elicit the driving MCP client
	// (fail-closed when it cannot elicit). Remote-MCP elicitation (hakase as
	// client) chains onto the same gates via the mcp package globals.
	gates := runserver.NewGates(
		interfaces.ApprovalConfig{Mode: cfg.Approval.Mode, ExpirySeconds: cfg.Approval.ExpirySeconds},
		interfaces.ClarifyConfig{ExpirySeconds: cfg.Clarify.ExpirySeconds},
	)
	runtime := &agent.Runtime{}
	runtime.SetApprovalGate(gates)
	runtime.SetClarifyGate(gates)
	mcp.SetApprovalGate(gates)
	mcp.SetClarifyGate(gates)

	ctx := context.Background()
	runner, err := agent.SetupRunner(ctx, deps, runtime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp serve --agent: failed to setup agent runner: %v\n", err)
		return 1
	}

	// Model capabilities feed the HistoryBuilder budget math and vision
	// detection (same background fetch as web.go).
	go func() {
		info, err := agent.FetchModelInfo(ctx, cfg)
		if err != nil {
			log.Printf("mcp: model info unavailable: %v", err)
			return
		}
		if deps.HistoryBuilder != nil {
			deps.HistoryBuilder.SetModelInfo(info)
		}
		vision.CurrentModelInfo = func() *interfaces.ModelInfo { return info }
	}()

	// Assemble the server: one stdio connection exposing the skill://
	// resources AND the run tools. Server name "hakase" (the skills-only
	// process keeps "hakase-skills").
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "hakase", Version: cli.Version}, nil)
	mcp.AddSkillResources(srv, cwd, cfg.SkillDirs, logToFile)
	runserver.Mount(srv, runserver.Deps{
		Runner:   agentrun.NewForTransport(runner, sessionSvc, "mcp"),
		Sessions: sessionSvc,
		Gates:    gates,
		Log:      interfaces.LogFunc(logToFile),
	})

	fmt.Fprintln(os.Stderr, "hakase mcp serve --agent: serving skills + runs on stdio")
	if err := srv.Run(ctx, &mcpsdk.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "hakase mcp serve --agent: %v\n", err)
		return 1
	}
	return 0
}
