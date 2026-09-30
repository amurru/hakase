package agent

import (
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/hooks"

	"google.golang.org/adk/v2/agent"
)

// wireHookSessionStart attaches the SessionStart-hooks renderer to the
// history builder (docs/hooks/spec.md HK-104). It is a no-op unless the
// runner could ever fire SessionStart: user groups exist, or the project
// layer is enabled (a project file may appear — or be trusted —
// mid-process). The trust store is opened best-effort here so project
// SessionStart handlers from a cloned repo stay skipped, never executed:
// an unopenable store trusts nothing.
func wireHookSessionStart(hb *hctx.HistoryBuilder, runner *hooks.Runner) {
	if hb == nil || runner == nil || !runner.HasSessionStart() {
		return
	}
	hb.SetSessionStartProvider(func(ctx agent.Context) string {
		return runner.RunSessionStart(ctx)
	})
}

// wireHookUserPrompt attaches the UserPromptSubmit renderer, extracting the
// current prompt text for the payload. No-op unless the runner could ever
// fire (user groups or project layer enabled).
func wireHookUserPrompt(hb *hctx.HistoryBuilder, runner *hooks.Runner) {
	if hb == nil || runner == nil || !runner.HasUserPrompt() {
		return
	}
	hb.SetUserPromptProvider(func(ctx agent.Context) string {
		var text string
		if uc := ctx.UserContent(); uc != nil {
			text = hctx.ContentText(uc)
		}
		return runner.RunUserPromptSubmit(ctx, text)
	})
}
