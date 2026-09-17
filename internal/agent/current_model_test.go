// current_model_test.go - SL-001/B5: the CurrentModelFunc hook constructor
// closes over the given model (nil-safe for pre-bootstrap callers).
package agent

import (
	"testing"
)

func TestCurrentModelFunc(t *testing.T) {
	if got := currentModelFunc(nil)(); got != nil {
		t.Fatalf("nil model hook returned %v", got)
	}
}
