package handlers

import (
	"bytes"
	"encoding/json"
	"io"
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
	// Prefer the live registry config (reflects env overrides); only hit the
	// config file when no registry is installed.
	var mediaCfg config.MediaConfig
	if mediaRegistry != nil {
		mediaCfg = mediaRegistry.Config() // already defaulted at construction
	} else if cfg, _ := config.LoadConfig(config.ResolveConfigPath("config.json")); cfg != nil {
		mediaCfg = cfg.Media
	}
	mediaCfg.ApplyDefaults()

	// Resolve providers via registry when available, else zero-config
	// defaults (pil guaranteed).
	resolvedImage := "pil"
	resolvedVideo := "none"
	resolvedAudio := "off"
	if mediaRegistry != nil {
		if p, err := mediaRegistry.Resolve("image"); err == nil {
			resolvedImage = p.Name()
		}
		if p, err := mediaRegistry.Resolve("video"); err == nil {
			resolvedVideo = p.Name()
		}
		if p, err := mediaRegistry.Resolve("audio"); err == nil {
			resolvedAudio = p.Name()
		} else if mediaCfg.AudioProvider != "off" {
			// Audio-off is the expected v1 state; anything else unresolvable
			// is reported as none.
			resolvedAudio = "none"
		}
	}

	// Capabilities expose a uniform key set for every provider so clients can
	// always read e.g. capabilities.pil.configured without undefined checks.
	caps := map[string]map[string]bool{
		"pil":    {"image": true, "configured": true},
		"openai": {"image": true, "video": true, "configured": false},
		"fal":    {"image": true, "video": true, "configured": false},
	}
	// mediaCfg mirrors the live registry config when one is installed.
	if mediaCfg.OpenAIImageKey != "" || mediaCfg.OpenAIVideoKey != "" {
		caps["openai"]["configured"] = true
	}
	if mediaCfg.FalKey != "" {
		caps["fal"]["configured"] = true
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

// manifestTailBytes bounds how many trailing bytes of manifest.jsonl are read
// per request; the file grows without bound over a long-running install, so
// it must never be read whole.
const manifestTailBytes = 64 << 10

// MediaManifest handles GET /api/media/manifest (last 20 entries).
func MediaManifest(w http.ResponseWriter, r *http.Request) {
	var outputDir string
	if mediaRegistry != nil {
		outputDir = mediaRegistry.Store().Root()
	} else if cfg, _ := config.LoadConfig(config.ResolveConfigPath("config.json")); cfg != nil && cfg.Media.OutputDir != "" {
		outputDir = cfg.Media.OutputDir
	} else {
		outputDir = "outputs/media"
	}
	manifestPath := filepath.Join(outputDir, "manifest.jsonl")

	f, err := os.Open(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}
		http.Error(w, "failed to read manifest", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	data, err := readManifestTail(f)
	if err != nil {
		http.Error(w, "failed to read manifest", http.StatusInternalServerError)
		return
	}

	// Drop the trailing newline (and any stray trailing whitespace) before
	// splitting so the final empty element cannot consume one of the 20
	// returned-entry slots.
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = []string{}
	}
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	var entries []json.RawMessage
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || !json.Valid([]byte(l)) {
			continue
		}
		entries = append(entries, json.RawMessage(l))
	}
	if entries == nil {
		entries = []json.RawMessage{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

// readManifestTail reads at most the final manifestTailBytes of f, discarding
// a leading partial line when the read started mid-file so only whole JSON
// objects are considered.
func readManifestTail(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if size := info.Size(); size > manifestTailBytes {
		start = size - manifestTailBytes
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, err
	}
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			// Single line larger than the window: nothing parseable inside.
			buf = nil
		}
	}
	return buf, nil
}
