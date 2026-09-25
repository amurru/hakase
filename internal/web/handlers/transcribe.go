package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/speech"
)

const (
	// maxTranscribeBody is the hard cap on the whole multipart request.
	// ParseMultipartForm's argument is only an in-memory threshold (larger
	// parts spill to temp files), so it is NOT a limit: without
	// MaxBytesReader an authenticated client could stream an arbitrarily
	// large body and have it read fully into memory below.
	maxTranscribeBody = 25 << 20
	// transcribeMemory is how much of the upload is kept in RAM before the
	// parser spills the rest to a temp file.
	transcribeMemory = 4 << 20
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
	// Hard-cap the request body before parsing so an oversized upload is
	// refused rather than buffered.
	r.Body = http.MaxBytesReader(w, r.Body, maxTranscribeBody)
	if err := r.ParseMultipartForm(transcribeMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "audio upload too large (limit 25 MiB)", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to parse multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing \"file\" parameter in multipart form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if header.Size > maxTranscribeBody {
		http.Error(w, "audio upload too large (limit 25 MiB)", http.StatusRequestEntityTooLarge)
		return
	}

	// Bounded even though MaxBytesReader already caps the body: this is the
	// read that actually allocates, so it carries its own limit.
	audioBytes, err := io.ReadAll(io.LimitReader(file, maxTranscribeBody))
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
	modelTimeout := 0
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
		modelTimeout = stt.ModelTimeoutSeconds
	}
	// Fail closed on the length bound. With no config.json the block above is
	// the zero value, and an unset max_seconds must not mean "transcribe
	// whatever the client uploads" - an unauthenticated-length upload is
	// exactly the resource-exhaustion path this endpoint must not offer.
	if sttCfg.MaxSeconds <= 0 {
		sttCfg.MaxSeconds = config.DefaultSTTMaxSeconds
	}
	// Fail closed on the time bound for the same reason. This handler builds
	// the STTConfig by hand, so when config.json is absent the block above left
	// TimeoutSeconds at 0 - and speech.WithTimeout reads 0 as "no wrapper",
	// which would silently leave the work unbounded. r.Context() cancels only
	// when the client goes away, so a stalled ffmpeg or whisper-cli would
	// otherwise keep running for as long as the browser holds the socket open.
	if sttCfg.TimeoutSeconds <= 0 {
		sttCfg.TimeoutSeconds = config.DefaultSTTTimeout
	}
	// The model download gets its own, much larger budget. It is a network
	// transfer of tens to hundreds of MiB, so bounding it by the transcription
	// timeout would leave a slow connection unable to ever finish a first run.
	if modelTimeout <= 0 {
		modelTimeout = config.DefaultSTTModelTimeout
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

	// Fetch the model before the transcription budget starts. Transcribe would
	// do this internally, but by then it is already running under tctx, so a
	// first-use download would be cut off at timeout_seconds. After this
	// returns, the internal call hits the os.Stat fast path.
	dctx, dcancel := speech.WithTimeout(r.Context(), modelTimeout)
	modelErr := whisper.EnsureModel(dctx)
	dcancel()
	if modelErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": modelErr.Error(),
		})
		return
	}

	tctx, cancel := speech.WithTimeout(r.Context(), sttCfg.TimeoutSeconds)
	defer cancel()

	tr, err := whisper.Transcribe(tctx, audioBytes, mimeType, 0)
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
