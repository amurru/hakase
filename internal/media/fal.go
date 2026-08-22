package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/vision"
)

var checkHostPublic = vision.CheckHostPublic

// falProvider implements Provider for fal.ai (image+video via queue+poll+download).
type falProvider struct {
	cfg    config.MediaConfig
	store  *Store
	log    LogFunc
	client *http.Client
}

// NewFalProvider creates fal provider.
func NewFalProvider(cfg config.MediaConfig, log LogFunc, store *Store) Provider {
	return &falProvider{
		cfg:   cfg,
		store: store,
		log:   log,
		client: &http.Client{
			Timeout: 120 * time.Second,
			// CheckRedirect is handled via shared SSRF guard? We'll implement simple.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				if err := vision.CheckHostPublic(req.URL.Host); err != nil {
					return fmt.Errorf("redirect target blocked: %w", err)
				}
				return nil
			},
		},
	}
}

func (p *falProvider) Name() string { return "fal" }

func (p *falProvider) Capabilities() Capabilities {
	return Capabilities{Image: true, Video: true}
}

func (p *falProvider) GenerateImage(ctx context.Context, req ImageRequest) (*MediaResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	w, h := req.ClampedSize()
	model := p.cfg.FalImageModel
	if req.Model != "" {
		model = req.Model
	}
	if model == "" {
		model = "fal-ai/flux/schnell"
	}
	return p.generateViaQueue(ctx, req.Prompt, model, w, h, req.Seed, nil, "image")
}

func (p *falProvider) GenerateVideo(ctx context.Context, req VideoRequest) (*MediaResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ImageRef) != "" {
		return nil, fmt.Errorf("provider fal does not support image-to-video; omit image or use provider openai (OpenAI-compatible router such as OpenRouter)")
	}
	model := p.cfg.FalVideoModel
	if req.Model != "" {
		model = req.Model
	}
	if model == "" {
		model = "fal-ai/wan/v2.7/text-to-video"
	}
	w, h := 1024, 576 // default 16:9 placeholder for aspect calc
	if req.Width != 0 && req.Height != 0 {
		w, h = req.Width, req.Height
	}
	aspect := aspectRatioFromWH(w, h)
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
	extra := map[string]interface{}{
		"resolution":   "720p",
		"aspect_ratio": aspect,
		"duration":     duration,
	}
	if req.Seed != nil {
		extra["seed"] = *req.Seed
	}
	mime := "video/mp4"
	ext := ".mp4"
	// For video we use generateViaQueue with video extra params
	res, err := p.generateViaQueue(ctx, req.Prompt, model, w, h, req.Seed, extra, "video")
	if err != nil {
		return nil, err
	}
	res.MimeType = mime
	// Ensure ext is mp4 (store Allocate used .png earlier? generateViaQueue picks ext based on kind)
	_ = ext
	res.Width = w
	res.Height = h
	return res, nil
}

func (p *falProvider) GenerateAudio(ctx context.Context, req AudioRequest) (*MediaResult, error) {
	return nil, fmt.Errorf("provider fal does not support audio")
}

func (p *falProvider) generateViaQueue(ctx context.Context, prompt, model string, w, h int, seed *int64, extra map[string]interface{}, kind string) (*MediaResult, error) {
	key := p.cfg.FalKey
	if key == "" {
		return nil, fmt.Errorf("fal auth failed: missing fal_key (401)")
	}
	base := p.cfg.FalBaseURL
	if base == "" {
		base = "https://queue.fal.run"
	}
	base = strings.TrimRight(base, "/")
	queueURL := base + "/" + model

	// Build request payload
	payload := map[string]interface{}{
		"prompt": prompt,
	}
	if kind == "image" {
		payload["image_size"] = map[string]int{"width": w, "height": h}
		if seed != nil {
			payload["seed"] = *seed
		}
	} else if kind == "video" {
		for k, v := range extra {
			payload[k] = v
		}
	}

	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", queueURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Key "+key)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fal queue request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("fal auth failed: check fal_key (401)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("fal queue failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var queueResp struct {
		RequestID string `json:"request_id"`
	}
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &queueResp); err != nil || queueResp.RequestID == "" {
		// Some fal responses use "request_id" directly, try alternative field
		var alt map[string]interface{}
		_ = json.Unmarshal(b, &alt)
		if v, ok := alt["request_id"].(string); ok && v != "" {
			queueResp.RequestID = v
		} else {
			return nil, fmt.Errorf("fal missing request_id")
		}
	}

	// Poll status
	statusURL := fmt.Sprintf("%s/%s/requests/%s/status", base, model, queueResp.RequestID)
	var resultURL string
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	timeout := 300 * time.Second
	if kind == "image" {
		timeout = 120 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("fal poll timeout")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		sreq, _ := http.NewRequestWithContext(ctx, "GET", statusURL, nil)
		sreq.Header.Set("Authorization", "Key "+key)
		sresp, err := p.client.Do(sreq)
		if err != nil {
			continue
		}
		sbody, _ := io.ReadAll(sresp.Body)
		sresp.Body.Close()
		if sresp.StatusCode == 401 {
			return nil, fmt.Errorf("fal auth failed: check fal_key (401)")
		}
		var status struct {
			Status   string `json:"status"`
			Response struct {
				Images []struct {
					URL string `json:"url"`
				} `json:"images"`
				Video struct {
					URL string `json:"url"`
				} `json:"video"`
			} `json:"response"`
		}
		_ = json.Unmarshal(sbody, &status)
		if status.Status == "COMPLETED" {
			if len(status.Response.Images) > 0 && status.Response.Images[0].URL != "" {
				resultURL = status.Response.Images[0].URL
			} else if status.Response.Video.URL != "" {
				resultURL = status.Response.Video.URL
			} else {
				// Try generic URL fields
				var generic map[string]interface{}
				_ = json.Unmarshal(sbody, &generic)
				// Try to find url in response
				if respMap, ok := generic["response"].(map[string]interface{}); ok {
					if imgs, ok := respMap["images"].([]interface{}); ok && len(imgs) > 0 {
						if m, ok := imgs[0].(map[string]interface{}); ok {
							if u, ok := m["url"].(string); ok {
								resultURL = u
							}
						}
					}
					if v, ok := respMap["video"].(map[string]interface{}); ok {
						if u, ok := v["url"].(string); ok {
							resultURL = u
						}
					}
				}
			}
			if resultURL == "" {
				return nil, fmt.Errorf("fal completed but no result url")
			}
			break
		} else if status.Status == "FAILED" {
			return nil, fmt.Errorf("fal generation failed")
		}
		// else IN_QUEUE / IN_PROGRESS continue
		if resultURL != "" {
			break
		}
	}

	// Download via SSRF-guarded client
	parsedURL := resultURL
	// Validate host is public (allow test override)
	u, err := parseURL(parsedURL)
	if err != nil {
		return nil, err
	}
	if err := checkHostPublic(u.Host); err != nil {
		return nil, fmt.Errorf("fal result url blocked: %w", err)
	}
	dreq, _ := http.NewRequestWithContext(ctx, "GET", parsedURL, nil)
	dresp, err := p.client.Do(dreq)
	if err != nil {
		return nil, fmt.Errorf("fal download failed: %w", err)
	}
	defer dresp.Body.Close()
	if dresp.StatusCode < 200 || dresp.StatusCode >= 300 {
		return nil, fmt.Errorf("fal download failed: HTTP %d", dresp.StatusCode)
	}
	capBytes := int64(20 << 20)
	ext := ".png"
	mime := "image/png"
	if kind == "video" {
		capBytes = 100 << 20
		ext = ".mp4"
		mime = "video/mp4"
	}
	limited := io.LimitReader(dresp.Body, capBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > capBytes {
		return nil, fmt.Errorf("fal file too large")
	}
	allocPath, err := p.store.Allocate(ext)
	if err != nil {
		return nil, err
	}
	if err := p.store.Write(allocPath, bytes.NewReader(data), capBytes); err != nil {
		return nil, err
	}
	// Report the workspace-relative path so the web UI mediaLinks plugin
	// rewrites it to /api/files/inline (absolute paths leak the host FS into
	// the chat and 404 against the page origin).
	relPath := p.store.WorkspaceRelPath(allocPath)
	return &MediaResult{
		Path:     relPath,
		Provider: "fal",
		Model:    model,
		MimeType: mime,
		Markdown: fmt.Sprintf("![generated](%s)", relPath),
		Seed:     seed,
		Width:    w,
		Height:   h,
	}, nil
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("unsupported url scheme: %s", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url has no host")
	}
	return u, nil
}

func aspectRatioFromWH(w, h int) string {
	if w == 0 || h == 0 {
		return "16:9"
	}
	ratio := float64(w) / float64(h)
	options := map[string]float64{
		"16:9": 16.0 / 9.0,
		"9:16": 9.0 / 16.0,
		"1:1":  1.0,
		"4:3":  4.0 / 3.0,
		"3:4":  3.0 / 4.0,
	}
	best := "16:9"
	bestDiff := 1e9
	for k, v := range options {
		diff := ratio - v
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			bestDiff = diff
			best = k
		}
	}
	return best
}
