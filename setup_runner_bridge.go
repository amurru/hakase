// setup_runner_bridge.go - Bridge for setupRunner to maintain backward compatibility.
package main

import (
	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"
	"context"

	"google.golang.org/adk/v2/runner"
)

// setupRunner is a wrapper that adapts the old signature to the new agent.SetupRunner.
// This maintains backward compatibility with main.go while using the new DI pattern.
func setupRunner(ctx context.Context, cfg *config.Config, logToUI func(string), sessionSvc *hakasesession.SessionService) (*runner.Runner, error) {
	// Create Deps struct with the provided parameters
	deps := &agent.Deps{
		Config:         cfg,
		Log:            interfaces.LogFunc(logToUI),
		SessionService: sessionSvc,
	}

	// Create Runtime struct (will be populated later by main.go)
	runtime := &agent.Runtime{}

	// Call the new SetupRunner
	runner, err := agent.SetupRunner(ctx, deps, runtime)
	if err != nil {
		return nil, err
	}

	// Store the runtime and deps in globals for later access
	// This is a temporary bridge until full DI migration
	currentConfig = cfg
	if deps.HistoryBuilder != nil {
		currentHistoryBuilder = deps.HistoryBuilder
	}

	return runner, nil
}
