package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/sandbox"
)

// openAIProvider implements Provider for OpenAI Images (plain net/http).
type openAIProvider struct {
	cfg    config.MediaConfig
	store  *Store
	log    LogFunc
	client *http.Client
}

// NewOpenAIProvider creates the provider. Global APIKey/BaseURL fallbacks are
// copied into MediaConfig by the wiring layer before registry construction.
func NewOpenAIProvider(cfg config.MediaConfig, log LogFunc, store *Store) Provider {
	timeout := 120 * time.Second
	if cfg.TimeoutSeconds > 0 {
		// Honor media.timeout_seconds so the documented setting actually
		// governs per-request behavior instead of being silently ignored.
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	return &openAIProvider{
		cfg:    cfg,
		store:  store,
		log:    log,
		client: &http.Client{Timeout: timeout},
	}
}

func (p *openAIProvider) Name() string { return "openai" }

func (p *openAIProvider) Capabilities() Capabilities {
	return Capabilities{Image: true, Video: true}
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
	if err := req.Validate(); err != nil {
		return nil, err
	}
	model := p.cfg.OpenAIVideoModel
	if req.Model != "" {
		model = req.Model
	}
	if model == "" {
		model = "google/veo-3.1-lite"
	}
	key := p.cfg.OpenAIVideoKey
	if key == "" {
		key = p.cfg.OpenAIImageKey
	}
	if key == "" {
		return nil, fmt.Errorf("openai video auth failed: check api_key / openai_video_key / openai_image_key (401)")
	}
	baseURL := p.cfg.OpenAIVideoBaseURL
	if baseURL == "" {
		baseURL = p.cfg.OpenAIImageBaseURL
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	duration := req.DurationSeconds
	if duration == 0 {
		duration = 5
	}
	if duration < 2 {
		duration = 2
	}
	if duration > 10 {
		duration = 10
	}

	payload := map[string]interface{}{
		"model":          model,
		"prompt":         req.Prompt,
		"duration":       duration,
		"generate_audio": false,
	}
	if req.Width > 0 && req.Height > 0 {
		payload["aspect_ratio"] = aspectRatioFromWH(req.Width, req.Height)
	}
	if res := strings.TrimSpace(p.cfg.OpenAIVideoResolution); res != "" && !strings.EqualFold(res, "auto") {
		payload["resolution"] = res
	}
	if req.Seed != nil {
		payload["seed"] = *req.Seed
	}
	frame, err := buildFrameImage(req.ImageRef)
	if err != nil {
		return nil, err
	}
	if frame != nil {
		payload["frame_images"] = []map[string]interface{}{frame}
	}

	bodyBytes, _ := json.Marshal(payload)
	endpoint := baseURL + "/videos"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai video submit failed: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("openai video auth failed: check api_key / openai_video_key (401)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai video submit failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var submit struct {
		ID         string `json:"id"`
		PollingURL string `json:"polling_url"`
		Status     string `json:"status"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(b, &submit); err != nil {
		return nil, fmt.Errorf("decode openai video response: %w", err)
	}
	downloadURL, err := p.pollVideoJob(ctx, baseURL, key, submit.ID, submit.PollingURL)
	if err != nil {
		return nil, err
	}

	// Download via SSRF-guarded client (same pattern as fal).
	parsed, err := parseURL(downloadURL)
	if err != nil {
		return nil, err
	}
	if err := checkHostPublic(parsed.Host); err != nil {
		return nil, fmt.Errorf("openai video result url blocked: %w", err)
	}
	dreq, _ := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	// Attach credentials only when the download host matches the configured
	// API base host. unsigned_urls are API-controlled and may point at
	// third-party hosts (CDNs) that must never receive the bearer key.
	if bu, berr := url.Parse(baseURL); berr == nil && strings.EqualFold(bu.Host, parsed.Host) {
		dreq.Header.Set("Authorization", "Bearer "+key)
	}
	dresp, err := p.client.Do(dreq)
	if err != nil {
		return nil, fmt.Errorf("openai video download failed: %w", err)
	}
	defer dresp.Body.Close()
	if dresp.StatusCode < 200 || dresp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai video download failed: HTTP %d", dresp.StatusCode)
	}
	capBytes := int64(100 << 20)
	allocPath, err := p.store.Allocate(".mp4")
	if err != nil {
		return nil, err
	}
	// Stream straight into the capped store write; buffering up to 100MB in
	// memory adds nothing because store.Write enforces the cap itself.
	if err := p.store.Write(allocPath, io.LimitReader(dresp.Body, capBytes+1), capBytes); err != nil {
		return nil, err
	}
	relPath := p.store.WorkspaceRelPath(allocPath)
	w, h := req.Width, req.Height
	return &MediaResult{
		Path:     relPath,
		Provider: "openai",
		Model:    model,
		MimeType: "video/mp4",
		Markdown: fmt.Sprintf("![generated](%s)", relPath),
		Seed:     req.Seed,
		Width:    w,
		Height:   h,
	}, nil
}

// pollVideoJob polls an OpenRouter/OpenAI-style async video job until it
// completes and returns a download URL. Handles both the OpenRouter shape
// (relative polling_url + unsigned_urls on completion) and the OpenAI shape
// (GET /videos/{id}, download via /videos/{id}/content).
func (p *openAIProvider) pollVideoJob(ctx context.Context, baseURL, key, id, pollingURL string) (string, error) {
	pollURL := resolvePollingURL(baseURL, pollingURL, id)
	if pollURL == "" {
		return "", fmt.Errorf("openai video job has no id or polling_url")
	}
	// The API supplies polling_url; guard it like any other remote URL so a
	// hostile endpoint cannot redirect authenticated polling at internal
	// addresses.
	if pu, perr := parseURL(pollURL); perr != nil {
		return "", fmt.Errorf("openai video poll url invalid: %w", perr)
	} else if err := checkHostPublic(pu.Host); err != nil {
		return "", fmt.Errorf("openai video poll url blocked: %w", err)
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := 300 * time.Second
	if p.cfg.TimeoutSeconds > 0 {
		deadline = time.Duration(p.cfg.TimeoutSeconds) * time.Second
	}
	expiry := time.Now().Add(deadline)
	for {
		if time.Now().After(expiry) {
			return "", fmt.Errorf("openai video poll timeout")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", pollURL, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := p.client.Do(req)
		if err != nil {
			continue // transient poll errors are retried until deadline
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("openai video poll failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var status struct {
			Status       string   `json:"status"`
			Error        string   `json:"error"`
			ID           string   `json:"id"`
			UnsignedURLs []string `json:"unsigned_urls"`
		}
		_ = json.Unmarshal(body, &status)
		switch status.Status {
		case "completed":
			if len(status.UnsignedURLs) > 0 && status.UnsignedURLs[0] != "" {
				return status.UnsignedURLs[0], nil
			}
			jobID := status.ID
			if jobID == "" {
				jobID = id
			}
			if jobID != "" {
				// OpenAI-style content endpoint fallback.
				return baseURL + "/videos/" + jobID + "/content", nil
			}
			return "", fmt.Errorf("openai video completed but no download url")
		case "failed", "cancelled", "expired":
			msg := strings.TrimSpace(status.Error)
			if msg == "" {
				msg = status.Status
			}
			return "", fmt.Errorf("openai video generation failed: %s", msg)
		}
		// pending / in_progress / unknown -> keep polling
	}
}

func resolvePollingURL(baseURL, pollingURL, id string) string {
	if pollingURL != "" {
		if strings.HasPrefix(pollingURL, "http://") || strings.HasPrefix(pollingURL, "https://") {
			return pollingURL
		}
		if u, err := url.Parse(baseURL); err == nil && u.Scheme != "" && u.Host != "" {
			if !strings.HasPrefix(pollingURL, "/") {
				pollingURL = "/" + pollingURL
			}
			return u.Scheme + "://" + u.Host + pollingURL
		}
		return baseURL + "/" + strings.TrimPrefix(pollingURL, "/")
	}
	if id != "" {
		return baseURL + "/videos/" + id
	}
	return ""
}

// buildFrameImage converts a VideoRequest.ImageRef into an OpenRouter-style
// frame_images entry anchored as the first frame. http(s)/data URLs pass
// through; local paths are read (sandbox-aware) and inlined as data URLs.
func buildFrameImage(ref string) (map[string]interface{}, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	var imageURL string
	switch {
	case strings.HasPrefix(ref, "data:"), strings.HasPrefix(ref, "http://"), strings.HasPrefix(ref, "https://"):
		imageURL = ref
	default:
		path := ref
		if sandbox.CurrentSandbox != nil && sandbox.CurrentSandbox.Mode != sandbox.SandboxModeOff {
			// This is a read (the file is only Stat'ed and ReadFile'd below),
			// so resolve against read roots - write roots would wrongly reject
			// frame images that live in configured read_roots.
			resolved, err := sandbox.CurrentSandbox.ResolveScopedPath(path, false)
			if err != nil {
				return nil, fmt.Errorf("video image outside approved workspace: %w", err)
			}
			path = resolved
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("video image not found: %s", ref)
		}
		if info.Size() > 10<<20 {
			return nil, fmt.Errorf("video image too large: %d bytes (max 10MB)", info.Size())
		}
		mime, ok := imageMimeByExt(strings.ToLower(filepath.Ext(path)))
		if !ok {
			return nil, fmt.Errorf("video image must be png/jpg/webp, got %q", filepath.Ext(path))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read video image: %w", err)
		}
		imageURL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	return map[string]interface{}{
		"type":       "image_url",
		"image_url":  map[string]string{"url": imageURL},
		"frame_type": "first_frame",
	}, nil
}

func imageMimeByExt(ext string) (string, bool) {
	switch ext {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".webp":
		return "image/webp", true
	default:
		return "", false
	}
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
