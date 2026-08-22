package media

import (
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"context"
	"fmt"
	"strings"
)

// Capabilities describes what a provider can generate.
type Capabilities struct {
	Image bool `json:"image"`
	Video bool `json:"video"`
	Audio bool `json:"audio"`
}

// ImageRequest is the input to GenerateImage.
type ImageRequest struct {
	Prompt         string
	NegativePrompt string
	Width          int
	Height         int
	Steps          int
	Seed           *int64
	Provider       string
	Model          string
}

// Validate checks ImageRequest fields.
func (r ImageRequest) Validate() error {
	p := strings.TrimSpace(r.Prompt)
	if len(p) == 0 || len(p) > 4000 {
		return fmt.Errorf("prompt is required (1-4000 chars)")
	}
	if r.Width != 0 && (r.Width < 256 || r.Width > 2048) {
		return fmt.Errorf("width must be 256-2048, got %d", r.Width)
	}
	if r.Height != 0 && (r.Height < 256 || r.Height > 2048) {
		return fmt.Errorf("height must be 256-2048, got %d", r.Height)
	}
	if r.Steps != 0 && (r.Steps < 1 || r.Steps > 50) {
		return fmt.Errorf("steps must be 1-50, got %d", r.Steps)
	}
	return nil
}

// ClampedSize returns width/height clamped to 256-2048, with defaults 1024.
func (r ImageRequest) ClampedSize() (int, int) {
	w, h := r.Width, r.Height
	if w == 0 {
		w = 1024
	}
	if h == 0 {
		h = 1024
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
	return w, h
}

// VideoRequest is the input to GenerateVideo.
type VideoRequest struct {
	Prompt          string
	DurationSeconds int
	Width           int
	Height          int
	Provider        string
	Model           string
	Seed            *int64
	// ImageRef optionally anchors generation to an input image
	// (image-to-video via first frame). Accepts an http(s) URL, a
	// data: URL, or a local file path (providers convert local files
	// to a data URL). Empty means pure text-to-video.
	ImageRef string
}

// Validate checks VideoRequest.
func (r VideoRequest) Validate() error {
	p := strings.TrimSpace(r.Prompt)
	if len(p) == 0 || len(p) > 4000 {
		return fmt.Errorf("prompt is required (1-4000 chars)")
	}
	return nil
}

// AudioRequest is the input to GenerateAudio.
type AudioRequest struct {
	Text     string
	Voice    string
	Provider string
	Model    string
}

// Validate checks AudioRequest.
func (r AudioRequest) Validate() error {
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("text is required")
	}
	return nil
}

// MediaResult is the result from a provider.
type MediaResult struct {
	Path     string `json:"path"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Seed     *int64 `json:"seed,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	MimeType string `json:"mime_type"`
	Markdown string `json:"markdown"`
}

// LogFunc is a callback for status logging.
type LogFunc = interfaces.LogFunc

// Provider is the pluggable media generation interface.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	GenerateImage(ctx context.Context, req ImageRequest) (*MediaResult, error)
	GenerateVideo(ctx context.Context, req VideoRequest) (*MediaResult, error)
	GenerateAudio(ctx context.Context, req AudioRequest) (*MediaResult, error)
}

// Factory creates a Provider from config.
type Factory func(cfg config.MediaConfig, log LogFunc, store *Store) (Provider, error)
