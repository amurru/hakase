// web.go - the `hakase web` and `hakase serve` server bootstrap.
// Lives in package main because handlers/cron.go imports internal/cli,
// preventing internal/cli from importing internal/web (import cycle).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/auth"
	"amurru/hakase/internal/cli"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/knowledge"
	"amurru/hakase/internal/mcp"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/skill"
	"amurru/hakase/internal/web"
	"amurru/hakase/internal/web/handlers"
	"amurru/hakase/internal/web/sse"

	"google.golang.org/adk/v2/tool"
)

// runWeb starts the full web UI server (SPA + API).
func runWeb(args []string) int {
	return runServer(args, true)
}

// runServe starts the API-only server (no SPA).
func runServe(args []string) int {
	return runServer(args, false)
}

// runServer is the shared bootstrap for web and serve modes.
func runServer(args []string, serveSPA bool) int {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	port := fs.Int("port", 0, "port to listen on")
	host := fs.String("host", "", "host address to bind to")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintf(os.Stderr, "hakase: invalid flags: %v\n", err)
		return 2
	}

	// Defaults: web=8080, serve=8081
	if *port == 0 {
		if serveSPA {
			*port = 8080
		} else {
			*port = 8081
		}
	}
	if *host == "" {
		*host = "127.0.0.1"
	}

	// Security: warn about network exposure.
	if *host == "0.0.0.0" {
		fmt.Fprintln(os.Stderr, "WARNING: Binding to 0.0.0.0 exposes the server on all network interfaces.")
		fmt.Fprintln(os.Stderr, "         Use a reverse proxy (nginx/Caddy) for TLS termination.")
	}

	// Security check: credentials must exist.
	home := config.HakaseHome()
	credsPath := filepath.Join(home, "credentials.json")
	if _, err := os.Stat(credsPath); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "Run 'hakase auth set-password' first")
		return 1
	}

	// Load/generate JWT secret.
	jwtSecretPath := filepath.Join(home, "jwt-secret")
	jwtKey, err := auth.GenerateOrLoadSecret(jwtSecretPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: failed to load JWT secret: %v\n", err)
		return 1
	}

	// Load config.
	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: failed to load config: %v\n", err)
		return 1
	}

	// Init debug logging.
	_ = os.MkdirAll(filepath.Join(home, "logs"), 0o755)
	logToFile := func(msg string) {
		log.Printf("web: %s", msg)
	}

	// Create session service.
	var sessionSvc *hakasesession.SessionService
	if store, err := hakasesession.NewSessionStore(hakasesession.Dir); err == nil {
		if svc, err := hakasesession.NewSessionService(store); err == nil {
			sessionSvc = svc
		}
	}

	// Build agent Deps (same wiring as main.go's runTUI).
	deps := &agent.Deps{
		Config:         cfg,
		Log:            interfaces.LogFunc(logToFile),
		SessionService: sessionSvc,

		NewMCPServerManagerFn: func(cfgAny any, logFn interfaces.LogFunc) (any, error) {
			c, ok := cfgAny.(*config.Config)
			if !ok {
				return nil, fmt.Errorf("NewMCPServerManagerFn: config is not *config.Config")
			}
			mgr, err := mcp.NewMCPServerManager(c, logFn)
			if err != nil {
				return nil, err
			}
			return mgr, nil
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
			// No background scheduler in web mode.
		},

		BuildQueryExpansionPromptFn: knowledge.BuildQueryExpansionPrompt,
		ParseQueryExpansionsFn:      knowledge.ParseQueryExpansions,

		ResolveVisionProviderFn: resolveVisionProvider,
	}

	// Create the SSE bridge for real-time event streaming.
	bridge := sse.NewEventBridge()

	// Create web-based gates backed by the SSE bridge.
	approvalGate := handlers.NewWebApprovalGate(bridge, "", interfaces.ApprovalConfig{
		Mode:          cfg.Approval.Mode,
		ExpirySeconds: cfg.Approval.ExpirySeconds,
	})
	clarifyGate := handlers.NewWebClarifyGate(bridge, "", interfaces.ClarifyConfig{
		ExpirySeconds: cfg.Clarify.ExpirySeconds,
	})

	// Build Runtime and wire gates.
	runtime := &agent.Runtime{}
	runtime.SetApprovalGate(approvalGate)
	runtime.SetClarifyGate(clarifyGate)
	runtime.SetEventNotifier(bridge)

	// SetupRunner builds the ADK runner.
	ctx := context.Background()
	runner, err := agent.SetupRunner(ctx, deps, runtime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: failed to setup agent runner: %v\n", err)
		return 1
	}

	// Build the web server.
	srv := web.NewServer(jwtKey, sessionSvc)
	srv.SetChatDeps(bridge, runner, runtime)
	srv.SetGates(approvalGate, clarifyGate)

	// Register routes.
	if serveSPA {
		srv.RegisterDefaults(web.FrontendAssets())
	} else {
		srv.RegisterDefaults(nil)
	}

	// Start the HTTP server.
	addr := fmt.Sprintf("%s:%d", *host, *port)
	done := make(chan error, 1)

	go func() {
		if serveSPA {
			fmt.Printf("Hakase web UI: http://%s\n", addr)
		} else {
			fmt.Printf("Hakase API: http://%s\n", addr)
		}
		done <- srv.Run(addr)
	}()

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-done:
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "hakase: server error: %v\n", err)
			return 1
		}
		return 0
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "\nhakase: received %v, shutting down...\n", sig)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "hakase: shutdown error: %v\n", err)
			return 1
		}
		return 0
	}
}
