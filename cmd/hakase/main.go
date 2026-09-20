// cmd/hakase is the entry point for the hakase binary, combining CLI dispatch
// (via internal/cli) and the interactive terminal UI (via internal/tui). Full
// dependency injection wires agent.Deps with all bridge factories, gates, and
// notifiers previously provided by root globals.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/cli"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/herdr"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/knowledge"
	"amurru/hakase/internal/mcp"
	"amurru/hakase/internal/sandbox"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/skill"
	"amurru/hakase/internal/tui"
	"amurru/hakase/internal/util"
	"amurru/hakase/internal/vision"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/adk/v2/tool"
)

func main() {
	// Wire the real web/serve/TUI handlers into the dispatcher. They live in
	// package main (not internal/cli) because their dependencies import
	// internal/cli back (internal/web/handlers -> internal/cli): registering
	// from here keeps every subcommand on the uniform Dispatch path with no
	// import cycle.
	cli.RegisterCommand("web", "serve the web UI", runWeb)
	cli.RegisterCommand("serve", "run the API-only server", runServe)
	cli.RegisterCommand("tui", "launch the interactive terminal UI", runTUICommand)

	// No subcommand falls through to the TUI handler via Dispatch, like
	// every other subcommand.
	os.Exit(cli.Dispatch(os.Args[1:]))
}

// runTUICommand adapts runTUI to the dispatcher handler shape: launching the
// interactive terminal UI, for both bare `hakase` and `hakase tui`.
func runTUICommand(args []string) int {
	runTUI()
	return 0
}

func runTUI() {
	ctx := context.Background()

	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Init sandbox before any file or exec operations.
	// LoadConfig already refused landlock mode (issue #14); re-validate here
	// defensively so a directly-constructed config can never slip through.
	sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(cfg.Sandbox)
	if err := sandbox.ValidateSandboxConfig(sandbox.CurrentSandbox); err != nil {
		log.Fatalf("Invalid sandbox config: %v", err)
	}

	// Vision hooks: feed the live config to the vision package so the
	// BeforeModel callback can rewrite user-attached image parts before the
	// OpenAI-compatible adapter rejects InlineData. init.go seeds nil; this
	// is the documented override point. Config is load-once per process.
	vision.CurrentConfig = func() *config.Config { return cfg }

	var program *tea.Program

	// Dev-mode structured JSON logging.
	if logPath := util.InitDebugLogging(cfg.Debug); logPath != "" {
		util.DebugEvent("startup", "log_file", logPath, "debug", true)
	}
	defer util.CloseDebugLogging()

	// Thread-safe log function that routes debug events to both the file
	// logger (via util.DebugEvent) and the TUI status bar (via program.Send).
	logToUI := func(msg string) {
		util.DebugEvent("status_log", "msg", msg)
		if program != nil {
			program.Send(tui.StatusLogMsg{Text: msg})
		}
	}

	// Issue #14: surface sandbox degradation visibly at startup (not just
	// debug logs) and per fallback exec. The audit trail is the normative
	// record; this hook is the TUI-visible half.
	if warn := sandbox.SandboxStartupWarning(sandbox.CurrentSandbox); warn != "" {
		log.Printf("WARNING: %s", warn)
		logToUI("WARNING: " + warn)
	}
	sandbox.SandboxNoticeFunc = func(sessionID, msg string) {
		if sessionID != "" {
			logToUI(fmt.Sprintf("WARNING [session %s]: %s", sessionID, msg))
		} else {
			logToUI("WARNING: " + msg)
		}
	}

	// Create the session service up front so the same instance backs both the
	// TUI (persistence) and the runner's HistoryBuilder (history injection).
	var sessionSvc *hakasesession.SessionService
	if store, err := hakasesession.NewSessionStore(hakasesession.Dir); err == nil {
		if svc, err := hakasesession.NewSessionService(store); err == nil {
			sessionSvc = svc
		}
	}

	// Build Deps with all bridge factories that the agent package needs
	// during SetupRunner. These replace the old root-level global functions.
	deps := &agent.Deps{
		Config:         cfg,
		Log:            interfaces.LogFunc(logToUI),
		SessionService: sessionSvc,

		// MCP server manager factory: discovers, validates, and connects to
		// every configured MCP server (project config + user registry).
		NewMCPServerManagerFn: func(cfgAny any, logFn interfaces.LogFunc) (any, error) {
			c, ok := cfgAny.(*config.Config)
			if !ok {
				return nil, fmt.Errorf("NewMCPServerManagerFn: config is not *config.Config")
			}
			mgr, err := mcp.NewMCPServerManager(c, logFn)
			if err != nil {
				return nil, err
			}
			// Share the live manager through the package global so the TUI
			// (/mcp), slash commands, and other consumers can reach it.
			mcp.MCPManager = mgr
			return mgr, nil
		},

		// Markdown skill discovery: walks project directories and user skill
		// dirs for SKILL.md files.
		DiscoverMarkdownSkillsFn: func(cwd string, extraDirs []string, logFn interfaces.LogFunc) any {
			return skill.DiscoverMarkdownSkills(cwd, extraDirs, logFn)
		},

		// Knowledge base tools: save/recall/search/update/link/cite/list/lint.
		CreateKnowledgeToolsFn: func(logFn interfaces.LogFunc, dir string, expansion bool) ([]tool.Tool, error) {
			return knowledge.CreateKnowledgeTools(knowledge.LogFunc(logFn), dir, expansion)
		},

		// Cron job tool: schedule one-shot or recurring agent tasks.
		CreateCronjobToolFn: func(logFn interfaces.LogFunc) (tool.Tool, error) {
			return cli.CreateCronjobTool(logFn)
		},

		// Background cron scheduler: ticks every 30s, fires due jobs.
		StartCronSchedulerFn: func(logFn interfaces.LogFunc) {
			cli.StartCronScheduler(logFn)
		},

		// HyDE-lite query expansion for search_knowledge.
		BuildQueryExpansionPromptFn: knowledge.BuildQueryExpansionPrompt,
		ParseQueryExpansionsFn:      knowledge.ParseQueryExpansions,

		// Vision model provider selection: config-driven (explicit provider,
		// base_url heuristic, or reuse primary).
		ResolveVisionProviderFn: resolveVisionProvider,
	}

	// Media generation: construct registry unconditionally (zero-config guarantee).
	setupMedia(cfg, deps, interfaces.LogFunc(logToUI))

	// Runtime holds interactive gates that are wired AFTER the tea.Program
	// exists (they need to Send tea.Msg into the TUI event loop).
	runtime := &agent.Runtime{}

	// SetupRunner builds all ADK agents, tools, context blocks, and the
	// ADK runner. Returns a *runner.Runner ready to serve agent runs.
	r, err := agent.SetupRunner(ctx, deps, runtime)
	if err != nil {
		log.Fatalf("Failed to setup agent runner: %v", err)
	}

	// NOTE (plan SL-001, audit B5): the skill-evolver mutator bridge
	// (skill.EvolveMutateFn) and the hctx.CurrentModelFunc hook are owned
	// by agent.SetupRunner above (it already imports internal/skill), so
	// live TUI runs mutate correctly with no per-entrypoint wiring.

	// Build the TUI model and the tea.Program.
	m := tui.NewModel(ctx, r, sessionSvc, cfg.ChatBufferSize, cfg.ShowThinking, cfg.ModelName, cfg.ThinkingLevel)
	program = tea.NewProgram(&m)
	m.SetProgram(program)

	// Install the Herdr lifecycle reporter when hakase runs inside a Herdr
	// pane. Releases authority on exit so Herdr stops tracking this agent.
	if rep := herdr.NewReporter(); rep != nil {
		m.SetHerdrReporter(rep)
	}
	defer m.HerdrRelease()

	// Wire TUI hook variables for slash command handlers that live in this
	// package (formerly in root). These are accessed by internal/tui/slash.go.
	tui.CurrentHistoryBuilder = deps.HistoryBuilder
	tui.RunBoardCommand = runBoardCommand
	tui.RunMCPCommand = runMCPCommand
	tui.RunSidekickCommand = func(m *tui.AppModel, args string) tea.Cmd {
		return runSidekickCommand(m, args, runtime)
	}
	tui.SidekickBeginRun = func() {
		if sk := runtime.Sidekick(); sk != nil {
			sk.BeginRun()
		}
	}
	tui.SidekickEndRun = func() {
		if sk := runtime.Sidekick(); sk != nil {
			sk.EndRun()
		}
	}

	// Fatal shutdown path: release Herdr authority immediately before exiting.
	// The deferred release handles normal exits, but log.Fatalf bypasses defer.
	defer func() {
		if r := recover(); r != nil {
			m.HerdrRelease()
			log.Fatalf("fatal error: %v", r)
		}
	}()

	// Set approval and clarify configs on the TUI package so the gate
	// implementations (gates.go) can read expiry and mode at runtime.
	tui.SetApprovalConfig(interfaces.ApprovalConfig{
		Mode:          cfg.Approval.Mode,
		ExpirySeconds: cfg.Approval.ExpirySeconds,
	})
	tui.SetClarifyConfig(interfaces.ClarifyConfig{
		ExpirySeconds: cfg.Clarify.ExpirySeconds,
	})

	// Install interactive gates into the agent Runtime. The TUI model
	// implements interfaces.ApprovalGate, interfaces.ClarifyGate, and
	// interfaces.EventNotifier (see internal/tui/gates.go).
	runtime.SetApprovalGate(&m)
	runtime.SetClarifyGate(&m)
	runtime.SetEventNotifier(&m)

	// Wire the event notifier into the sidekick after the TUI model
	// exists. The sidekick is created during SetupRunner before the
	// tea.Program (and thus before the TUI model), so SetNotifier
	// must be called here to avoid nil-notifier drops on sidekick
	// events.
	if sk := runtime.Sidekick(); sk != nil {
		sk.SetNotifier(&m)
	}

	// Share the TUI's mid-run message queue with the HistoryBuilder so
	// prompts typed while the agent is busy are steered into the running
	// session at model-call boundaries (BeforeModelCallback).
	if deps.HistoryBuilder != nil {
		deps.HistoryBuilder.SetPendingQueue(m.PendingQueue())
	}

	// Wire cron job lifecycle events to the TUI. This MUST happen before
	// the cron scheduler (started in SetupRunner) fires any jobs.
	cli.CronJobNotify = func(status, jobID, name, summary, outputPath string) {
		if program != nil {
			program.Send(tui.CronJobMsg{
				JobID:      jobID,
				Name:       name,
				Status:     status,
				Summary:    summary,
				OutputPath: outputPath,
			})
		}
	}

	// Stale session cleanup: run once at startup, then every 5 minutes.
	go func() {
		if sessionSvc != nil {
			removed, err := sessionSvc.CleanupStale(30 * 24 * time.Hour)
			if err == nil && removed > 0 {
				logToUI(fmt.Sprintf("Cleaned up %d stale session(s)", removed))
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if sessionSvc != nil {
				removed, err := sessionSvc.CleanupStale(30 * 24 * time.Hour)
				if err == nil && removed > 0 {
					logToUI(fmt.Sprintf("Cleaned up %d stale session(s)", removed))
				}
			}
		}
	}()

	// Fetch model capabilities (context window, thinking support) in the
	// background so the status bar can show them once available.
	go func() {
		info, err := agent.FetchModelInfo(ctx, cfg)
		if err != nil {
			logToUI(fmt.Sprintf("Model info unavailable: %v", err))
			return
		}
		// Feed the model capabilities to the HistoryBuilder for budget math.
		if deps.HistoryBuilder != nil {
			deps.HistoryBuilder.SetModelInfo(info)
		}
		// Install the model info provider for vision and status bar use.
		vision.CurrentModelInfo = func() *interfaces.ModelInfo { return info }
		if program != nil {
			program.Send(tui.ModelInfoMsg{Info: info})
		}
	}()

	if _, err := program.Run(); err != nil {
		// log.Fatalf bypasses deferred functions; release Herdr authority
		// explicitly so Herdr stops tracking this agent on fatal exit.
		m.HerdrRelease()
		log.Fatalf("Error running program: %v", err)
	}
	util.DebugEvent("shutdown")
}

// resolveVisionProvider selects the provider used to create the vision model.
// Precedence: an explicit vision_provider wins; otherwise vision_base_url
// forces an OpenAI-compatible endpoint; otherwise the main provider is reused.
func resolveVisionProvider(mainProvider agent.LLMProvider, cfg *config.Config) agent.LLMProvider {
	if cfg != nil && cfg.VisionProvider != "" {
		switch cfg.VisionProvider {
		case "gemini":
			return &agent.GeminiProvider{}
		case "openai", "openai-compatible":
			return &agent.OpenAIProvider{BaseURL: cfg.VisionBaseURL}
		}
	}
	if cfg != nil && cfg.VisionBaseURL != "" {
		return &agent.OpenAIProvider{BaseURL: cfg.VisionBaseURL}
	}
	return mainProvider
}
