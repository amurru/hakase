// hooks.go - Function hook variables set by the root package (main.go) to
// bridge slash command handlers and runtime state that live in the root package
// until task 12/13 migrate them fully.
package tui

import (
	"amurru/hakase/internal/config"
	hctx "amurru/hakase/internal/context"
	tea "charm.land/bubbletea/v2"
)

// CurrentGuard holds the agent's degeneracy guard config, set before the
// TUI starts. LoopGuardDefaults holds the function that populates defaults.
var (
	CurrentGuard       config.LoopGuardConfig
	LoopGuardDefaults  func(config.LoopGuardConfig) any // returns agent.DegenerationGuard
)

// CurrentHistoryBuilder is the active HistoryBuilder for /compact.
// Set before the TUI starts; updated by the runner.
var CurrentHistoryBuilder *hctx.HistoryBuilder

// RunBoardCommand is the slash command handler for /board, wired by root's
// task_slash.go.
var RunBoardCommand func(m *AppModel, args string) tea.Cmd

// RunMCPCommand is the slash command handler for /mcp, wired by root's
// mcp_slash.go.
var RunMCPCommand func(m *AppModel, args string) tea.Cmd
