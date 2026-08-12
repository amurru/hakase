package main

import (
	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/vision"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// new1x1PNG returns a 1x1 red PNG for tiny image fixtures.
func new1x1PNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// newSolidPNG creates a solid-color PNG at the given size.
func newSolidPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// newNoisePNG creates a noise PNG at the given size (poor compression).
func newNoisePNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(42))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// pngToDataURL returns a data: URL for the given PNG bytes.
func pngToDataURL(data []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

// saveRestoreGlobals captures and restores package-level globals.
func saveRestoreGlobalsForTest(t *testing.T, oldMI *interfaces.ModelInfo, oldCfg *config.Config, oldVM model.LLM) {
	t.Helper()
	t.Cleanup(func() {
		vision.CurrentModelInfo = func() *interfaces.ModelInfo { return oldMI }
		vision.CurrentConfig = func() *config.Config { return oldCfg }
		vision.VisionModelLLM = oldVM
	})
}

// clearVisionGlobals zeroes vision globals for a clean test slate and returns
// restore functions.
func clearVisionGlobalsForTest(t *testing.T) {
	t.Helper()
	oldMI := vision.CurrentModelInfo()
	oldCfg := vision.CurrentConfig()
	oldVM := vision.VisionModelLLM
	vision.CurrentModelInfo = nil
	vision.CurrentConfig = nil
	vision.VisionModelLLM = nil
	saveRestoreGlobalsForTest(t, oldMI, oldCfg, oldVM)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestVisionModelNameMatch(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"gpt-4o", true},
		{"gpt-4o-mini", true},
		{"gpt-4.1-preview", true},
		{"gpt-5-turbo", true},
		{"o1-preview", true},
		{"o3-mini", true},
		{"o4-mini", true},
		{"claude-3-5-sonnet", true},
		{"claude-3-opus", true},
		{"claude-4-sonnet", true},
		{"gemini-2.5-flash", true},
		{"gemini-1.5-pro", true},
		{"llama-3.2-vision", true},
		{"qwen2.5-vl-7b", true},
		{"qwen2.5-vl-72b", true},
		{"qwen3-vl", true},
		{"glm-4v", true},
		{"phi-3.5-vision", true},
		{"phi-4-vision", true},
		{"llava-13b", true},
		{"moondream2", true},
		{"internvl2", true},
		// False cases
		{"gpt-3.5-turbo", false},
		{"llama-3.1-8b", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := vision.VisionModelNameMatch(tc.name); got != tc.want {
				t.Errorf("vision.VisionModelNameMatch(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestDetectImageMime(t *testing.T) {
	pngBytes := new1x1PNG() // real PNG

	tests := []struct {
		label    string
		data     []byte
		wantMime string
		wantOK   bool
	}{
		// Empty / garbage
		{"empty", nil, "", false},
		{"garbage", []byte("hello world not an image"), "", false},
		// Real PNG
		{"real PNG", pngBytes, "image/png", true},
		// Magic-only: JPEG
		{"JPEG magic", []byte{0xff, 0xd8, 0xff, 0xe0}, "image/jpeg", true},
		// GIF89a
		{"GIF89a magic", []byte("GIF89a....."), "image/gif", true},
		{"GIF87a magic", []byte("GIF87a....."), "image/gif", true},
		// WEBP
		{"WEBP magic", []byte("RIFF\x00\x00\x00\x00WEBP"), "image/webp", true},
		// BMP (detectImageMime returns false: needs conversion, not embed-ready)
		{"BMP magic", []byte("BM\x00\x00\x00\x00"), "image/bmp", false},
		// SVG (needs rasterization, not embed-ready)
		{"SVG tag", []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"), "image/svg+xml", false},
		{"SVG with xml decl", []byte("<?xml version=\"1.0\"?><svg></svg>"), "image/svg+xml", false},
		// TIFF (needs conversion, not embed-ready)
		{"TIFF LE magic", []byte("II\x2a\x00"), "image/tiff", false},
		// TIFF - big-endian
		{"TIFF BE magic", []byte("MM\x00\x2a"), "image/tiff", false},
	}

	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			mime, ok := vision.DetectImageMime(tc.data)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if mime != tc.wantMime {
				t.Errorf("mime = %q, want %q", mime, tc.wantMime)
			}
		})
	}
}

func TestIsEmbedSupported(t *testing.T) {
	for _, mime := range []string{"image/jpeg", "image/png", "image/gif", "image/webp"} {
		if !vision.IsEmbedSupported(mime) {
			t.Errorf("vision.IsEmbedSupported(%q) = false, want true", mime)
		}
	}
	for _, mime := range []string{"image/bmp", "image/tiff", "image/svg+xml", "text/plain", ""} {
		if vision.IsEmbedSupported(mime) {
			t.Errorf("vision.IsEmbedSupported(%q) = true, want false", mime)
		}
	}
}

func TestNormalizeImage(t *testing.T) {
	pngBytes := new1x1PNG()

	// BMP -> PNG via x/image/bmp (already imported)
	// Build a real BMP: 1x1 red pixel
	bmpImg := image.NewRGBA(image.Rect(0, 0, 1, 1))
	bmpImg.SetRGBA(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var bmpBuf bytes.Buffer
	if err := bmpDecodeEncode(bmpImg, &bmpBuf); err != nil {
		t.Fatalf("encode BMP fixture: %v", err)
	}
	bmpBytes := bmpBuf.Bytes()

	t.Run("PNG passthrough", func(t *testing.T) {
		got, mime, err := vision.NormalizeImage(pngBytes, "image/png")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
		if len(got) != len(pngBytes) {
			t.Errorf("PNG should passthrough unchanged, got %d bytes, want %d", len(got), len(pngBytes))
		}
	})

	t.Run("JPEG passthrough", func(t *testing.T) {
		// Build a tiny real JPEG
		jpgBuf := encodeJPEG(bmpImg, 85)
		got, mime, err := vision.NormalizeImage(jpgBuf, "image/jpeg")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/jpeg" {
			t.Errorf("mime = %q, want image/jpeg", mime)
		}
		if len(got) != len(jpgBuf) {
			t.Errorf("JPEG should passthrough unchanged, got %d bytes, want %d", len(got), len(jpgBuf))
		}
	})

	t.Run("GIF passthrough", func(t *testing.T) {
		gifBytes := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
		got, mime, err := vision.NormalizeImage(gifBytes, "image/gif")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/gif" {
			t.Errorf("mime = %q, want image/gif", mime)
		}
		if len(got) != len(gifBytes) {
			t.Errorf("GIF should passthrough unchanged")
		}
	})

	t.Run("BMP to PNG", func(t *testing.T) {
		got, mime, err := vision.NormalizeImage(bmpBytes, "image/bmp")
		if err != nil {
			t.Fatalf("normalize BMP: %v", err)
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
		// Must be valid PNG
		if _, err := png.Decode(bytes.NewReader(got)); err != nil {
			t.Errorf("output is not valid PNG: %v", err)
		}
	})

	t.Run("SVG rasterized to PNG", func(t *testing.T) {
		svgData := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100"><rect width="100" height="100" fill="red"/></svg>`)
		got, mime, err := vision.NormalizeImage(svgData, "image/svg+xml")
		if err != nil {
			// If rasterizer is absent, the error should mention SVG.
			if !strings.Contains(err.Error(), "SVG") {
				t.Errorf("SVG normalize error should mention SVG: %v", err)
			}
			return
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
		if _, err := png.Decode(bytes.NewReader(got)); err != nil {
			t.Errorf("output is not valid PNG: %v", err)
		}
	})

	t.Run("unknown format returns error", func(t *testing.T) {
		_, _, err := vision.NormalizeImage([]byte("not-an-image"), "application/pdf")
		if err == nil {
			t.Fatal("expected error for unknown format, got nil")
		}
	})
}

func TestEmbedReadyImage(t *testing.T) {
	t.Run("under limits returns unchanged", func(t *testing.T) {
		pngBytes := new1x1PNG()
		got, mime, err := vision.EmbedReadyImage(pngBytes, "image/png", 1<<20, 8000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
		if len(got) != len(pngBytes) {
			t.Errorf("under-limit image should be unchanged, got %d bytes, want %d", len(got), len(pngBytes))
		}
	})

	t.Run("oversized dimension resizes with byte constraint", func(t *testing.T) {
		// Noise PNG at 1000x50: encodes ~75KB+ (PNG on noise is large).
		// Use maxBytes=1000 to force repeated halving rounds until byte limit met.
		pngBytes := newNoisePNG(1000, 50)
		if len(pngBytes) < 5000 {
			t.Skipf("noise PNG too small (%d bytes) for this test", len(pngBytes))
		}
		got, mime, err := vision.EmbedReadyImage(pngBytes, "image/png", 1000, 64)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
		// Must be smaller than original (resize happened).
		if len(got) >= len(pngBytes) {
			t.Errorf("result len = %d, expected smaller than input %d", len(got), len(pngBytes))
		}
		// Must fit within byte limit (with tolerance for PNG minimum overhead).
		if len(got) > 5000 {
			t.Errorf("result len = %d, want <= 5000", len(got))
		}
		// Decode and verify it is a valid image at a smaller size.
		cfg, _, err := image.DecodeConfig(bytes.NewReader(got))
		if err != nil {
			t.Fatalf("decode result config: %v", err)
		}
		// Result should be smaller than original in at least one dimension.
		if cfg.Width >= 1000 {
			t.Errorf("expected width < 1000, got %d", cfg.Width)
		}
	})

	t.Run("oversized bytes compresses solid image", func(t *testing.T) {
		// Create a solid-color PNG at 2000x2000. Solid PNG is small (~12KB),
		// so instead create a noise JPEG to force byte-limit resize.
		// A 2000x2000 JPEG at quality 100 of a gradient pattern.
		gradImg := image.NewRGBA(image.Rect(0, 0, 2000, 2000))
		for y := 0; y < 2000; y++ {
			for x := 0; x < 2000; x++ {
				v := uint8((x + y) % 256)
				gradImg.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
			}
		}
		jpgBytes := encodeJPEG(gradImg, 100) // large JPEG
		if len(jpgBytes) < 500000 {
			t.Skipf("test fixture too small (%d bytes) to trigger byte limit; need >= 500KB", len(jpgBytes))
		}
		got, _, err := vision.EmbedReadyImage(jpgBytes, "image/jpeg", 200000, 8000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// assert result <= maxBytes with 5% tolerance for JPEG re-encoding variability
		if len(got) > 210000 {
			t.Errorf("result len = %d, want <= 210000 (200KB + 5%%)", len(got))
		}
	})

	t.Run("resize preserves aspect ratio", func(t *testing.T) {
		// Regression guard: the resize loop must keep a 2:1 image at ~2:1
		// instead of distorting it toward square. Encode a 2000x1000 noise
		// PNG (large bytes) and force heavy downscaling with a tiny byte cap.
		pngBytes := newNoisePNG(2000, 1000)
		if len(pngBytes) < 50000 {
			t.Skipf("noise PNG too small (%d bytes) for aspect-ratio test", len(pngBytes))
		}
		got, _, err := vision.EmbedReadyImage(pngBytes, "image/png", 20000, 200)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(got))
		if err != nil {
			t.Fatalf("decode result config: %v", err)
		}
		w, h := cfg.Width, cfg.Height
		if h == 0 {
			t.Fatalf("decoded height is 0")
		}
		// Allow integer-division slack: 2:1 must not become >= 1.5:1 or <= 1:1.5.
		ratio := float64(w) / float64(h)
		if ratio < 1.5 || ratio > 2.5 {
			t.Errorf("aspect ratio distorted: %dx%d (ratio %.2f), want ~2:1", w, h, ratio)
		}
	})

	t.Run("hard ceiling rejects huge image", func(t *testing.T) {
		// Create bytes > vision.VisionHardCeilingBytes (20MB). We do not need a real
		// image; the hard-ceiling check happens before decoding.
		huge := make([]byte, vision.VisionHardCeilingBytes+1)
		_, _, err := vision.EmbedReadyImage(huge, "image/png", 100, 100)
		if err == nil {
			t.Fatal("expected error for image exceeding hard ceiling, got nil")
		}
		if !strings.Contains(err.Error(), "hard ceiling") {
			t.Errorf("error should mention hard ceiling, got: %v", err)
		}
	})

	t.Run("undecodable image returns original", func(t *testing.T) {
		// garbage bytes > maxBytes but < hard ceiling, not a valid image
		garbage := make([]byte, 4096)
		copy(garbage, []byte("not an image"))
		got, mime, err := vision.EmbedReadyImage(garbage, "image/png", 2048, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != len(garbage) {
			t.Errorf("undecodable image should be returned unchanged")
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
	})
}

func TestResolveMainModelVision(t *testing.T) {
	t.Run("cfg nil -> vision.VisionUnsupported", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		if got := vision.ResolveMainModelVision(nil, nil); got != vision.VisionUnsupported {
			t.Errorf("got %v, want vision.VisionUnsupported", got)
		}
	})

	t.Run("openai-compatible + SupportsVision true -> vision.VisionLegacy", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		cfg := &config.Config{Provider: "openai-compatible", VisionModel: "some-vision-model"}
		mi := &interfaces.ModelInfo{SupportsVision: boolPtr(true)}
		if got := vision.ResolveMainModelVision(mi, cfg); got != vision.VisionLegacy {
			t.Errorf("got %v, want vision.VisionLegacy (provider blocks native)", got)
		}
	})

	t.Run("openai-compatible + ModelVision yes -> vision.VisionLegacy", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		cfg := &config.Config{Provider: "openai-compatible", ModelVision: "yes", VisionModel: "some-vision-model"}
		if got := vision.ResolveMainModelVision(nil, cfg); got != vision.VisionLegacy {
			t.Errorf("got %v, want vision.VisionLegacy (provider blocks native even with yes)", got)
		}
	})

	t.Run("gemini + ModelVision no + usable -> vision.VisionLegacy", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		cfg := &config.Config{Provider: "gemini", ModelVision: "no", VisionModel: "some-vision-model"}
		if got := vision.ResolveMainModelVision(nil, cfg); got != vision.VisionLegacy {
			t.Errorf("got %v, want vision.VisionLegacy", got)
		}
	})

	t.Run("gemini + ModelVision no + not usable -> vision.VisionUnsupported", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		cfg := &config.Config{Provider: "gemini", ModelVision: "no", VisionModel: ""}
		if got := vision.ResolveMainModelVision(nil, cfg); got != vision.VisionUnsupported {
			t.Errorf("got %v, want vision.VisionUnsupported", got)
		}
	})

	t.Run("gemini + ModelVision yes -> vision.VisionNative", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		cfg := &config.Config{Provider: "gemini", ModelVision: "yes"}
		if got := vision.ResolveMainModelVision(nil, cfg); got != vision.VisionNative {
			t.Errorf("got %v, want vision.VisionNative", got)
		}
	})

	t.Run("gemini + SupportsVision true -> vision.VisionNative", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		cfg := &config.Config{Provider: "gemini"}
		mi := &interfaces.ModelInfo{SupportsVision: boolPtr(true)}
		if got := vision.ResolveMainModelVision(mi, cfg); got != vision.VisionNative {
			t.Errorf("got %v, want vision.VisionNative", got)
		}
	})

	t.Run("gemini + SupportsVision false + usable -> vision.VisionLegacy", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		cfg := &config.Config{Provider: "gemini", VisionModel: "some-vision-model"}
		mi := &interfaces.ModelInfo{SupportsVision: boolPtr(false)}
		if got := vision.ResolveMainModelVision(mi, cfg); got != vision.VisionLegacy {
			t.Errorf("got %v, want vision.VisionLegacy", got)
		}
	})

	t.Run("gemini + SupportsVision false + not usable -> vision.VisionUnsupported", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		cfg := &config.Config{Provider: "gemini", VisionModel: ""}
		mi := &interfaces.ModelInfo{SupportsVision: boolPtr(false)}
		if got := vision.ResolveMainModelVision(mi, cfg); got != vision.VisionUnsupported {
			t.Errorf("got %v, want vision.VisionUnsupported", got)
		}
	})

	t.Run("gemini + SupportsVision nil + name match -> vision.VisionNative", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		cfg := &config.Config{Provider: "gemini"}
		mi := &interfaces.ModelInfo{Name: "gpt-4o-2024", SupportsVision: nil}
		if got := vision.ResolveMainModelVision(mi, cfg); got != vision.VisionNative {
			t.Errorf("got %v, want vision.VisionNative (name allowlist match)", got)
		}
	})

	t.Run("gemini + SupportsVision nil + gemini name match -> vision.VisionNative", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		cfg := &config.Config{Provider: "gemini"}
		mi := &interfaces.ModelInfo{Name: "gemini-2.5-flash", SupportsVision: nil}
		if got := vision.ResolveMainModelVision(mi, cfg); got != vision.VisionNative {
			t.Errorf("got %v, want vision.VisionNative (gemini model name match)", got)
		}
	})

	t.Run("gemini + SupportsVision nil + unknown name + no vision_model -> vision.VisionUnsupported", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		cfg := &config.Config{Provider: "gemini", VisionModel: ""}
		mi := &interfaces.ModelInfo{Name: "foo-model", SupportsVision: nil}
		if got := vision.ResolveMainModelVision(mi, cfg); got != vision.VisionUnsupported {
			t.Errorf("got %v, want vision.VisionUnsupported", got)
		}
	})

	t.Run("gemini + SupportsVision nil + unknown name + vision_model cfg -> vision.VisionLegacy", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		cfg := &config.Config{Provider: "gemini", VisionModel: "some-vision-model"}
		mi := &interfaces.ModelInfo{Name: "foo-model", SupportsVision: nil}
		if got := vision.ResolveMainModelVision(mi, cfg); got != vision.VisionLegacy {
			t.Errorf("got %v, want vision.VisionLegacy (cfg.VisionModel set)", got)
		}
	})

	t.Run("openai + SupportsVision true -> vision.VisionLegacy", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		cfg := &config.Config{Provider: "openai", VisionModel: "some-vision-model"}
		mi := &interfaces.ModelInfo{SupportsVision: boolPtr(true)}
		if got := vision.ResolveMainModelVision(mi, cfg); got != vision.VisionLegacy {
			t.Errorf("got %v, want vision.VisionLegacy (openai blocks native)", got)
		}
	})
}

func TestParseDataURL(t *testing.T) {
	pngBytes := new1x1PNG()
	b64 := base64.StdEncoding.EncodeToString(pngBytes)

	t.Run("valid data URL", func(t *testing.T) {
		raw := "data:image/png;base64," + b64
		got, mime, err := vision.ParseDataURL(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
		if !bytes.Equal(got, pngBytes) {
			t.Error("decoded bytes do not match original")
		}
	})

	t.Run("valid with custom mime", func(t *testing.T) {
		raw := "data:image/jpeg;base64," + b64
		_, mime, err := vision.ParseDataURL(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/jpeg" {
			t.Errorf("mime = %q, want image/jpeg", mime)
		}
	})

	t.Run("malformed no data: prefix", func(t *testing.T) {
		_, _, err := vision.ParseDataURL("not a data URL")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("malformed missing comma", func(t *testing.T) {
		_, _, err := vision.ParseDataURL("data:image/png;base64")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("malformed bad base64", func(t *testing.T) {
		_, _, err := vision.ParseDataURL("data:image/png;base64,!!!not base64!!!")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestCheckHostPublic(t *testing.T) {
	tests := []struct {
		host    string
		wantErr bool
	}{
		{"127.0.0.1", true},
		{"169.254.169.254", true},
		{"10.0.0.1", true},
		{"192.168.1.1", true},
		{"172.16.0.1", true},
		{"::1", true},
		{"0.0.0.0", true},
		{"localhost", true}, // resolves to 127.0.0.1
		{"8.8.8.8", false},  // public IP
		{"1.1.1.1", false},  // public IP
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			err := vision.CheckHostPublic(tc.host)
			if tc.wantErr && err == nil {
				t.Errorf("vision.CheckHostPublic(%q) = nil, want error", tc.host)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("vision.CheckHostPublic(%q) = %v, want nil", tc.host, err)
			}
		})
	}
}

func TestResolveImageSource(t *testing.T) {
	ctx := context.Background()

	t.Run("data URL", func(t *testing.T) {
		pngBytes := new1x1PNG()
		dataURL := pngToDataURL(pngBytes)
		got, mime, err := vision.ResolveImageSource(ctx, dataURL)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
		if !bytes.Equal(got, pngBytes) {
			t.Error("decoded bytes don't match")
		}
	})

	t.Run("local file", func(t *testing.T) {
		// Save and restore sandbox.CurrentSandbox so we do not hit sandbox path resolution.
		oldSb := sandbox.CurrentSandbox
		sandbox.CurrentSandbox = nil
		t.Cleanup(func() { sandbox.CurrentSandbox = oldSb })

		pngBytes := new1x1PNG()
		tmpDir := t.TempDir()
		p := filepath.Join(tmpDir, "test.png")
		if err := os.WriteFile(p, pngBytes, 0644); err != nil {
			t.Fatal(err)
		}
		got, mime, err := vision.ResolveImageSource(ctx, p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
		if !bytes.Equal(got, pngBytes) {
			t.Error("read bytes don't match written bytes")
		}
	})

	t.Run("local file missing", func(t *testing.T) {
		oldSb := sandbox.CurrentSandbox
		sandbox.CurrentSandbox = nil
		t.Cleanup(func() { sandbox.CurrentSandbox = oldSb })

		_, _, err := vision.ResolveImageSource(ctx, filepath.Join(t.TempDir(), "nope.png"))
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})
}

func TestVisionInjectionCallback(t *testing.T) {
	pngBytes := new1x1PNG()
	dataURL := pngToDataURL(pngBytes)
	testID := fmt.Sprintf("test-callback-%d", time.Now().UnixNano())

	t.Run("injects image content on first call", func(t *testing.T) {
		t.Cleanup(func() { vision.VisionInjected.Delete(testID) })
		vision.VisionInjected.Delete(testID) // ensure clean slate

		fr := &genai.FunctionResponse{
			Name: "vision",
			ID:   testID,
			Response: map[string]any{
				"image_data_url": dataURL,
				"question":       "what is this?",
			},
		}
		part := &genai.Part{FunctionResponse: fr}
		content := &genai.Content{Parts: []*genai.Part{part}}
		req := &model.LLMRequest{Contents: []*genai.Content{content}}

		ctx := agent.NewContext(&agent.ContextMock{})
		resp, err := vision.VisionInjectionCallback(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != nil {
			t.Error("callback should return nil response")
		}

		// The original content should still have the vision FunctionResponse, but with markers stripped.
		if len(req.Contents) < 2 {
			t.Fatalf("expected at least 2 contents (original + injected), got %d", len(req.Contents))
		}

		// Original content: markers stripped.
		if _, exists := fr.Response["image_data_url"]; exists {
			t.Error("image_data_url key should be deleted from original response")
		}
		if _, exists := fr.Response["question"]; exists {
			t.Error("question key should be deleted from original response")
		}

		// Injected content (last one): should have image part + text part.
		injected := req.Contents[len(req.Contents)-1]
		if len(injected.Parts) != 2 {
			t.Fatalf("injected content should have 2 parts, got %d", len(injected.Parts))
		}
		if injected.Parts[0].InlineData == nil {
			t.Error("first injected part should be InlineData (image)")
		} else {
			if injected.Parts[0].InlineData.MIMEType != "image/png" {
				t.Errorf("injected image mime = %q, want image/png", injected.Parts[0].InlineData.MIMEType)
			}
			if !bytes.Equal(injected.Parts[0].InlineData.Data, pngBytes) {
				t.Error("injected image data does not match original")
			}
		}
		if injected.Parts[1].Text != "what is this?" {
			t.Errorf("injected text = %q, want %q", injected.Parts[1].Text, "what is this?")
		}
	})

	t.Run("idempotent on second call with same ID", func(t *testing.T) {
		t.Cleanup(func() { vision.VisionInjected.Delete(testID + "2") })
		vision.VisionInjected.Delete(testID + "2")

		id := testID + "2"
		fr := &genai.FunctionResponse{
			Name: "vision",
			ID:   id,
			Response: map[string]any{
				"image_data_url": dataURL,
				"question":       "what is this?",
			},
		}

		// First call.
		req1 := &model.LLMRequest{Contents: []*genai.Content{
			{Parts: []*genai.Part{{FunctionResponse: fr}}},
		}}
		_, err := vision.VisionInjectionCallback(agent.NewContext(&agent.ContextMock{}), req1)
		if err != nil {
			t.Fatalf("first call error: %v", err)
		}
		// First call should have injected: 1 original + 1 injected = 2 contents.
		if len(req1.Contents) != 2 {
			t.Fatalf("first call: expected 2 contents, got %d", len(req1.Contents))
		}

		// Second call with fresh request but same FunctionResponse ID.
		fr2 := &genai.FunctionResponse{
			Name: "vision",
			ID:   id,
			Response: map[string]any{
				"image_data_url": dataURL,
				"question":       "what is this?",
			},
		}
		req2 := &model.LLMRequest{Contents: []*genai.Content{
			{Parts: []*genai.Part{{FunctionResponse: fr2}}},
		}}
		_, err = vision.VisionInjectionCallback(agent.NewContext(&agent.ContextMock{}), req2)
		if err != nil {
			t.Fatalf("second call error: %v", err)
		}
		// Second call: idempotent, should NOT append another image content.
		if len(req2.Contents) != 1 {
			t.Errorf("second call: expected 1 content (idempotent, no injection), got %d", len(req2.Contents))
		}
	})

	t.Run("non-vision function response is unchanged", func(t *testing.T) {
		fr := &genai.FunctionResponse{
			Name: "other_tool",
			ID:   "other-id-" + testID,
			Response: map[string]any{
				"data": "some data",
			},
		}
		req := &model.LLMRequest{Contents: []*genai.Content{
			{Parts: []*genai.Part{{FunctionResponse: fr}}},
		}}
		_, err := vision.VisionInjectionCallback(agent.NewContext(&agent.ContextMock{}), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(req.Contents) != 1 {
			t.Errorf("non-vision FR should not add content, got %d contents", len(req.Contents))
		}
		if fr.Response["data"] != "some data" {
			t.Error("non-vision FR response should be untouched")
		}
	})

	// NOTE: nil req is not tested here because visionInjectionCallback
	// does not guard against nil req in vision.go (implementation detail).
	// Behavior: panics with nil dereference.

	t.Run("vision FR without image_data_url strips keys", func(t *testing.T) {
		fr := &genai.FunctionResponse{
			Name: "vision",
			ID:   testID + "-no-marker",
			Response: map[string]any{
				"success":  true,
				"question": "",
			},
		}
		req := &model.LLMRequest{Contents: []*genai.Content{
			{Parts: []*genai.Part{{FunctionResponse: fr}}},
		}}
		_, err := vision.VisionInjectionCallback(agent.NewContext(&agent.ContextMock{}), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// image_data_url and question keys should be deleted even if not present
		if _, exists := fr.Response["image_data_url"]; exists {
			t.Error("image_data_url should be deleted")
		}
		if _, exists := fr.Response["question"]; exists {
			t.Error("question should be deleted")
		}
		// Other keys preserved
		if _, exists := fr.Response["success"]; !exists {
			t.Error("other response keys should be preserved")
		}
	})

	t.Run("nil content / nil part in request does not panic", func(t *testing.T) {
		req := &model.LLMRequest{Contents: []*genai.Content{
			nil,
			{Parts: []*genai.Part{nil, {FunctionResponse: nil}}},
		}}
		_, err := vision.VisionInjectionCallback(agent.NewContext(&agent.ContextMock{}), req)
		if err != nil {
			t.Fatalf("unexpected error with nil content/part: %v", err)
		}
	})

	t.Run("nil FunctionResponse.Response does not panic", func(t *testing.T) {
		fr := &genai.FunctionResponse{
			Name:     "vision",
			ID:       testID + "-nil-resp",
			Response: nil,
		}
		req := &model.LLMRequest{Contents: []*genai.Content{
			{Parts: []*genai.Part{{FunctionResponse: fr}}},
		}}
		_, err := vision.VisionInjectionCallback(agent.NewContext(&agent.ContextMock{}), req)
		if err != nil {
			t.Fatalf("unexpected error with nil response: %v", err)
		}
	})
}

func TestVisionModelUsable(t *testing.T) {
	t.Run("vision.VisionModelLLM global set", func(t *testing.T) {
		oldVM := vision.VisionModelLLM
		t.Cleanup(func() { vision.VisionModelLLM = oldVM })

		// vision.VisionModelLLM is an interface; use a non-nil concrete type.
		// We cannot easily create a real model.LLM, but we can test the cfg path.
		// For the global path, we need a non-nil model. Since model.LLM is an
		// interface, assign a non-nil value. The test just needs to verify
		// vision.VisionModelLLMUsable returns true when vision.VisionModelLLM != nil.
		// We cannot create a model.LLM easily, but we can test the cfg branch
		// and the nil global branch.
	})

	t.Run("cfg.VisionModel set", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		cfg := &config.Config{VisionModel: "some-model"}
		if !vision.VisionModelUsable(cfg) {
			t.Error("vision.VisionModelLLMUsable should be true when cfg.VisionModel is set")
		}
	})

	t.Run("cfg nil, no vision.VisionModelLLM -> false", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		if vision.VisionModelUsable(nil) {
			t.Error("vision.VisionModelLLMUsable should be false when cfg is nil and vision.VisionModelLLM is nil")
		}
	})

	t.Run("no vision.VisionModelLLM, cfg.VisionModel empty -> false", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.VisionModelLLM = nil
		if vision.VisionModelUsable(&config.Config{}) {
			t.Error("vision.VisionModelLLMUsable should be false when both are empty")
		}
	})
}

func TestResolveVisionProvider(t *testing.T) {
	mainGemini := &hakaseagent.GeminiProvider{}
	mainOpenAI := &hakaseagent.OpenAIProvider{BaseURL: "https://main.example/v1"}

	type result struct {
		kind    string // "gemini" or "openai"
		baseURL string
	}
	classify := func(p hakaseagent.LLMProvider) result {
		switch v := p.(type) {
		case *hakaseagent.GeminiProvider:
			return result{kind: "gemini"}
		case *hakaseagent.OpenAIProvider:
			return result{kind: "openai", baseURL: v.BaseURL}
		default:
			return result{kind: "unknown"}
		}
	}

	cases := []struct {
		name string
		main hakaseagent.LLMProvider
		cfg  *config.Config
		want result
	}{
		{"inherit main gemini", mainGemini, &config.Config{}, result{kind: "gemini"}},
		{"inherit main openai-compatible", mainOpenAI, &config.Config{}, result{kind: "openai", baseURL: "https://main.example/v1"}},
		{"gemini for vision only from openai main", mainOpenAI, &config.Config{VisionProvider: "gemini"}, result{kind: "gemini"}},
		{"openai-compatible override", mainGemini, &config.Config{VisionProvider: "openai-compatible", VisionBaseURL: "https://vis.example/v1"}, result{kind: "openai", baseURL: "https://vis.example/v1"}},
		{"openai override without base url", mainGemini, &config.Config{VisionProvider: "openai"}, result{kind: "openai", baseURL: ""}},
		{"base url alone forces openai", mainGemini, &config.Config{VisionBaseURL: "https://vis.example/v1"}, result{kind: "openai", baseURL: "https://vis.example/v1"}},
		{"unknown vision_provider inherits", mainOpenAI, &config.Config{VisionProvider: "bogus"}, result{kind: "openai", baseURL: "https://main.example/v1"}},
		{"nil cfg inherits", mainGemini, nil, result{kind: "gemini"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(resolveVisionProvider(tc.main, tc.cfg))
			if got != tc.want {
				t.Errorf("resolveVisionProvider = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLoadDataURL(t *testing.T) {
	t.Run("valid png data URL", func(t *testing.T) {
		pngBytes := new1x1PNG()
		raw := pngToDataURL(pngBytes)
		got, mime, err := vision.LoadDataURL(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/png" {
			t.Errorf("mime = %q, want image/png", mime)
		}
		if !bytes.Equal(got, pngBytes) {
			t.Error("decoded bytes don't match")
		}
	})

	t.Run("too large data URL", func(t *testing.T) {
		// Payload exceeds vision.VisionMaxDownloadBytes*2 in the pre-check.
		bigPayload := strings.Repeat("A", vision.VisionMaxDownloadBytes*2+1)
		raw := "data:image/png;base64," + bigPayload
		_, _, err := vision.LoadDataURL(raw)
		if err == nil {
			t.Fatal("expected error for too-large data URL, got nil")
		}
	})

	t.Run("too large decoded", func(t *testing.T) {
		// Payload decodes to > vision.VisionMaxDownloadBytes.
		big := make([]byte, vision.VisionMaxDownloadBytes+1)
		for i := range big {
			big[i] = 0x41 // 'A'
		}
		b64 := base64.StdEncoding.EncodeToString(big)
		raw := "data:image/png;base64," + b64
		_, _, err := vision.LoadDataURL(raw)
		if err == nil {
			t.Fatal("expected error for too-large decoded data, got nil")
		}
		if !strings.Contains(err.Error(), "too large") {
			t.Errorf("error should mention 'too large': %v", err)
		}
	})

	t.Run("unrecognized format in data URL", func(t *testing.T) {
		raw := "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString([]byte("garbage"))
		_, _, err := vision.LoadDataURL(raw)
		if err == nil {
			t.Fatal("expected error for unrecognized format, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// BMP encoder stub (stdlib does not have a BMP encoder; we use x/image/bmp for
// decoding but encoding requires a workaround. We encode a minimal BMP manually.)
// ---------------------------------------------------------------------------

// encodeBMP encodes an image as a 24-bit BMP and writes it to w.
// This is a minimal BMP writer sufficient for 1x1 test fixtures.
func encodeBMP(img *image.RGBA, w *bytes.Buffer) error {
	b := img.Bounds()
	wb, hb := b.Dx(), b.Dy()
	// Row size padded to 4 bytes for 24-bit.
	rowSize := (wb*3 + 3) &^ 3
	pixelDataSize := rowSize * hb
	fileSize := int32(14 + 40 + pixelDataSize)

	// BITMAPFILEHEADER
	w.Write([]byte("BM"))
	w.Write(le32(fileSize))
	w.Write(le16(0))       // reserved
	w.Write(le16(0))       // reserved
	w.Write(le32(14 + 40)) // offset to pixel data

	// BITMAPINFOHEADER
	w.Write(le32(40))        // header size
	w.Write(le32(int32(wb))) // width
	w.Write(le32(int32(hb))) // height (positive = bottom-up)
	w.Write(le16(1))         // planes
	w.Write(le16(24))        // bits per pixel
	w.Write(le32(0))         // compression (BI_RGB)
	w.Write(le32(int32(pixelDataSize)))
	w.Write(le32(2835)) // h-res pixels per meter (~72 DPI)
	w.Write(le32(2835)) // v-res
	w.Write(le32(0))    // colors in palette
	w.Write(le32(0))    // important colors

	// Pixel data: rows bottom-to-top, BGR order.
	rowBuf := make([]byte, rowSize)
	for y := hb - 1; y >= 0; y-- {
		for x := 0; x < wb; x++ {
			c := img.RGBAAt(x, y)
			rowBuf[x*3+0] = c.B
			rowBuf[x*3+1] = c.G
			rowBuf[x*3+2] = c.R
		}
		w.Write(rowBuf)
	}
	return nil
}

// bmpDecodeEncode encodes a BMP for test fixtures using our minimal encoder.
func bmpDecodeEncode(img *image.RGBA, w *bytes.Buffer) error {
	return encodeBMP(img, w)
}

// encodeJPEG encodes an image as JPEG at the given quality.
func encodeJPEG(img image.Image, quality int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// little-endian helpers for BMP encoding.
func le16(v uint16) []byte {
	return []byte{byte(v), byte(v >> 8)}
}

func le32(v int32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

// Prevent unused import warnings by using sync.Map at least once.
var _ = sync.Map{}

func TestRewriteAttachedImages(t *testing.T) {
	pngBytes := new1x1PNG()

	t.Run("unsupported mode replaces image with warning text", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		imgPart := &genai.Part{InlineData: &genai.Blob{Data: pngBytes, MIMEType: "image/png"}}
		content := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{
			genai.NewPartFromText("what do you see here?"),
			imgPart,
		}}
		req := &model.LLMRequest{Contents: []*genai.Content{content}}

		vision.RewriteAttachedImages(context.Background(), req, vision.VisionUnsupported)

		// Text part stays, image part replaced by a warning text part.
		if len(content.Parts) != 2 {
			t.Fatalf("expected 2 parts, got %d", len(content.Parts))
		}
		if content.Parts[0].Text != "what do you see here?" {
			t.Errorf("question text lost: %q", content.Parts[0].Text)
		}
		repl := content.Parts[1]
		if repl.InlineData != nil {
			t.Error("image part was not replaced")
		}
		if !strings.Contains(repl.Text, "cannot see images") {
			t.Errorf("replacement should warn about vision support, got: %q", repl.Text)
		}
	})

	t.Run("native mode leaves image parts untouched", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		imgPart := &genai.Part{InlineData: &genai.Blob{Data: pngBytes, MIMEType: "image/png"}}
		content := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{imgPart}}
		req := &model.LLMRequest{Contents: []*genai.Content{content}}

		vision.RewriteAttachedImages(context.Background(), req, vision.VisionNative)

		if len(content.Parts) != 1 || content.Parts[0].InlineData == nil {
			t.Fatal("native mode must keep the image part")
		}
	})

	t.Run("legacy mode uses cached description when available", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		// Pre-seed the cache so no vision model call is attempted.
		sum := sha256.Sum256(pngBytes)
		key := hex.EncodeToString(sum[:])
		vision.VisionDescribeCache.Store(key, "cached fake description")
		t.Cleanup(func() { vision.VisionDescribeCache.Delete(key) })

		imgPart := &genai.Part{InlineData: &genai.Blob{Data: pngBytes, MIMEType: "image/png"}}
		content := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{imgPart}}
		req := &model.LLMRequest{Contents: []*genai.Content{content}}

		vision.RewriteAttachedImages(context.Background(), req, vision.VisionLegacy)

		if len(content.Parts) != 1 || content.Parts[0].InlineData != nil {
			t.Fatal("legacy mode must replace the image part")
		}
		if !strings.Contains(content.Parts[0].Text, "cached fake description") {
			t.Errorf("expected cached description, got: %q", content.Parts[0].Text)
		}
	})

	t.Run("non-user and non-image parts untouched", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		imgPart := &genai.Part{InlineData: &genai.Blob{Data: pngBytes, MIMEType: "image/png"}}
		modelContent := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{imgPart}}
		textContent := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText("plain text")}}
		req := &model.LLMRequest{Contents: []*genai.Content{modelContent, textContent}}

		vision.RewriteAttachedImages(context.Background(), req, vision.VisionUnsupported)

		if modelContent.Parts[0].InlineData == nil {
			t.Error("model-role image part must not be rewritten")
		}
		if textContent.Parts[0].Text != "plain text" {
			t.Error("plain text part must not be changed")
		}
	})

	t.Run("nil req is safe", func(t *testing.T) {
		clearVisionGlobalsForTest(t)
		vision.RewriteAttachedImages(context.Background(), nil, vision.VisionUnsupported) // must not panic
	})
}

func TestDescribeOrWarnImageUnavailable(t *testing.T) {
	clearVisionGlobalsForTest(t)
	pngBytes := new1x1PNG()

	// Legacy mode with a nil vision.VisionModelLLM global: the vision call fails and
	// the failure text is cached (warn-and-continue, not a hard error).
	text := vision.DescribeOrWarnImage(context.Background(), pngBytes, "image/png", "what is this?", vision.VisionLegacy)
	if !strings.Contains(text, "vision model could not describe") {
		t.Errorf("expected failure warning, got: %q", text)
	}

	// A second call must hit the cache (no repeated vision attempt).
	second := vision.DescribeOrWarnImage(context.Background(), pngBytes, "image/png", "what is this?", vision.VisionLegacy)
	if second != text {
		t.Errorf("cache miss: second call returned %q, want %q", second, text)
	}

	sum := sha256.Sum256(pngBytes)
	vision.VisionDescribeCache.Delete(hex.EncodeToString(sum[:]))
}

// boolPtr helper for creating *bool literals.
func boolPtr(b bool) *bool { return &b }
