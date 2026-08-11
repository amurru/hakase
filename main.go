package main

import (
	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/cli"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/tui"
	"amurru/hakase/internal/util"
	"amurru/hakase/internal/vision"
	"context"
	"fmt"
	"log"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
)

func main() {
	// Dispatch CLI subcommands through the unified framework.
	if len(os.Args) > 1 {
		os.Exit(cli.Dispatch(os.Args[1:]))
	}

	ctx := context.Background()

	cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json"))
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	var p *tea.Program

	// Dev-mode structured JSON logging (feature flag from config or env).
	if p := util.InitDebugLogging(cfg.Debug); p != "" {
		util.DebugEvent("startup", "log_file", p, "debug", true)
	}
	defer util.CloseDebugLogging()

	// Define thread-safe logger function
	logToUI := func(msg string) {
		util.DebugEvent("status_log", "msg", msg)
		if p != nil {
			p.Send(tui.StatusLogMsg{Text: msg})
		}
	}

	// Push task board refreshes to the TUI whenever an agent tool mutates tasks
	taskBoardNotify = func(action string, task agent.TaskMeta) {
		if p != nil {
			p.Send(tui.TaskUpdateMsg{Task: task, Action: action})
		}
	}

	// Stream delegated sub-agent progress (text, tool calls, status) to the TUI
	delegationProgressNotify = func(status string, taskID, agentName, message string) {
		if p != nil {
			p.Send(tui.DelegationProgressMsg{TaskID: taskID, Agent: agentName, Status: status, Message: message})
		}
	}

	// Stream cron job lifecycle events (scheduled/completed/failed) to the TUI
	cli.CronJobNotify = func(status, jobID, name, summary, outputPath string) {
		if p != nil {
			p.Send(tui.CronJobMsg{JobID: jobID, Name: name, Status: status, Summary: summary, OutputPath: outputPath})
		}
	}

	// Create the session service up front so the same instance backs both the
	// TUI (persistence) and the runner's HistoryBuilder (history injection).
	var sessionSvc *session.SessionService
	if store, err := session.NewSessionStore(session.Dir); err == nil {
		if svc, err := session.NewSessionService(store); err == nil {
			sessionSvc = svc
		}
	}

	r, err := setupRunner(ctx, cfg, logToUI, sessionSvc)
	if err != nil {
		log.Fatalf("Failed to setup agent runner: %v", err)
	}

	m := tui.NewModel(ctx, r, sessionSvc, cfg.ChatBufferSize, cfg.ShowThinking, cfg.ModelName, cfg.ThinkingLevel)
	p = tea.NewProgram(&m)
	m.SetProgram(p)

	// Wire TUI hook variables for slash command handlers that live in root.
	tui.CurrentHistoryBuilder = currentHistoryBuilder
	tui.RunBoardCommand = runBoardCommand
	tui.RunMCPCommand = runMCPCommand

	// Set approval/clarify configs on the TUI package.
	tui.SetApprovalConfig(interfaces.ApprovalConfig{
		Mode:          cfg.Approval.Mode,
		ExpirySeconds: cfg.Approval.ExpirySeconds,
	})
	tui.SetClarifyConfig(interfaces.ClarifyConfig{
		ExpirySeconds: cfg.Clarify.ExpirySeconds,
	})

	// Share the TUI's mid-run message queue with the HistoryBuilder so
	// prompts typed while the agent is busy are steered into the running
	// session at model-call boundaries (BeforeModelCallback).
	if currentHistoryBuilder != nil {
		currentHistoryBuilder.SetPendingQueue(m.PendingQueue())
	}

	// Install the interactive approval gate. Bridge the interfaces.ApprovalRequest
	// type (from interfaces) to agent.ApprovalRequest (used by root globals).
	approvalExpiry := time.Duration(cfg.Approval.ExpirySeconds) * time.Second
	if approvalExpiry <= 0 {
		approvalExpiry = 60 * time.Second
	}
	askApproval = func(req agent.ApprovalRequest) (bool, error) {
		return m.AskApproval(interfaces.ApprovalRequest{
			Tool:      req.Tool,
			Command:   req.Command,
			Args:      req.Args,
			Risk:      req.Risk,
			Reason:    req.Reason,
			Source:    req.Source,
			ExpiresAt: req.ExpiresAt,
		})
	}

	// Install the interactive clarify gate.
	clarifyExpiry := time.Duration(cfg.Clarify.ExpirySeconds) * time.Second
	if clarifyExpiry <= 0 {
		clarifyExpiry = 120 * time.Second
	}
	askClarify = func(req agent.ClarifyRequest) (agent.ClarifyResponse, error) {
		res, err := m.AskClarify(interfaces.ClarifyRequest{
			Question:    req.Question,
			Choices:     req.Choices,
			MultiSelect: req.MultiSelect,
		})
		if err != nil {
			return agent.ClarifyResponse{}, err
		}
		return agent.ClarifyResponse{
			Answer:   res.Answer,
			Canceled: res.Canceled,
			TimedOut: res.TimedOut,
		}, nil
	}

	// Run stale session cleanup on startup.
	go func() {
		if sessionSvc != nil {
			removed, err := sessionSvc.CleanupStale(30 * 24 * time.Hour)
			if err == nil && removed > 0 {
				logToUI(fmt.Sprintf("🧹 Cleaned up %d stale session(s)", removed))
			}
		}
	}()

	// Run periodic stale session cleanup every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if sessionSvc != nil {
				removed, err := sessionSvc.CleanupStale(30 * 24 * time.Hour)
				if err == nil && removed > 0 {
					logToUI(fmt.Sprintf("🧹 Cleaned up %d stale session(s)", removed))
				}
			}
		}
	}()

	// Fetch model capabilities (context window, thinking support) in the
	// background so the status bar can show them once available.
	go func() {
		info, err := agent.FetchModelInfo(ctx, cfg)
		if err != nil {
			logToUI(fmt.Sprintf("⚠️ model info unavailable: %v", err))
			return
		}
		// Feed the model capabilities to the HistoryBuilder for budget math.
		if currentHistoryBuilder != nil {
			currentHistoryBuilder.SetModelInfo(info)
		}
		currentModelInfo = info
		vision.CurrentModelInfo = func() *interfaces.ModelInfo { return currentModelInfo }
		if p != nil {
			p.Send(tui.ModelInfoMsg{Info: info})
		}
	}()

	if _, err := p.Run(); err != nil {
		log.Fatalf("Error running program: %v", err)
	}
	util.DebugEvent("shutdown")
}
