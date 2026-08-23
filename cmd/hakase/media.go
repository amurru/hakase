package main

import (
	"fmt"

	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/media"
	"amurru/hakase/internal/util"
	"amurru/hakase/internal/web/handlers"

	"google.golang.org/adk/v2/tool"
)

// setupMedia builds the media store and registry, wires the tool factory into
// deps, and shares the registry with the web handlers. A nil registry means
// media generation is disabled; CreateMediaTools then returns an actionable
// error. Shared by the TUI (main.go) and web (web.go) bootstrap paths so the
// zero-config guarantee cannot drift between them.
func setupMedia(cfg *config.Config, deps *agent.Deps, logFn interfaces.LogFunc) {
	var reg *media.Registry
	store, err := media.NewStore(cfg.Media.OutputDir)
	if err == nil {
		reg, err = media.NewRegistry(cfg.Media, logFn, store)
	}
	if err != nil {
		reg = nil
		util.DebugEvent("media_disabled", "error", err.Error())
		logFn(fmt.Sprintf("WARN [media] disabled: %v", err))
	}
	deps.CreateMediaToolsFn = func(l interfaces.LogFunc) ([]tool.Tool, error) {
		return media.CreateMediaTools(reg, media.LogFunc(l))
	}
	handlers.SetMediaRegistry(reg)
}
