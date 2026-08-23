package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ManifestMu guards manifest appends.
var manifestMu sync.Mutex

// GenerateImageInput is the ADK input for generate_image.
type GenerateImageInput struct {
	Prompt         string `json:"prompt" doc:"Text prompt for image generation (1-4000 chars, required)"`
	NegativePrompt string `json:"negative_prompt,omitempty" doc:"Negative prompt to avoid (optional)"`
	Width          int    `json:"width,omitempty" doc:"Image width in pixels (256-2048, default 1024)"`
	Height         int    `json:"height,omitempty" doc:"Image height in pixels (256-2048, default 1024)"`
	Steps          int    `json:"steps,omitempty" doc:"Inference steps (1-50, default 20, used by pil and some fal models)"`
	Seed           *int64 `json:"seed,omitempty" doc:"Random seed for deterministic generation (optional)"`
	Provider       string `json:"provider,omitempty" doc:"Provider override: auto (default), pil, openai, fal, off"`
	Model          string `json:"model,omitempty" doc:"Model override (optional, passed through)"`
}

// GenerateImageOutput is the result.
type GenerateImageOutput struct {
	Path     string `json:"path" doc:"Workspace-relative path to the generated image"`
	Provider string `json:"provider" doc:"Provider that generated the image"`
	Model    string `json:"model" doc:"Model used"`
	Seed     *int64 `json:"seed,omitempty" doc:"Seed used"`
	Width    int    `json:"width" doc:"Image width"`
	Height   int    `json:"height" doc:"Image height"`
	MimeType string `json:"mime_type" doc:"MIME type, e.g. image/png"`
	Markdown string `json:"markdown" doc:"Markdown snippet to render the image inline: ![generated](path)"`
}

// GenerateVideoInput is the ADK input for generate_video.
type GenerateVideoInput struct {
	Prompt          string `json:"prompt" doc:"Text prompt describing the motion/content of the video (required)"`
	DurationSeconds int    `json:"duration_seconds,omitempty" doc:"Duration in seconds (2-10, default 5). Must match a duration the chosen model supports (e.g. veo 4/6/8, wan 5/10); on mismatch retry with a supported value from the error."`
	Width           int    `json:"width,omitempty" doc:"Video width (optional, used to derive aspect ratio)"`
	Height          int    `json:"height,omitempty" doc:"Video height (optional, used to derive aspect ratio)"`
	Image           string `json:"image,omitempty" doc:"Optional image to anchor generation as the first frame (image-to-video): workspace path of a previously generated image, http(s) URL, or data: URL"`
	Provider        string `json:"provider,omitempty" doc:"Provider override: auto (default), openai (OpenAI-compatible router such as OpenRouter), fal, off"`
	Model           string `json:"model,omitempty" doc:"Model override (e.g. google/veo-3.1-lite, bytedance/seedance-1-5-pro, alibaba/wan-2.7)"`
	Seed            *int64 `json:"seed,omitempty" doc:"Random seed (optional)"`
}

// GenerateVideoOutput is the result.
type GenerateVideoOutput struct {
	Path     string `json:"path" doc:"Workspace-relative path to the generated video"`
	Provider string `json:"provider" doc:"Provider that generated the video"`
	Model    string `json:"model" doc:"Model used"`
	Seed     *int64 `json:"seed,omitempty" doc:"Seed used"`
	Width    int    `json:"width,omitempty" doc:"Width"`
	Height   int    `json:"height,omitempty" doc:"Height"`
	MimeType string `json:"mime_type" doc:"MIME type, e.g. video/mp4"`
	Markdown string `json:"markdown" doc:"Markdown snippet for the video"`
}

// GenerateAudioInput is the ADK input for generate_audio (stub).
type GenerateAudioInput struct {
	Text     string `json:"text" doc:"Text to synthesize to audio (required)"`
	Voice    string `json:"voice,omitempty" doc:"Voice name (optional)"`
	Provider string `json:"provider,omitempty" doc:"Provider override: off (default)"`
	Model    string `json:"model,omitempty" doc:"Model override"`
}

// GenerateAudioOutput is stub output.
type GenerateAudioOutput struct {
	Path     string `json:"path,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Markdown string `json:"markdown,omitempty"`
}

// manifestEntry is one line in outputs/media/manifest.jsonl
type manifestEntry struct {
	TS         string `json:"ts"`
	Tool       string `json:"tool"`
	Prompt     string `json:"prompt"`
	Provider   string `json:"provider"`
	Path       string `json:"path"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Model      string `json:"model,omitempty"`
}

// CreateMediaTools returns the three media tools.
func CreateMediaTools(reg *Registry, log LogFunc) ([]tool.Tool, error) {
	if reg == nil {
		return nil, fmt.Errorf("media registry is nil")
	}
	var tools []tool.Tool

	// generate_image
	imgTool, err := util.NewDocTool(functiontool.Config{
		Name:        "generate_image",
		Description: "Generate an image from a text prompt. Use for infographics, posters, illustrations. Offline fallback pil always works; cloud providers used when keys present. Returns path and markdown snippet.",
	}, func(ctx agent.Context, input GenerateImageInput) (GenerateImageOutput, error) {
		// Explicit input wins; otherwise default resolution honors the
		// configured image_provider preference before falling back to auto
		// ordering.
		provHint := input.Provider
		if provHint == "" || provHint == "auto" {
			pref := reg.Config().ImageProvider
			if pref != "" && pref != "auto" && pref != "off" {
				provHint = pref
			} else {
				provHint = "auto"
			}
		}
		// Check global image_provider off
		if reg.Config().ImageProvider == "off" && provHint == "auto" {
			return GenerateImageOutput{}, fmt.Errorf("image generation is off: set media.image_provider to auto, pil, openai, or fal")
		}
		if provHint == "off" {
			return GenerateImageOutput{}, fmt.Errorf("image generation is off: set media.image_provider to auto, pil, openai, or fal")
		}
		if strings.TrimSpace(input.Prompt) == "" {
			return GenerateImageOutput{}, fmt.Errorf("prompt is required (1-4000 chars)")
		}
		if len(input.Prompt) > 4000 {
			return GenerateImageOutput{}, fmt.Errorf("prompt is required (1-4000 chars)")
		}
		// Clamp sizes
		w, h := 1024, 1024
		if input.Width != 0 {
			w = input.Width
		}
		if input.Height != 0 {
			h = input.Height
		}
		if w < 256 {
			w = 256
		}
		if w > 2048 {
			w = 2048
		}
		if h < 256 {
			h = 256
		}
		if h > 2048 {
			h = 2048
		}
		// Resolve provider
		provider, err := reg.ResolveForProvider("image", provHint)
		if err != nil {
			return GenerateImageOutput{}, err
		}
		// Timeout per kind
		timeout := 120 * time.Second
		if reg.Config().TimeoutSeconds != 0 {
			timeout = time.Duration(reg.Config().TimeoutSeconds) * time.Second
		}
		ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		// Semaphore
		if err := reg.Acquire(ctxTimeout, provider.Name()); err != nil {
			return GenerateImageOutput{}, err
		}
		defer reg.Release(provider.Name())

		// Build request
		req := ImageRequest{
			Prompt:         input.Prompt,
			NegativePrompt: input.NegativePrompt,
			Width:          w,
			Height:         h,
			Steps:          input.Steps,
			Seed:           input.Seed,
			Provider:       provHint,
			Model:          input.Model,
		}
		start := time.Now()
		result, err := provider.GenerateImage(ctxTimeout, req)
		durationMs := time.Since(start).Milliseconds()
		if err != nil {
			return GenerateImageOutput{}, err
		}
		// Manifest append
		appendManifest(reg.Store(), manifestEntry{
			TS:         time.Now().UTC().Format(time.RFC3339),
			Tool:       "generate_image",
			Prompt:     input.Prompt,
			Provider:   result.Provider,
			Path:       result.Path,
			Width:      result.Width,
			Height:     result.Height,
			DurationMs: durationMs,
			Model:      result.Model,
		})
		return GenerateImageOutput{
			Path:     result.Path,
			Provider: result.Provider,
			Model:    result.Model,
			Seed:     result.Seed,
			Width:    result.Width,
			Height:   result.Height,
			MimeType: result.MimeType,
			Markdown: result.Markdown,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, imgTool)

	// generate_video
	videoTool, err := util.NewDocTool(functiontool.Config{
		Name:        "generate_video",
		Description: "Generate a video from a text prompt, optionally anchored to an input image (image-to-video via first frame). Prefers the OpenAI-compatible router (e.g. OpenRouter) when its key is configured; falls back to fal. Returns path and markdown snippet.",
	}, func(ctx agent.Context, input GenerateVideoInput) (GenerateVideoOutput, error) {
		// Explicit input wins; otherwise default resolution honors the
		// configured video_provider preference before falling back to auto
		// ordering.
		provHint := input.Provider
		if provHint == "" || provHint == "auto" {
			pref := reg.Config().VideoProvider
			if pref != "" && pref != "auto" && pref != "off" {
				provHint = pref
			} else {
				provHint = "auto"
			}
		}
		if reg.Config().VideoProvider == "off" && provHint == "auto" {
			return GenerateVideoOutput{}, errors.New(videoNoProviderMsg)
		}
		if provHint == "off" {
			return GenerateVideoOutput{}, errors.New(videoNoProviderMsg)
		}
		if strings.TrimSpace(input.Prompt) == "" {
			return GenerateVideoOutput{}, fmt.Errorf("prompt is required (1-4000 chars)")
		}
		if len(input.Prompt) > 4000 {
			return GenerateVideoOutput{}, fmt.Errorf("prompt is required (1-4000 chars)")
		}
		// Duration clamp 2-10 default 5
		duration := input.DurationSeconds
		if duration == 0 {
			duration = 5
		}
		if duration < 2 {
			duration = 2
		}
		if duration > 10 {
			duration = 10
		}
		provider, err := reg.ResolveForProvider("video", provHint)
		if err != nil {
			// Ensure verbatim error for no provider
			if provHint == "auto" {
				return GenerateVideoOutput{}, errors.New(videoNoProviderMsg)
			}
			return GenerateVideoOutput{}, err
		}
		timeout := 300 * time.Second
		if reg.Config().TimeoutSeconds != 0 {
			timeout = time.Duration(reg.Config().TimeoutSeconds) * time.Second
		}
		ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := reg.Acquire(ctxTimeout, provider.Name()); err != nil {
			return GenerateVideoOutput{}, err
		}
		defer reg.Release(provider.Name())

		req := VideoRequest{
			Prompt:          input.Prompt,
			DurationSeconds: duration,
			Width:           input.Width,
			Height:          input.Height,
			Provider:        provHint,
			Model:           input.Model,
			Seed:            input.Seed,
			ImageRef:        strings.TrimSpace(input.Image),
		}
		start := time.Now()
		result, err := provider.GenerateVideo(ctxTimeout, req)
		durationMs := time.Since(start).Milliseconds()
		if err != nil {
			return GenerateVideoOutput{}, err
		}
		appendManifest(reg.Store(), manifestEntry{
			TS:         time.Now().UTC().Format(time.RFC3339),
			Tool:       "generate_video",
			Prompt:     input.Prompt,
			Provider:   result.Provider,
			Path:       result.Path,
			Width:      result.Width,
			Height:     result.Height,
			DurationMs: durationMs,
			Model:      result.Model,
		})
		return GenerateVideoOutput{
			Path:     result.Path,
			Provider: result.Provider,
			Model:    result.Model,
			Seed:     result.Seed,
			Width:    result.Width,
			Height:   result.Height,
			MimeType: result.MimeType,
			Markdown: result.Markdown,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, videoTool)

	// generate_audio (stub)
	audioTool, err := util.NewDocTool(functiontool.Config{
		Name:        "generate_audio",
		Description: "Generate audio from text (stub - planned for v2). Returns actionable error.",
	}, func(ctx agent.Context, input GenerateAudioInput) (GenerateAudioOutput, error) {
		cfg := reg.Config()
		// audio_provider off -> stub
		if cfg.AudioProvider == "off" {
			// If provider hint is off or auto with off config, return off message
			hint := input.Provider
			if hint == "" {
				hint = "auto"
			}
			if hint == "auto" || hint == "off" {
				return GenerateAudioOutput{}, fmt.Errorf("audio generation is off: set media.audio_provider to openai once TTS is wired (planned v2)")
			}
			// Any other value -> not wired
			return GenerateAudioOutput{}, fmt.Errorf("audio generation is not wired in this build: openai TTS is planned for v2")
		}
		// Any other audio value in v1 -> not wired
		return GenerateAudioOutput{}, fmt.Errorf("audio generation is not wired in this build: openai TTS is planned for v2")
	})
	if err != nil {
		return nil, err
	}
	tools = append(tools, audioTool)

	return tools, nil
}

func appendManifest(store *Store, e manifestEntry) {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	var path string
	if store != nil {
		path = filepath.Join(store.Root(), "manifest.jsonl")
	} else {
		path = filepath.Join("outputs", "media", "manifest.jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		util.DebugWarn("media_manifest_write_failed", "path", path, "error", err.Error())
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		util.DebugWarn("media_manifest_marshal_failed", "error", err.Error())
		return
	}
	b = append(b, '\n')
	// Append exactly one formatted line per call so a concurrent writer from
	// another process cannot interleave payload and newline.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		util.DebugWarn("media_manifest_open_failed", "path", path, "error", err.Error())
		return
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		util.DebugWarn("media_manifest_write_failed", "path", path, "error", err.Error())
	}
}
