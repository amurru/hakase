package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/media"
)

// mediaRegistry is set by web bootstrap (main.go/web.go) after constructing the media registry.
var mediaRegistry *media.Registry

// SetMediaRegistry installs the global media registry for status handlers.
func SetMediaRegistry(r *media.Registry) {
	mediaRegistry = r
}

// mediaRouter is the minimal interface for media routes.
type mediaRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
}

// RegisterMediaRoutes registers GET /api/media/status and /api/media/manifest inside the auth group.
func RegisterMediaRoutes(r mediaRouter) {
	r.Get("/media/status", MediaStatus)
	r.Get("/media/manifest", MediaManifest)
}

// MediaStatus handles GET /api/media/status (auth-gated).
func MediaStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _ := config.LoadConfig(config.ResolveConfigPath("config.json"))
	// Fallback to defaults if load fails
	var mediaCfg config.MediaConfig
	if cfg != nil {
		mediaCfg = cfg.Media
		mediaCfg.ApplyDefaults()
	} else {
		mediaCfg = config.MediaConfig{}
		mediaCfg.ApplyDefaults()
	}

	// Override with registry config if registry exists (ensures env overrides etc are reflected)
	if mediaRegistry != nil {
		mediaCfg = mediaRegistry.Config()
	}

	// Resolve providers via registry when available, else via defaults (pil guaranteed)
	resolvedImage := "none"
	resolvedVideo := "none"
	resolvedAudio := "off"
	if mediaRegistry != nil {
		if p, err := mediaRegistry.Resolve("image"); err == nil {
			resolvedImage = p.Name()
		} else {
			resolvedImage = "pil"
		}
		if p, err := mediaRegistry.Resolve("video"); err == nil {
			resolvedVideo = p.Name()
		} else {
			resolvedVideo = "none"
		}
		if p, err := mediaRegistry.Resolve("audio"); err == nil {
			resolvedAudio = p.Name()
		} else {
			// audio off is expected
			if mediaCfg.AudioProvider == "off" {
				resolvedAudio = "off"
			} else {
				resolvedAudio = "none"
			}
		}
	} else {
		// No registry: zero-config defaults
		resolvedImage = "pil"
		resolvedVideo = "none"
		if mediaCfg.AudioProvider == "off" {
			resolvedAudio = "off"
		}
	}

	// Capabilities with configured booleans
	caps := map[string]map[string]bool{
		"pil":    {"image": true},
		"openai": {"image": true, "configured": false},
		"fal":    {"image": true, "video": true, "configured": false},
	}
	if mediaRegistry != nil {
		// Use registry's configured checks (key presence)
		// Check via provider health? Use config fields
		innerCfg := mediaRegistry.Config()
		if innerCfg.OpenAIImageKey != "" {
			caps["openai"]["configured"] = true
		}
		if innerCfg.FalKey != "" {
			caps["fal"]["configured"] = true
		}
	} else {
		if mediaCfg.OpenAIImageKey != "" {
			caps["openai"]["configured"] = true
		}
		if mediaCfg.FalKey != "" {
			caps["fal"]["configured"] = true
		}
	}

	resp := map[string]interface{}{
		"image_provider": mediaCfg.ImageProvider,
		"video_provider": mediaCfg.VideoProvider,
		"audio_provider": mediaCfg.AudioProvider,
		"resolved_image": resolvedImage,
		"resolved_video": resolvedVideo,
		"resolved_audio": resolvedAudio,
		"capabilities":   caps,
		"output_dir":     mediaCfg.OutputDir,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// MediaManifest handles GET /api/media/manifest (last 20 entries).
func MediaManifest(w http.ResponseWriter, r *http.Request) {
	var cfg *config.Config
	cfg, _ = config.LoadConfig(config.ResolveConfigPath("config.json"))
	outputDir := "outputs/media"
	if cfg != nil {
		if cfg.Media.OutputDir != "" {
			outputDir = cfg.Media.OutputDir
		}
	}
	if mediaRegistry != nil {
		outputDir = mediaRegistry.Store().Root()
		// Store root is absolute; need manifest path
		// If absolute, use join
	}
	manifestPath := filepath.Join(outputDir, "manifest.jsonl")
	// If outputDir is absolute, Join will handle
	if filepath.IsAbs(outputDir) {
		manifestPath = filepath.Join(outputDir, "manifest.jsonl")
	} else {
		// Try to resolve via working dir
		if !strings.HasPrefix(manifestPath, "outputs") {
			manifestPath = filepath.Join(outputDir, "manifest.jsonl")
		}
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}
		http.Error(w, "failed to read manifest", http.StatusInternalServerError)
		return
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = []string{}
	}
	// Last 20
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	var entries []json.RawMessage
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		var raw json.RawMessage = json.RawMessage(l)
		// Validate JSON
		if !json.Valid([]byte(l)) {
			continue
		}
		entries = append(entries, raw)
	}
	if entries == nil {
		entries = []json.RawMessage{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}
