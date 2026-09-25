package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/speech"
)

// transcribeRouter is the minimal interface for transcribe routes.
type transcribeRouter interface {
	Post(pattern string, handlerFn http.HandlerFunc)
}

// RegisterTranscribeRoutes registers POST /api/transcribe inside the auth group.
func RegisterTranscribeRoutes(r transcribeRouter) {
	r.Post("/transcribe", Transcribe)
}

// TranscribeResponse is the JSON response returned by POST /api/transcribe.
type TranscribeResponse struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
}

// Transcribe handles POST /api/transcribe (auth-gated).
// It accepts a multipart form with an audio file under the "file" form field.
func Transcribe(w http.ResponseWriter, r *http.Request) {
	// Parse multipart form up to 25 MB
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		http.Error(w, "failed to parse multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing \"file\" parameter in multipart form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	audioBytes, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read audio file", http.StatusBadRequest)
		return
	}

	if len(audioBytes) == 0 {
		http.Error(w, "empty audio file", http.StatusBadRequest)
		return
	}

	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "audio/webm"
	}

	// Load speech config from config.json if available
	var sttCfg speech.STTConfig
	if cfg, err := config.LoadConfig(config.ResolveConfigPath("config.json")); err == nil {
		stt := cfg.SpeechToText
		sttCfg = speech.STTConfig{
			Model:          stt.Model,
			Language:       stt.Language,
			BinaryPath:     stt.BinaryPath,
			FFMpegPath:     stt.FFMpegPath,
			ModelsDir:      stt.ModelsDir,
			ModelURLBase:   stt.ModelURLBase,
			MaxSeconds:     stt.MaxSeconds,
			TimeoutSeconds: stt.TimeoutSeconds,
		}
	}

	whisper := speech.NewWhisperCLI(sttCfg)
	if err := whisper.Availability(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	tr, err := whisper.Transcribe(r.Context(), audioBytes, mimeType, 0)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(TranscribeResponse{
		Text:     tr.Text,
		Language: tr.Language,
	})
}
