// vision.go - multimodal image tool for the hakase agent.
//
// Provides a "vision" function tool that loads images (URL, local path, or
// data: URL) into the conversation. Operates in one of three modes:
//
//   - VisionNative: image is attached directly to the Gemini model call via a
//     BeforeModelCallback (the only injection point ADK v2 provides).
//   - VisionLegacy: a separate vision_model describes the image as text, which
//     is then returned to the main model. Works on all providers.
//   - VisionUnsupported: the model has no vision support and no vision_model
//     is configured; the tool warns and continues.
//
// The native path is Gemini-only because ADK's OpenAI adapter cannot serialize
// image parts in tool results (see .omo/plans/vision-integration.md appendix).

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/bmp" // register BMP decoder for image.Decode
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff" // register TIFF decoder for image.Decode
	_ "golang.org/x/image/webp" // register WEBP decoder for image.Decode

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// VisionMode describes how the vision tool will handle an image for the
// current model + configuration.
type VisionMode int

const (
	// VisionUnsupported means the main model has no vision support and no
	// vision_model is configured. The tool returns a guidance note and
	// continues the run (warn-and-continue).
	VisionUnsupported VisionMode = iota

	// VisionNative means the image is attached directly to the main model call
	// via the visionInjectionCallback (Gemini only).
	VisionNative

	// VisionLegacy means a separate vision_model is called to describe the
	// image as text, which is then returned to the main model.
	VisionLegacy
)

// Image size / dimension caps.
const (
	visionMaxDownloadBytes = 10 << 20 // 10 MB
	visionEmbedTargetBytes = 4 << 20  // 4 MB (native path embed target)
	visionEmbedMaxDim      = 7900     // native path max dimension (px)
	visionLegacyMaxBytes   = 5 << 20  // 5 MB (legacy path embed target)
	visionLegacyMaxDim     = 8000     // legacy path max dimension (px)
	visionHardCeilingBytes = 20 << 20 // 20 MB hard ceiling
	visionResizeMaxRounds  = 5        // max halving rounds during resize
	visionLegacyTimeout    = 3 * time.Minute
)

// VisionInput is the argument schema for the vision tool.
type VisionInput struct {
	ImageURL string `json:"image_url" doc:"Image URL (http/https), local file path, or data: URL to load into the conversation."`
	Question string `json:"question" doc:"Optional question or request about the image."`
}

// VisionOutput is the result returned by the vision tool.
type VisionOutput struct {
	Success      bool   `json:"success"`
	Description  string `json:"description,omitempty"`
	Note         string `json:"note,omitempty"`
	ImageDataURL string `json:"image_data_url,omitempty"` // native-path marker
	Question     string `json:"question,omitempty"`       // native-path question
}

// visionModel is the legacy-path model created from cfg.VisionModel in
// setupRunner. nil = not configured or creation failed.
var visionModel model.LLM

// currentModelInfo holds the main model's capabilities from the async
// FetchModelInfo fetch (set in main.go). nil = fetch not complete/failed.
var currentModelInfo *ModelInfo

// resolveVisionProvider selects the provider used to create the vision model.
// Precedence: an explicit vision_provider wins; otherwise vision_base_url
// forces an OpenAI-compatible endpoint; otherwise the main provider is reused.
func resolveVisionProvider(main LLMProvider, cfg *Config) LLMProvider {
	if cfg != nil && cfg.VisionProvider != "" {
		switch cfg.VisionProvider {
		case "gemini":
			return &GeminiProvider{}
		case "openai", "openai-compatible":
			return &OpenAIProvider{BaseURL: cfg.VisionBaseURL}
		}
		// Unknown value: fall through to the inherit rules below.
	}
	if cfg != nil && cfg.VisionBaseURL != "" {
		return &OpenAIProvider{BaseURL: cfg.VisionBaseURL}
	}
	return main
}

// visionInjected tracks which function-response IDs have already been
// processed by the injection callback, providing idempotency across
// multiple model calls within the same session. Entries are tiny
// (per-call unique IDs); no cleanup is needed for v1.
var visionInjected sync.Map

// visionDescribeCache caches vision-model descriptions of user-attached images,
// keyed by the sha256 of the image bytes. req.Contents is deep-cloned from
// session events on every model call, so without the cache the vision model
// would re-describe every attached image on every turn. Failures are cached
// as their replacement text too, so a broken/misconfigured vision model is
// not hammered on each call.
var visionDescribeCache sync.Map

// httpClient is the shared HTTP client for image downloads, with connection
// pooling, sane timeouts, and SSRF-preventing redirect checks.
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if err := checkHostPublic(req.URL.Host); err != nil {
			return fmt.Errorf("redirect target blocked: %w", err)
		}
		return nil
	},
}

// ---------------------------------------------------------------------------
// Detection helpers
// ---------------------------------------------------------------------------

// visionModelNameMatch checks whether the given model name matches known
// vision-capable model families (case-insensitive substring match).
func visionModelNameMatch(modelName string) bool {
	if modelName == "" {
		return false
	}
	lower := strings.ToLower(modelName)
	patterns := []string{
		"gpt-4o", "gpt-4.1", "gpt-5", "o1-", "o3-", "o4-",
		"claude-3", "claude-4", "gemini-", "llama-3.2-vision",
		"qwen2.5-vl", "qwen3-vl", "glm-4v", "phi-3.5-vision",
		"phi-4-vision", "llava", "moondream", "internvl",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// visionModelUsable reports whether the legacy vision-model path is available:
// either a successfully-created visionModel global exists, or the config has a
// non-empty VisionModel (the model may not be created yet at tool-call time,
// but the presence of the config field means the user intends to use it).
func visionModelUsable(cfg *Config) bool {
	if visionModel != nil {
		return true
	}
	if cfg != nil && cfg.VisionModel != "" {
		return true
	}
	return false
}

// resolveMainModelVision determines the active VisionMode for the given model
// info and configuration.
func resolveMainModelVision(mi *ModelInfo, cfg *Config) VisionMode {
	if cfg == nil {
		return VisionUnsupported
	}

	// Provider constraint: OpenAI and OpenAI-compatible providers can never
	// use the native path (ADK's OpenAI adapter cannot serialize image parts
	// embedded in tool results).
	providerBlocksNative := cfg.Provider == "openai" || cfg.Provider == "openai-compatible"

	legacy := func() VisionMode {
		if visionModelUsable(cfg) {
			return VisionLegacy
		}
		return VisionUnsupported
	}

	// Config override (highest precedence).
	switch cfg.ModelVision {
	case "no":
		return legacy()
	case "yes":
		if providerBlocksNative {
			return legacy()
		}
		return VisionNative
	}

	// Live metadata.
	if mi != nil && mi.SupportsVision != nil {
		if *mi.SupportsVision {
			if providerBlocksNative {
				return legacy()
			}
			return VisionNative
		}
		return legacy()
	}

	// Unknown: fall back to name allowlist.
	modelName := ""
	if mi != nil {
		modelName = mi.Name
	}
	if modelName == "" {
		modelName = cfg.ModelName
	}
	if visionModelNameMatch(modelName) {
		if providerBlocksNative {
			return legacy()
		}
		return VisionNative
	}
	return legacy()
}

// ---------------------------------------------------------------------------
// Tool creation
// ---------------------------------------------------------------------------

// createVisionTool returns the registered "vision" function tool.
func createVisionTool() (tool.Tool, error) {
	return newDocTool(functiontool.Config{
		Name: "vision",
		Description: "Load an image (URL, local file path, or data: URL) so it " +
			"can be seen by the model. Use whenever the user references an image " +
			"or a URL or file path points to an image.",
	}, visionHandler)
}

// visionHandler is the handler for the vision tool.
func visionHandler(ctx agent.Context, input VisionInput) (VisionOutput, error) {
	if input.ImageURL == "" {
		return VisionOutput{Success: false, Note: "image_url is required"}, nil
	}

	// 1. Resolve source.
	imgData, mime, err := resolveImageSource(ctx, input.ImageURL)
	if err != nil {
		return VisionOutput{}, fmt.Errorf("resolve image source: %w", err)
	}

	// 2. Normalize format.
	imgData, mime, err = normalizeImage(imgData, mime)
	if err != nil {
		return VisionOutput{}, fmt.Errorf("normalize image: %w", err)
	}

	// 3. Decide mode.
	mode := resolveMainModelVision(currentModelInfo, currentConfig)

	switch mode {
	case VisionNative:
		// 4a. Resize for native embedding.
		imgData, mime, err = embedReadyImage(imgData, mime, visionEmbedTargetBytes, visionEmbedMaxDim)
		if err != nil {
			return VisionOutput{}, fmt.Errorf("resize for embedding: %w", err)
		}
		// Build data URL for the callback marker.
		dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(imgData)
		return VisionOutput{
			Success:      true,
			Note:         "image attached natively",
			ImageDataURL: dataURL,
			Question:     input.Question,
		}, nil

	case VisionLegacy:
		// 4b. Resize for legacy vision-model call (looser caps).
		imgData, mime, err = embedReadyImage(imgData, mime, visionLegacyMaxBytes, visionLegacyMaxDim)
		if err != nil {
			return VisionOutput{}, fmt.Errorf("resize for vision model: %w", err)
		}
		desc, err := describeImageWithVisionModel(ctx, imgData, mime, input.Question)
		if err != nil {
			return VisionOutput{}, fmt.Errorf("vision model: %w", err)
		}
		return VisionOutput{Success: true, Description: desc}, nil

	default: // VisionUnsupported
		return VisionOutput{
			Success: false,
			Note: "Main model has no vision support and no vision_model is configured. " +
				"Set vision_model in config.json (e.g. \"google/gemini-3-flash-preview:free\") " +
				"or model_vision:\"yes\" if the main model supports images.",
		}, nil
	}
}

// ---------------------------------------------------------------------------
// Injection callback (native path)
// ---------------------------------------------------------------------------

// visionInjectionCallback is a BeforeModelCallback that rewrites vision-tool
// results into inline image parts. It walks req.Contents looking for function
// responses named "vision" that carry an image_data_url marker, decodes the
// data URL, appends a new content block with the image bytes + question text,
// and strips the marker keys from the original response. Idempotent across
// repeated model calls within the same session (tracked by function call ID).
//
// It also rewrites user-attached image parts (from @-files or pasted
// screenshots) when the main model cannot see images: they are replaced with a
// vision-model text description (legacy mode) or a warning note (unsupported
// mode). Without this, the OpenAI-compatible adapter rejects InlineData parts
// outright with "openai: unsupported content part" and the whole call fails.
func visionInjectionCallback(ctx agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil || part.FunctionResponse == nil {
				continue
			}
			fr := part.FunctionResponse
			if fr.Name != "vision" {
				continue
			}
			resp := fr.Response
			if resp == nil {
				continue
			}
			imageDataURL, _ := resp["image_data_url"].(string)
			if imageDataURL == "" {
				// No marker - nothing to inject. Still strip stale keys.
				delete(resp, "image_data_url")
				delete(resp, "question")
				continue
			}

			frID := fr.ID

			// Idempotency: only inject once per call ID.
			if _, seen := visionInjected.LoadOrStore(frID, true); !seen {
				// Decode the data URL.
				data, mime, err := parseDataURL(imageDataURL)
				if err != nil {
					// If we cannot decode, strip the marker so downstream
					// does not stall but leave a note.
					delete(resp, "image_data_url")
					delete(resp, "question")
					continue
				}

				question := ""
				if q, ok := resp["question"].(string); ok && q != "" {
					question = q
				}
				if question == "" {
					question = "Image loaded by the vision tool."
				}

				// Build the injected content: image part + text part.
				injectedParts := []*genai.Part{
					genai.NewPartFromBytes(data, mime),
					genai.NewPartFromText(question),
				}
				req.Contents = append(req.Contents,
					genai.NewContentFromParts(injectedParts, genai.RoleUser),
				)
			}

			// Always strip marker keys so the model sees a clean ack.
			// req.Contents is deep-cloned per model call, so in-place
			// modification is per-call deterministic.
			delete(resp, "image_data_url")
			delete(resp, "question")
		}
	}

	// Phase 2: user-attached images (not vision-tool results) must be made
	// safe for the main model. On native-vision models they pass through as
	// inline parts; otherwise they are replaced with a text description (or
	// a warning) so the OpenAI-compatible adapter never sees an image part.
	if cfg := currentConfig; cfg != nil {
		rewriteAttachedImages(ctx, req, resolveMainModelVision(currentModelInfo, cfg))
	}
	return nil, nil
}

// visionAttachContextMax caps how much surrounding user text is passed to the
// vision model as the question when rewriting an attached image.
const visionAttachContextMax = 2000

// rewriteAttachedImages replaces image InlineData parts in user contents with
// text: a vision-model description in legacy mode, a guidance note when the
// vision path is unavailable. Native mode leaves the pixels untouched (the
// main model sees them directly).
func rewriteAttachedImages(ctx context.Context, req *model.LLMRequest, mode VisionMode) {
	if mode == VisionNative || req == nil {
		return
	}
	for _, content := range req.Contents {
		if content == nil || content.Role != genai.RoleUser || len(content.Parts) == 0 {
			continue
		}
		// Surrounding text (the user's actual question) becomes the vision
		// model's prompt context.
		var sb strings.Builder
		for _, p := range content.Parts {
			if p != nil && p.Text != "" {
				sb.WriteString(p.Text)
				sb.WriteString("\n")
			}
		}
		question := strings.TrimSpace(sb.String())
		if question == "" {
			question = "Describe this image."
		}
		if len(question) > visionAttachContextMax {
			question = question[:visionAttachContextMax]
		}

		for i, part := range content.Parts {
			if part == nil || part.InlineData == nil {
				continue
			}
			mime := part.InlineData.MIMEType
			if !isKnownImageMime(mime) {
				// Fall back to magic-byte sniff; non-images (audio/video)
				// are left alone.
				if _, ok := detectImageMime(part.InlineData.Data); !ok {
					continue
				}
				mime, _ = detectImageMime(part.InlineData.Data)
			}
			content.Parts[i] = genai.NewPartFromText(describeOrWarnImage(ctx, part.InlineData.Data, mime, question, mode))
		}
	}
}

// describeOrWarnImage returns the text that replaces an attached image part:
// a vision-model description (cached by image bytes) in legacy mode, or a
// warn-and-continue note otherwise.
func describeOrWarnImage(ctx context.Context, data []byte, mime, question string, mode VisionMode) string {
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	if cached, ok := visionDescribeCache.Load(key); ok {
		return cached.(string)
	}

	var text string
	if mode == VisionLegacy {
		desc, err := describeImageWithVisionModel(ctx, data, mime, question)
		if err != nil {
			text = fmt.Sprintf(
				"[image: %s - the vision model could not describe it: %v. Check vision_model/vision_provider in config.json.]",
				mime, err)
		} else {
			text = "[Image description from the vision model]:\n" + desc
		}
	} else {
		text = fmt.Sprintf(
			"[image: %s - the main model cannot see images and no vision_model is configured. Set vision_model (and vision_provider if needed) in config.json to enable image descriptions.]",
			mime)
	}
	visionDescribeCache.Store(key, text)
	return text
}

// ---------------------------------------------------------------------------
// Image source resolution
// ---------------------------------------------------------------------------

// resolveImageSource resolves an image_url to raw bytes and a detected MIME
// type. Supports data: URLs, http(s) URLs (SSRF-guarded), and local file
// paths (sandbox-aware).
func resolveImageSource(ctx context.Context, imageURL string) ([]byte, string, error) {
	switch {
	case strings.HasPrefix(imageURL, "data:"):
		return loadDataURL(imageURL)
	case strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://"):
		return downloadImage(ctx, imageURL)
	default:
		return loadLocalImage(imageURL)
	}
}

// loadDataURL parses a data: URL (data:<mime>;base64,<payload>) and returns
// the decoded bytes and MIME type. Caps at 10 MB.
func loadDataURL(raw string) ([]byte, string, error) {
	const prefix = "data:"
	if !strings.HasPrefix(raw, prefix) {
		return nil, "", fmt.Errorf("not a data URL")
	}
	rest := raw[len(prefix):]
	commaIdx := strings.Index(rest, ",")
	if commaIdx < 0 {
		return nil, "", fmt.Errorf("invalid data URL: missing comma")
	}
	header := rest[:commaIdx]
	isBase64 := strings.HasSuffix(header, ";base64")
	payload := rest[commaIdx+1:]
	if len(payload) > visionMaxDownloadBytes*2 { // generous pre-check
		return nil, "", fmt.Errorf("data URL too large")
	}
	var decoded []byte
	var err error
	if isBase64 {
		decoded, err = base64.StdEncoding.DecodeString(payload)
	} else {
		decoded, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			// Not base64; treat as raw URL-encoded? Unlikely for images.
			return nil, "", fmt.Errorf("data URL decode: %w", err)
		}
	}
	if err != nil {
		return nil, "", fmt.Errorf("data URL base64 decode: %w", err)
	}
	if len(decoded) > visionMaxDownloadBytes {
		return nil, "", fmt.Errorf("image too large: %d bytes (max %d)", len(decoded), visionMaxDownloadBytes)
	}
	detectedMime, ok := detectImageMime(decoded)
	if !ok {
		return nil, "", fmt.Errorf("unrecognized image format in data URL")
	}
	return decoded, detectedMime, nil
}

// downloadImage downloads an image from an http(s) URL with SSRF protection,
// retries for transient failures, and caps the response at 10 MB.
func downloadImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, "", fmt.Errorf("URL has no host")
	}

	// Pre-resolve host for SSRF check. This is done once; redirect
	// targets are checked at each hop via httpClient.CheckRedirect.
	if err := checkHostPublic(parsed.Host); err != nil {
		return nil, "", err
	}

	// Retry loop for transient failures.
	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		data, mime, err := downloadOnce(ctx, parsed)
		if err == nil {
			return data, mime, nil
		}
		lastErr = err
		// Only retry on 429, 5xx, or network errors.
		if !isTransient(err) {
			break
		}
		// Backoff: 2s, 4s, 8s.
		backoff := time.Duration(1<<(attempt+1)) * time.Second
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(backoff):
		}
	}
	return nil, "", lastErr
}

// downloadOnce performs a single HTTP GET with the shared httpClient, streams
// the body through a limit reader, and detects the MIME type from magic bytes.
func downloadOnce(ctx context.Context, u *url.URL) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	// 4xx (except 429) is terminal.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
		return nil, "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return nil, "", transientError{msg: fmt.Sprintf("download status %d", resp.StatusCode)}
	}

	// Stream body through limit reader (10 MB + 1 for overflow detection).
	limited := io.LimitReader(resp.Body, visionMaxDownloadBytes+1)
	buf := &bytes.Buffer{}
	n, err := io.Copy(buf, limited)
	if err != nil {
		return nil, "", transientError{msg: fmt.Sprintf("read body: %v", err)}
	}
	if n > visionMaxDownloadBytes {
		return nil, "", fmt.Errorf("image too large: exceeds %d bytes", visionMaxDownloadBytes)
	}

	data := buf.Bytes()
	detectedMime, ok := detectImageMime(data)
	if !ok {
		// Fall back to server Content-Type if magic bytes cannot identify.
		serverMime := resp.Header.Get("Content-Type")
		if serverMime != "" && isKnownImageMime(serverMime) {
			return data, serverMime, nil
		}
		return nil, "", fmt.Errorf("unrecognized image format at %s", u.String())
	}
	return data, detectedMime, nil
}

// loadLocalImage reads an image from a local file path. When the sandbox is
// active, the path is resolved through workspace confinement.
func loadLocalImage(path string) ([]byte, string, error) {
	resolved := path
	if currentSandbox != nil && currentSandbox.Mode != SandboxModeOff {
		var err error
		resolved, err = currentSandbox.resolveScopedPath(path, false)
		if err != nil {
			return nil, "", fmt.Errorf("sandbox path resolution: %w", err)
		}
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}
	if len(data) > visionMaxDownloadBytes {
		return nil, "", fmt.Errorf("image too large: %d bytes (max %d)", len(data), visionMaxDownloadBytes)
	}
	detectedMime, ok := detectImageMime(data)
	if !ok {
		return nil, "", fmt.Errorf("unrecognized image format: %s", path)
	}
	return data, detectedMime, nil
}

// ---------------------------------------------------------------------------
// SSRF guards
// ---------------------------------------------------------------------------

// checkHostPublic resolves a host (with optional port) and rejects any
// address in a private, loopback, link-local, or unspecified range.
func checkHostPublic(host string) error {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		// No port or invalid port; use the host directly.
		h = host
	}
	addrs, err := net.ResolveIPAddr("ip", h)
	if err != nil {
		return fmt.Errorf("cannot resolve host %s: %w", h, err)
	}
	ip, err := netip.ParseAddr(addrs.IP.String())
	if err != nil {
		return fmt.Errorf("cannot parse resolved IP: %w", err)
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return fmt.Errorf("blocked internal address: %s (%s)", h, ip.String())
	}
	return nil
}

// isKnownImageMime checks a server-reported Content-Type against known image
// MIME types.
func isKnownImageMime(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp",
		"image/bmp", "image/tiff", "image/svg+xml":
		return true
	}
	return false
}

// transientError is a sentinel for retriable download errors.
type transientError struct{ msg string }

func (e transientError) Error() string { return e.msg }

func isTransient(err error) bool {
	_, ok := err.(transientError)
	return ok
}

// ---------------------------------------------------------------------------
// MIME detection and normalization
// ---------------------------------------------------------------------------

// detectImageMime sniffs the first 64 bytes of data to identify the image
// format. Returns (mime, true) when a known format is found, ("", false)
// otherwise.
func detectImageMime(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	// PNG: \x89PNG\r\n\x1a\n
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' &&
		data[4] == '\r' && data[5] == '\n' && data[6] == 0x1a && data[7] == '\n' {
		return "image/png", true
	}
	// JPEG: \xff\xd8\xff
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg", true
	}
	// GIF: GIF87a or GIF89a
	if len(data) >= 6 {
		if (data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' && data[4] == '7' && data[5] == 'a') ||
			(data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' && data[4] == '9' && data[5] == 'a') {
			return "image/gif", true
		}
	}
	// WEBP: RIFF....WEBP
	if len(data) >= 12 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
		data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return "image/webp", true
	}
	// BMP: BM
	if len(data) >= 2 && data[0] == 'B' && data[1] == 'M' {
		return "image/bmp", false // needs conversion
	}
	// TIFF: II*\x00 (little-endian) or MM\x00* (big-endian)
	if len(data) >= 4 {
		if (data[0] == 0x49 && data[1] == 0x49 && data[2] == 0x2A && data[3] == 0x00) ||
			(data[0] == 0x4D && data[1] == 0x4D && data[2] == 0x00 && data[3] == 0x2A) {
			return "image/tiff", false // needs conversion
		}
	}
	// SVG: text <?xml or <svg
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 4 {
		if bytes.HasPrefix(trimmed, []byte("<?xml")) || bytes.HasPrefix(trimmed, []byte("<svg")) {
			return "image/svg+xml", false
		}
	}
	// Loose SVG: contains <svg
	if bytes.Contains(data, []byte("<svg")) && bytes.Contains(data, []byte("</svg>")) {
		return "image/svg+xml", false
	}
	return "", false
}

// isEmbedSupported reports whether the given MIME type can be embedded
// directly (jpeg, png, gif, webp).
func isEmbedSupported(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

// normalizeImage converts unsupported image formats to a supported format.
// BMP and TIFF are re-encoded as PNG. SVG is rasterized via system tools
// (rsvg-convert or inkscape) if available, otherwise returns an error.
// Already-supported formats pass through unchanged.
func normalizeImage(data []byte, detectedMime string) ([]byte, string, error) {
	if isEmbedSupported(detectedMime) {
		return data, detectedMime, nil
	}

	switch detectedMime {
	case "image/bmp", "image/tiff":
		return rasterToPNG(data, detectedMime)
	case "image/svg+xml":
		return rasterizeSVG(data)
	default:
		return nil, "", fmt.Errorf("cannot normalize unsupported format: %s", detectedMime)
	}
}

// rasterToPNG decodes a BMP or TIFF image and re-encodes it as PNG.
func rasterToPNG(data []byte, mime string) ([]byte, string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode %s: %w", mime, err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, "", fmt.Errorf("encode PNG: %w", err)
	}
	return buf.Bytes(), "image/png", nil
}

// rasterizeSVG attempts to convert an SVG to PNG using rsvg-convert or
// inkscape. Returns an actionable error if neither is available.
func rasterizeSVG(data []byte) ([]byte, string, error) {
	// Write SVG to a temp file.
	svgFile, err := os.CreateTemp("", "hakase-vision-*.svg")
	if err != nil {
		return nil, "", fmt.Errorf("create temp SVG: %w", err)
	}
	defer os.Remove(svgFile.Name())
	if _, err := svgFile.Write(data); err != nil {
		svgFile.Close()
		return nil, "", fmt.Errorf("write temp SVG: %w", err)
	}
	svgFile.Close()

	outFile, err := os.CreateTemp("", "hakase-vision-*.png")
	if err != nil {
		return nil, "", fmt.Errorf("create temp PNG: %w", err)
	}
	outPath := outFile.Name()
	outFile.Close()
	defer os.Remove(outPath)

	// Try rsvg-convert first.
	if rsvg, err := exec.LookPath("rsvg-convert"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, rsvg, "-o", outPath, svgFile.Name())
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, "", fmt.Errorf("rsvg-convert failed: %w (output: %s)", err, string(out))
		}
		return readRasterizedPNG(outPath)
	}

	// Try inkscape.
	if inkscape, err := exec.LookPath("inkscape"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, inkscape,
			svgFile.Name(),
			"--export-type=png",
			"--export-filename="+outPath,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, "", fmt.Errorf("inkscape failed: %w (output: %s)", err, string(out))
		}
		return readRasterizedPNG(outPath)
	}

	return nil, "", fmt.Errorf(
		"this is an SVG, which vision models cannot read directly, " +
			"and no SVG rasterizer is installed (tried rsvg-convert, inkscape). " +
			"convert the SVG to PNG first, or install a rasterizer, then re-run vision on the PNG",
	)
}

// readRasterizedPNG reads a PNG from a temp file and returns its bytes.
func readRasterizedPNG(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read rasterized PNG: %w", err)
	}
	return data, "image/png", nil
}

// ---------------------------------------------------------------------------
// Size control
// ---------------------------------------------------------------------------

// embedReadyImage ensures the image fits within maxBytes and maxDim by
// resizing iteratively. For JPEG images it applies a quality ladder; all
// other formats are re-encoded as PNG when resizing is needed. Returns the
// (possibly resized) bytes and the (possibly changed) MIME type.
func embedReadyImage(data []byte, mime string, maxBytes int, maxDim int) ([]byte, string, error) {
	// Fast path: within byte cap. Check dimensions cheaply.
	if len(data) <= maxBytes {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err == nil && cfg.Width <= maxDim && cfg.Height <= maxDim {
			return data, mime, nil
		}
	}

	// Hard ceiling check before any work.
	if len(data) > visionHardCeilingBytes {
		return nil, "", fmt.Errorf("image too large: %d bytes exceeds hard ceiling of %d bytes",
			len(data), visionHardCeilingBytes)
	}

	// Decode for resizing.
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// Cannot decode; return original and let the provider decide.
		return data, mime, nil
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	isJPEG := mime == "image/jpeg"

	// Quality ladder for JPEG.
	qualities := []int{85, 70, 50}
	qi := 0

	for round := 0; round < visionResizeMaxRounds; round++ {
		// Check if resize is needed.
		if w <= maxDim && h <= maxDim {
			// Dimensions OK - try encoding.
			encoded, outMime, encErr := encodeImage(img, mime, qualityFor(isJPEG, qualities, qi))
			if encErr == nil && len(encoded) <= maxBytes {
				return encoded, outMime, nil
			}
			// If JPEG, try next quality.
			if isJPEG && qi < len(qualities)-1 {
				qi++
				continue
			}
			// Still too large after all qualities; force resize.
		}

		// Halve the longest side, preserving aspect ratio: scale the other
		// side by the same factor. (Bounds at this point is the CURRENT
		// image bounds, so the divisor is the pre-shrink width/height.)
		if w >= h {
			prevW := w
			w = w / 2
			if w < 1 {
				w = 1
			}
			h = h * w / prevW
		} else {
			prevH := h
			h = h / 2
			if h < 1 {
				h = 1
			}
			w = w * h / prevH
		}
		if w < 1 {
			w = 1
		}
		if h < 1 {
			h = 1
		}
		// Stop if dimensions cannot shrink further (prevents an infinite
		// or degenerate loop on 1xN images).
		if w == bounds.Dx() && h == bounds.Dy() {
			break
		}

		// Actually resize.
		resized := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(resized, resized.Bounds(), img, bounds, draw.Over, nil)
		img = resized
		bounds = resized.Bounds()

		// Reset quality ladder for the new size.
		qi = 0

		// Encode and check.
		encoded, outMime, encErr := encodeImage(img, mime, qualityFor(isJPEG, qualities, qi))
		if encErr == nil && len(encoded) <= maxBytes {
			return encoded, outMime, nil
		}
		// Try quality steps for JPEG.
		if isJPEG {
			for qi = 1; qi < len(qualities); qi++ {
				encoded, _, encErr = encodeImage(img, mime, qualities[qi])
				if encErr == nil && len(encoded) <= maxBytes {
					return encoded, outMime, nil
				}
			}
		}
		// Continue to next round.
	}

	// Final attempt after all rounds: encode with minimum quality.
	finalQ := 50
	if !isJPEG {
		finalQ = -1
	}
	encoded, outMime, err := encodeImage(img, mime, finalQ)
	if err != nil {
		return nil, "", fmt.Errorf("encode after resize: %w", err)
	}
	if len(encoded) > visionHardCeilingBytes {
		return nil, "", fmt.Errorf("image too large for the vision API after resizing: %d bytes", len(encoded))
	}
	return encoded, outMime, nil
}

// encodeImage encodes an image.Image to bytes. For JPEG input, it uses
// jpeg.Encode with the given quality (-1 = PNG fallback). For all other
// formats it re-encodes as PNG.
func encodeImage(img image.Image, mime string, quality int) ([]byte, string, error) {
	var buf bytes.Buffer
	if mime == "image/jpeg" && quality >= 0 {
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/jpeg", nil
	}
	if err := png.Encode(&buf, img); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/png", nil
}

// qualityFor returns the quality value for the given index, or -1 if not JPEG.
func qualityFor(isJPEG bool, qualities []int, idx int) int {
	if !isJPEG {
		return -1
	}
	if idx < len(qualities) {
		return qualities[idx]
	}
	return qualities[len(qualities)-1]
}

// ---------------------------------------------------------------------------
// Legacy path: vision-model call
// ---------------------------------------------------------------------------

// describeImageWithVisionModel sends the image to the configured vision_model
// with an optional question and returns the model's text description.
func describeImageWithVisionModel(ctx context.Context, img []byte, mime string, question string) (string, error) {
	llm := visionModel
	if llm == nil {
		return "", fmt.Errorf("vision_model not available - set vision_model in config.json")
	}

	prompt := "Fully describe and explain everything about this image"
	if question != "" {
		prompt += ", then answer: " + question
	}

	req := &model.LLMRequest{
		Model: llm.Name(),
		Contents: []*genai.Content{
			genai.NewContentFromParts([]*genai.Part{
				genai.NewPartFromBytes(img, mime),
				genai.NewPartFromText(prompt),
			}, genai.RoleUser),
		},
	}

	ctx, cancel := context.WithTimeout(ctx, visionLegacyTimeout)
	defer cancel()

	var parts []string
	for resp, err := range llm.GenerateContent(ctx, req, false) {
		if err != nil {
			return "", fmt.Errorf("vision model: %w", err)
		}
		if resp == nil || resp.Content == nil {
			continue
		}
		for _, part := range resp.Content.Parts {
			if part == nil {
				continue
			}
			if part.Text != "" && !part.Thought {
				parts = append(parts, part.Text)
			}
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		return "", fmt.Errorf("vision model returned empty response")
	}
	return text, nil
}

// parseDataURL decodes a data: URL (data:<mime>;base64,<payload>) into raw
// bytes and a MIME type. Used by the injection callback.
func parseDataURL(raw string) ([]byte, string, error) {
	if !strings.HasPrefix(raw, "data:") {
		return nil, "", fmt.Errorf("not a data URL")
	}
	rest := raw[len("data:"):]
	idx := strings.Index(rest, ",")
	if idx < 0 {
		return nil, "", fmt.Errorf("invalid data URL")
	}
	header := rest[:idx]
	payload := rest[idx+1:]

	mime := "image/png"
	if strings.HasSuffix(header, ";base64") {
		mimePart := strings.TrimSuffix(header, ";base64")
		if mimePart != "" {
			mime = mimePart
		}
	} else if header != "" {
		mime = header
	}

	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", fmt.Errorf("base64 decode: %w", err)
	}
	return decoded, mime, nil
}
