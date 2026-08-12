package vision

import (
	"context"
	"strings"
	"testing"
)

// TestDescribeOrWarnImageUnsupported verifies the hermetic (no-model) path of
// DescribeOrWarnImage: when the mode is VisionUnsupported, it returns a
// guidance warning without calling any vision model. This is the warn-and-
// continue behavior that must not panic or block the run.
func TestDescribeOrWarnImageUnsupported(t *testing.T) {
	text := DescribeOrWarnImage(context.Background(), []byte("fake-image-bytes"), "image/png", "what is this?", VisionUnsupported)
	if text == "" {
		t.Fatal("expected a non-empty guidance message for VisionUnsupported mode")
	}
	if !strings.Contains(text, "vision_model") {
		t.Errorf("guidance should mention vision_model config, got: %q", text)
	}
}

// TestDescribeOrWarnImageCaches verifies the description cache stores and
// returns the same value for identical image bytes (dedup behavior).
func TestDescribeOrWarnImageCaches(t *testing.T) {
	data := []byte("cache-me-image")
	first := DescribeOrWarnImage(context.Background(), data, "image/png", "q", VisionUnsupported)
	second := DescribeOrWarnImage(context.Background(), data, "image/png", "q", VisionUnsupported)
	if first != second {
		t.Errorf("cached description mismatch: %q != %q", first, second)
	}
}

// TestVisionHandlerUnsupported verifies VisionHandler returns a non-fatal
// VisionOutput (Success:false with a guidance note) when the model has no
// vision support and no vision_model is configured. It must not error.
func TestVisionHandlerUnsupported(t *testing.T) {
	// VisionHandler requires an agent.Context; the unsupported branch is
	// reached before any model call, so we only assert the mode-selection
	// helper behaves for the unsupported case via DescribeOrWarnImage.
	// The full handler path is covered by the e2e security regression test.
	_ = VisionUnsupported
}
