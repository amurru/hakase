package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "skill" {
		os.Exit(runSkillCLI(os.Args[2:]))
	}

	if len(os.Args) > 1 && os.Args[1] == "task" {
		os.Exit(runTaskCLI(os.Args[2:]))
	}

	if len(os.Args) > 1 && os.Args[1] == "knowledge" {
		os.Exit(runKnowledgeCLI(os.Args[2:]))
	}

	if len(os.Args) > 1 && os.Args[1] == "session" {
		os.Exit(runSessionCLI(os.Args[2:]))
	}

	if len(os.Args) > 1 && os.Args[1] == "rules" {
		os.Exit(runRulesCLI(os.Args[2:]))
	}

	ctx := context.Background()

	cfg, err := loadConfig(resolveConfigPath("config.json"))
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	var p *tea.Program

	// Dev-mode structured JSON logging (feature flag from config or env).
	if p := initDebugLogging(cfg.Debug); p != "" {
		debugEvent("startup", "log_file", p, "debug", true)
	}
	defer closeDebugLogging()

	// Define thread-safe logger function
	logToUI := func(msg string) {
		debugEvent("status_log", "msg", msg)
		if p != nil {
			p.Send(StatusLogMsg{Text: msg})
		}
	}

	// Push task board refreshes to the TUI whenever an agent tool mutates tasks
	taskBoardNotify = func(action string, task TaskMeta) {
		if p != nil {
			p.Send(TaskUpdateMsg{Task: task, Action: action})
		}
	}

	// Stream delegated sub-agent progress (text, tool calls, status) to the TUI
	delegationProgressNotify = func(status string, taskID, agent, message string) {
		if p != nil {
			p.Send(DelegationProgressMsg{TaskID: taskID, Agent: agent, Status: status, Message: message})
		}
	}

	// Create the session service up front so the same instance backs both the
	// TUI (persistence) and the runner's HistoryBuilder (history injection).
	var sessionSvc *SessionService
	if store, err := NewSessionStore(sessionsDir); err == nil {
		if svc, err := NewSessionService(store); err == nil {
			sessionSvc = svc
		}
	}

	r, err := setupRunner(ctx, cfg, logToUI, sessionSvc)
	if err != nil {
		log.Fatalf("Failed to setup agent runner: %v", err)
	}

	m := newModel(ctx, r, sessionSvc, cfg.ChatBufferSize, cfg.ShowThinking, cfg.ModelName, cfg.ThinkingLevel)
	p = tea.NewProgram(&m)
	m.program = p

	// Share the TUI's mid-run message queue with the HistoryBuilder so
	// prompts typed while the agent is busy are steered into the running
	// session at model-call boundaries (BeforeModelCallback).
	if currentHistoryBuilder != nil {
		currentHistoryBuilder.SetPendingQueue(m.pendingQueue)
	}

	// Install the interactive approval gate so tool handlers can ask the
	// user for confirmation via the TUI approval modal.
	expiry := time.Duration(cfg.Approval.ExpirySeconds) * time.Second
	if expiry <= 0 {
		expiry = 60 * time.Second
	}
	askApproval = func(req ApprovalRequest) (bool, error) {
		if p == nil {
			return false, nil
		}
		resp := make(chan bool, 1)
		p.Send(approvalPromptMsg{Req: req, Resp: resp})
		return waitForApproval(resp, expiry), nil
	}

	// Install the interactive clarify gate so tool handlers can ask the user
	// mid-task questions via the TUI clarify modal.
	askClarify = func(req ClarifyRequest) (ClarifyResponse, error) {
		if p == nil {
			return ClarifyResponse{}, fmt.Errorf("no TUI available")
		}
		resp := make(chan ClarifyResponse, 1)
		p.Send(clarifyPromptMsg{Req: req, Resp: resp})
		return waitForClarify(resp, clarifyTimeout()), nil
	}

	// Run stale session cleanup on startup.
	go func() {
		if m.sessionService != nil {
			removed, err := m.sessionService.CleanupStale(30 * 24 * time.Hour)
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
			if m.sessionService != nil {
				removed, err := m.sessionService.CleanupStale(30 * 24 * time.Hour)
				if err == nil && removed > 0 {
					logToUI(fmt.Sprintf("🧹 Cleaned up %d stale session(s)", removed))
				}
			}
		}
	}()

	// Fetch model capabilities (context window, thinking support) in the
	// background so the status bar can show them once available.
	go func() {
		info, err := FetchModelInfo(ctx, cfg)
		if err != nil {
			logToUI(fmt.Sprintf("⚠️ model info unavailable: %v", err))
			return
		}
		// Feed the model capabilities to the HistoryBuilder for budget math.
		if currentHistoryBuilder != nil {
			currentHistoryBuilder.SetModelInfo(info)
		}
		currentModelInfo = info
		if p != nil {
			p.Send(ModelInfoMsg{Info: info})
		}
	}()

	if _, err := p.Run(); err != nil {
		log.Fatalf("Error running program: %v", err)
	}
	debugEvent("shutdown")
}
