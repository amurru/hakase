// cron_headless_test.go - SL-001 bridge coverage: the headless model hook
// must resolve to the bootstrapped currentModel (whatever it is, including
// nil pre-bootstrap) and never panic callers like ModelPromptFn's lookup.
package cli

import (
	hctx "amurru/hakase/internal/context"
	"testing"
)

func TestAssignHeadlessModelFunc(t *testing.T) {
	oldModel := currentModel
	oldFn := hctx.CurrentModelFunc
	defer func() {
		currentModel = oldModel
		hctx.CurrentModelFunc = oldFn
	}()

	currentModel = nil
	assignHeadlessModelFunc()
	if hctx.CurrentModelFunc == nil {
		t.Fatal("hook must be installed")
	}
	if got := hctx.CurrentModelFunc(); got != nil {
		t.Fatalf("hook returned %v, want nil currentModel", got)
	}
}
