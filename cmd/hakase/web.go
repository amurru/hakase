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
	"strconv"
	"syscall"
	"time"

	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/auth"
	"amurru/hakase/internal/channel"
	"amurru/hakase/internal/channel/telegram"
	"amurru/hakase/internal/cli"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/knowledge"
	"amurru/hakase/internal/mcp"
	"amurru/hakase/internal/registry"
	"amurru/hakase/internal/sandbox"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/skill"
	"amurru/hakase/internal/vision"
	"amurru/hakase/internal/web"
	"amurru/hakase/internal/web/handlers"
	"amurru/hakase/internal/web/sse"

	"google.golang.org/adk/v2/tool"
)

// minChannelGateExpirySeconds is the floor the bootstrap applies to the
// approval/clarify gate expiries when a channel is enabled: chat round-trips
// are slower than a desktop modal (Hermes defaults to 600s).
const minChannelGateExpirySeconds = 300

// insecureCookieFlag implements flag.Value for --insecure-cookie. Unlike a
// plain fs.Bool it records whether the flag was explicitly set, so the CLI can
// override the config file value in both directions (true and false) while an
// absent flag defers to the config file.
type insecureCookieFlag struct {
	value bool
	set   bool
}

// IsBoolFlag lets -insecure-cookie (no value) mean true, like the built-in
// bool flags.
func (f *insecureCookieFlag) IsBoolFlag() bool { return true }

func (f *insecureCookieFlag) Set(s string) error {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("invalid boolean value %q for -insecure-cookie: %v", s, err)
	}
	f.value = b
	f.set = true
	return nil
}

func (f *insecureCookieFlag) String() string { return strconv.FormatBool(f.value) }

// registerWebFlags registers the shared web/serve flags on fs and returns
// pointers to their parsed values. Single source of truth for the flag
// definitions so tests exercise the exact registration used by runServer.
func registerWebFlags(fs *flag.FlagSet) (port *int, host *string, insecureCookie *insecureCookieFlag) {
	port = fs.Int("port", 0, "port to listen on")
	host = fs.String("host", "", "host address to bind to")
	insecureCookie = &insecureCookieFlag{}
	fs.Var(insecureCookie, "insecure-cookie", "allow insecure (non-HTTPS) cookie transmission")
	return port, host, insecureCookie
}

// applyInsecureCookiePrecedence resolves the effective allow-insecure-cookie
// setting: an explicitly-set CLI flag wins over the config file value, which
// wins over the default (false).
func applyInsecureCookiePrecedence(cfgValue, flagSet, flagValue bool) bool {
	if flagSet {
		return flagValue
	}
	return cfgValue
}

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
	port, host, insecureCookie := registerWebFlags(fs)
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

	// --insecure-cookie (CLI) overrides auth.allow_insecure_cookie (config
	// file), which defaults to false. The cookie setter consumes the resolved
	// value (security-hardening Task 17 - W8).
	cfg.Auth.AllowInsecureCookie = applyInsecureCookiePrecedence(cfg.Auth.AllowInsecureCookie, insecureCookie.set, insecureCookie.value)

	// Init sandbox before any file or exec operations.
	sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(cfg.Sandbox)

	// Channels enabled? Approvals/clarifications may then be answered from a
	// phone, where sub-minute expiry is unanswerable (Hermes uses 600s).
	// Clamp to >=300s so the gates stay answerable over a chat round-trip.
	if cfg.Channels.Telegram.EnabledWithToken() {
		if cfg.Approval.ExpirySeconds > 0 && cfg.Approval.ExpirySeconds < minChannelGateExpirySeconds {
			log.Printf("web: channels enabled - approval expiry raised %ds -> %ds", cfg.Approval.ExpirySeconds, minChannelGateExpirySeconds)
			cfg.Approval.ExpirySeconds = minChannelGateExpirySeconds
		}
		if cfg.Clarify.ExpirySeconds > 0 && cfg.Clarify.ExpirySeconds < minChannelGateExpirySeconds {
			log.Printf("web: channels enabled - clarify expiry raised %ds -> %ds", cfg.Clarify.ExpirySeconds, minChannelGateExpirySeconds)
			cfg.Clarify.ExpirySeconds = minChannelGateExpirySeconds
		}
	}

	// Vision hooks: the BeforeModel callback rewrites user-attached image
	// parts into text (vision-model description) before the OpenAI-compatible
	// adapter sees them; without the config hook the rewrite is skipped and
	// InlineData parts crash the call with "openai: unsupported content part".
	// Config is load-once per process, so closing over cfg matches its lifecycle.
	vision.CurrentConfig = func() *config.Config { return cfg }

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

	// Project registry (registered remote projects, project-registry DP-6+).
	// Checkouts live under the hakase home; a corrupt registry file disables
	// project features (registry.Current stays nil) without blocking the web
	// UI - the /api/projects endpoints then report 503.
	if st, err := registry.NewStore(registry.DefaultPath()); err == nil {
		registry.Current = registry.NewService(st, interfaces.LogFunc(logToFile))
	} else {
		log.Printf("web: project registry unavailable: %v", err)
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
			// Share the live manager through the package global so the web
			// API handlers and other consumers can reach it.
			mcp.MCPManager = mgr
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
			// The background scheduler is TUI-only by default; channels
			// deployments opt in so scheduled jobs fire while headless.
			if cfg.Channels.EnableCronScheduler {
				cli.StartCronScheduler(logFn)
			}
		},

		BuildQueryExpansionPromptFn: knowledge.BuildQueryExpansionPrompt,
		ParseQueryExpansionsFn:      knowledge.ParseQueryExpansions,

		ResolveVisionProviderFn: resolveVisionProvider,
	}

	// Media generation (zero-config guarantee, same as TUI).
	setupMedia(cfg, deps, interfaces.LogFunc(logToFile))

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

	// Fetch model capabilities (context window, thinking support) in the
	// background and feed them to the HistoryBuilder for budget math and to
	// the vision package for main-model vision detection (mirrors runTUI in
	// main.go). Without this the HistoryBuilder has no budget limits; the
	// defensive nil guard in fitToBudget keeps runs safe until it lands.
	go func() {
		info, err := agent.FetchModelInfo(ctx, cfg)
		if err != nil {
			log.Printf("web: model info unavailable: %v", err)
			return
		}
		if deps.HistoryBuilder != nil {
			deps.HistoryBuilder.SetModelInfo(info)
		}
		vision.CurrentModelInfo = func() *interfaces.ModelInfo { return info }
	}()

	// Build the web server.
	srv := web.NewServer(jwtKey, sessionSvc)
	srv.SetAllowInsecureCookie(cfg.Auth.AllowInsecureCookie)
	srv.SetChatDeps(bridge, runner, runtime)
	srv.SetHistoryBuilder(deps.HistoryBuilder)
	srv.SetGates(approvalGate, clarifyGate)

	// Cron lifecycle events reach the SSE bridge in web mode too (the TUI
	// wires its own listener in main.go); the channel router subscribes to
	// the same bridge events for push notifications.
	cli.CronJobNotify = bridge.CronJobEvent

	// Communication channels (Telegram et al.): start inside this process so
	// they share the runner, gates, bridge, sessions, and cron registry.
	// Fail-soft: a bad token logs loudly but never kills the web server.
	stopChannels := func() {}
	if cfg.Channels.Telegram.EnabledWithToken() {
		chanSvc, err := channel.NewService(channel.Deps{
			Bridge:   bridge,
			Runner:   runner,
			Sessions: sessionSvc,
			Approval: approvalGate,
			Clarify:  clarifyGate,
			Log:      func(format string, args ...any) { log.Printf("web: "+format, args...) },
		})
		if err != nil {
			log.Printf("web: channel service unavailable: %v", err)
		} else {
			tg, err := telegram.New(telegram.Deps{
				Service: chanSvc,
				Config:  cfg.Channels.Telegram,
				Log:     func(format string, args ...any) { log.Printf("telegram: "+format, args...) },
			})
			if err != nil {
				log.Printf("web: telegram channel disabled: %v", err)
			} else {
				chanSvc.Register(tg, tg)
				chanSvc.Start()
				log.Printf("web: telegram channel started")
				stopChannels = func() { chanSvc.Stop(5 * time.Second) }
			}
		}
	}

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
		stopChannels()
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
			stopChannels()
			return 1
		}
		stopChannels()
		return 0
	}
}
