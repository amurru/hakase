// tui_bridge.go - Type aliases and hook wiring for the internal/tui package.
// This file bridges the root package to the tui package until task 13 completes
// the full dependency injection migration.
package main

import (
	"amurru/hakase/internal/tui"
)

// appModel is a type alias for the TUI's model, needed by root slash command
// handlers (mcp_slash.go, task_slash.go) that stay in the root package until
// task 12 migrates them.
type appModel = tui.AppModel

// SlashCommand describes a built-in slash command (type alias for root visibility).
type SlashCommand = tui.SlashCommand
