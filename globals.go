// globals.go - Global variables for the agent migration.
// These exist because the full DI migration is incomplete.
package main

import (
	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/config"
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/interfaces"
	"google.golang.org/adk/v2/model"
	"time"
)

// Core runtime globals
var (
	currentModel          model.LLM
	currentConfig         *config.Config
	currentHistoryBuilder *hctx.HistoryBuilder
	currentModelInfo      *interfaces.ModelInfo
	delegateTimeout       = 300 * time.Second
)

// Approval/clarify globals
var (
	currentApproval config.ApprovalConfig
	currentClarify  config.ClarifyConfig
	currentGuard    config.LoopGuardConfig
)

// Event notifier globals (set by main.go, consumed by agent package)
var (
	taskBoardNotify        func(action string, task agent.TaskMeta)
	delegationProgressNotify func(status string, taskID, agentName, message string)
)

// Interactive gate globals (set by main.go after tea.Program exists)
var (
	askApproval func(req agent.ApprovalRequest) (bool, error)
	askClarify  func(req agent.ClarifyRequest) (agent.ClarifyResponse, error)
)

// Helper function globals
var (
	clarifyTimeout = func() time.Duration { return 120 * time.Second }
)
