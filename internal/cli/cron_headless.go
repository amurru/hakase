// cron_headless.go - headless model-function bridge for CLI/cron execution.
//
// cronModelBootstrap (cronjob.go) runs without SetupRunner, so the
// hctx.CurrentModelFunc hook (nil stub from cmd/hakase/init.go) must be
// pointed at the bootstrapped model here. This lives in its own file because
// cronModelBootstrap's local `model` variable shadows the adk model package,
// making a `model.LLM`-typed closure literal unnameable there.
package cli

import (
	hctx "amurru/hakase/internal/context"

	"google.golang.org/adk/v2/model"
)

// assignHeadlessModelFunc points the context package's model hook at the
// headless currentModel so ModelPromptFn (and everything built on it:
// knowledge enrichment, query expansion, skill mutation) works in CLI/cron
// runs. Called once from cronModelBootstrap after currentModel is set.
func assignHeadlessModelFunc() {
	hctx.CurrentModelFunc = func() model.LLM { return currentModel }
}
