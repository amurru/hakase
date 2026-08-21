package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"amurru/hakase/internal/config"
)

// openAIProvider implements Provider for OpenAI Images (plain net/http).
type openAIProvider struct {
	cfg   config.MediaConfig
	fallbackAPIKey string
	fallbackBaseURL string
	store *Store
	log   LogFunc
	client *http.Client
}

// NewOpenAIProvider creates the provider. The global APIKey/BaseURL fallbacks are passed via config.MediaConfig?
// We capture fallback from cfg if needed; main.go should have copied APIKey/BaseURL into MediaConfig before registry creation.
func NewOpenAIProvider(cfg config.MediaConfig, log LogFunc, store *Store) Provider {
	// Fallbacks: if OpenAIImageKey empty, caller should have filled from global APIKey. Keep as-is.
	// Similarly for baseURL.
	return &openAIProvider{
		cfg:    cfg,
		store:  store,
		log:    log,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *openAIProvider) Name() string { return "openai" }

func (p *openAIProvider) Capabilities() Capabilities {
	return Capabilities{Image: true}
}

func (p *openAIProvider) GenerateImage(ctx context.Context, req ImageRequest) (*MediaResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	w, h := req.ClampedSize()
	// Per-model size clamping (MG-006)
	model := p.cfg.OpenAIImageModel
	if req.Model != "" {
		model = req.Model
	}
	if model == "" {
		model = "gpt-image-1-mini"
	}
	w, h = clampSizeForModel(model, w, h)

	baseURL := p.cfg.OpenAIImageBaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	path := p.cfg.OpenAIImagePath
	if path == "" {
		path = "/images/generations"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	endpoint := baseURL + path

	key := p.cfg.OpenAIImageKey
	if key == "" {
		return nil, fmt.Errorf("openai image auth failed: check api_key / openai_image_key (401)")
	}

	// Build request body.
	bodyMap := map[string]interface{}{
		"model":  model,
		"prompt": req.Prompt,
		"n":      1,
		"size":   fmt.Sprintf("%dx%d", w, h),
	}
	if strings.HasPrefix(model, "dall-e-") {
		bodyMap["response_format"] = "b64_json"
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("openai image auth failed: check api_key / openai_image_key (401)")
	}
	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("images endpoint not found at %s: for OpenRouter set openai_image_path to \"/images\" (404)", endpoint)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openai image generation failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
		Created int64 `json:"created"`
	}
	b, _ := io.ReadAll(resp.Body)
	// resp.Body already read partially? We already read b after? Need to handle: we already consumed? Actually we did io.ReadAll after status check, but we already read for error? Wait we read after success: need to parse.
	// Since we did io.ReadAll for error case already, but for success we need to parse b.
	// However we already did io.ReadAll unconditionally after? Let's parse b.
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	if len(out.Data) == 0 || out.Data[0].B64JSON == "" {
		return nil, fmt.Errorf("openai response missing b64_json")
	}
	decoded, err := base64.StdEncoding.DecodeString(out.Data[0].B64JSON)
	if err != nil {
		return nil, fmt.Errorf("decode b64: %w", err)
	}
	pathOut, err := p.store.Allocate(".png")
	if err != nil {
		return nil, err
	}
	if err := p.store.Write(pathOut, bytes.NewReader(decoded), 20<<20); err != nil {
		return nil, err
	}
	// Report the workspace-relative path so the web UI mediaLinks plugin
	// rewrites it to /api/files/inline (absolute paths leak the host FS into
	// the chat and 404 against the page origin).
	relPath := p.store.WorkspaceRelPath(pathOut)
	return &MediaResult{
		Path:     relPath,
		Provider: "openai",
		Model:    model,
		Width:    w,
		Height:   h,
		MimeType: "image/png",
		Markdown: fmt.Sprintf("![generated](%s)", relPath),
		Seed:     req.Seed,
	}, nil
}

func (p *openAIProvider) GenerateVideo(ctx context.Context, req VideoRequest) (*MediaResult, error) {
	return nil, fmt.Errorf("provider openai does not support video")
}

func (p *openAIProvider) GenerateAudio(ctx context.Context, req AudioRequest) (*MediaResult, error) {
	return nil, fmt.Errorf("provider openai does not support audio")
}

func clampSizeForModel(model string, w, h int) (int, int) {
	// Per spec MG-006 table.
	if strings.HasPrefix(model, "gpt-image-2") {
		// Arbitrary WxH divisible by 16, aspect 1:3..3:1, <=3840x2160; plus trio.
		// Snap to nearest legal: ensure divisible by 16, clamp dimensions, adjust aspect.
		w = (w / 16) * 16
		h = (h / 16) * 16
		if w < 16 {
			w = 16
		}
		if h < 16 {
			h = 16
		}
		if w > 3840 {
			w = 3840
		}
		if h > 2160 {
			h = 2160
		}
		// Enforce aspect ratio 1:3..3:1
		if w > 0 && h > 0 {
			ratio := float64(w) / float64(h)
			if ratio > 3 {
				w = h * 3
				w = (w / 16) * 16
			} else if ratio < 1.0/3 {
				h = w * 3
				h = (h / 16) * 16
			}
		}
		// If still small, snap to nearest trio? For simplicity keep arbitrary.
		return w, h
	}
	if strings.HasPrefix(model, "gpt-image-1") {
		// trio: 1024x1024, 1536x1024, 1024x1536
		return nearestTrio(w, h, [][2]int{{1024, 1024}, {1536, 1024}, {1024, 1536}})
	}
	if model == "dall-e-3" {
		return nearestTrio(w, h, [][2]int{{1024, 1024}, {1792, 1024}, {1024, 1792}})
	}
	// Custom slug: pass through
	return w, h
}

func nearestTrio(w, h int, options [][2]int) (int, int) {
	best := options[0]
	bestDist := abs(w-best[0]) + abs(h-best[1])
	for _, opt := range options[1:] {
		d := abs(w-opt[0]) + abs(h-opt[1])
		if d < bestDist {
			bestDist = d
			best = opt
		}
	}
	return best[0], best[1]
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
