package vision

import (
	"context"
	"encoding/base64"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// testPNGBytes is a 1x1 transparent PNG.
var testPNGBytes, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+ip1sAAAAASUVORK5CYII=")

// TestInjectionCallbackRewritesAttachedImagesWhenConfigWired is the
// regression guard for the web chat attachment crash
// ("openai: unsupported content part *genai.Part"): when CurrentConfig is
// wired (as main.go / web.go / cronjob bootstrap now do), the BeforeModel
// callback must replace user-attached InlineData parts with text so the
// OpenAI-compatible adapter never sees raw pixels.
func TestInjectionCallbackRewritesAttachedImagesWhenConfigWired(t *testing.T) {
	oldCfgHook, oldMIHook, oldLLM := CurrentConfig, CurrentModelInfo, VisionModelLLM
	t.Cleanup(func() {
		CurrentConfig, CurrentModelInfo, VisionModelLLM = oldCfgHook, oldMIHook, oldLLM
	})

	cfg := &config.Config{
		Provider:    "openai-compatible",
		VisionModel: "test-vision-model",
	}
	CurrentConfig = func() *config.Config { return cfg }
	CurrentModelInfo = func() *interfaces.ModelInfo { return nil }
	VisionModelLLM = nil // legacy path degrades to a warning note, still text

	prompt := "animate this image\n[attachments]\n@frame.png outputs/media/frame.png (image/png)"
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromParts([]*genai.Part{
				genai.NewPartFromText(prompt),
				genai.NewPartFromBytes(testPNGBytes, "image/png"),
			}, genai.RoleUser),
		},
	}

	mockCtx := &agent.StrictContextMock{Ctx: context.Background()}
	if _, err := VisionInjectionCallback(mockCtx, req); err != nil {
		t.Fatalf("callback: %v", err)
	}

	for ci, content := range req.Contents {
		for pi, part := range content.Parts {
			if part != nil && part.InlineData != nil {
				t.Fatalf("InlineData survived the rewrite at content %d part %d - would crash the openai adapter", ci, pi)
			}
		}
	}
	replaced := false
	for _, part := range req.Contents[0].Parts {
		if part.Text != "" && part.Text != prompt {
			replaced = true // description or warning note
		}
	}
	if !replaced {
		t.Fatal("expected the image part to be replaced with a text part")
	}
}

// TestInjectionCallbackLeavesPixelsWithoutConfig documents the failure mode
// that shipped broken in c2668a7 (the phase0-wave5 DI refactor dropped the
// real config hook): an unwired CurrentConfig skips the rewrite entirely,
// leaving InlineData in place for the openai adapter to reject. Entry points
// MUST wire vision.CurrentConfig.
func TestInjectionCallbackLeavesPixelsWithoutConfig(t *testing.T) {
	oldCfgHook := CurrentConfig
	t.Cleanup(func() { CurrentConfig = oldCfgHook })
	CurrentConfig = nil

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromParts([]*genai.Part{
				genai.NewPartFromBytes(testPNGBytes, "image/png"),
			}, genai.RoleUser),
		},
	}
	mockCtx := &agent.StrictContextMock{Ctx: context.Background()}
	if _, err := VisionInjectionCallback(mockCtx, req); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if req.Contents[0].Parts[0].InlineData == nil {
		t.Fatal("expected unwired config to skip the rewrite (documents why wiring is mandatory)")
	}
}
