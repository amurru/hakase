// Package speech provides fully-local speech pipelines for hakase
// (docs/telegram-voice/spec.md, issue #19): whisper.cpp transcription in,
// optional Piper synthesis out. Transport-neutral so the Telegram transport
// today and a web attach path later share one implementation.
//
// Posture (mirrors internal/vision's optional-binary handling): every
// external binary is a config path resolved with exec.LookPath at use time,
// Availability() names exactly what to install, and no audio bytes ever
// leave the machine. The single outbound network touch is the one-time
// whisper model download (injectable URL base for offline/mirrored setups).
package speech

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrBusy reports a full transcription queue (the transport turns it into a
// "try again in a moment" message).
var ErrBusy = errors.New("speech: transcription queue is full")

// ErrTooLong reports a voice note longer than the configured cap.
var ErrTooLong = errors.New("speech: voice note longer than the configured cap")

// Transcript is one transcription result: the spoken text plus the language
// whisper detected for it (ISO code like "de"; empty when unknown). The
// language lets a voice reply MIRROR the caller's language when the
// synthesizer has a matching voice configured.
type Transcript struct {
	Text     string
	Language string
}

// Transcriber turns audio bytes into text, fully local.
type Transcriber interface {
	// Transcribe converts the audio payload (Telegram voice notes are
	// OGG/Opus; mime drives the temp-file extension) into a Transcript. An
	// empty Text with nil error means nothing intelligible was heard.
	Transcribe(ctx context.Context, audio []byte, mime string, durationSec int) (Transcript, error)
	// Availability returns nil when the pipeline could run right now;
	// otherwise an error naming what to install or configure.
	Availability() error
}

// STTConfig configures the whisper.cpp transcriber. Zero-value paths
// resolve to conventional names; ModelsDir empty means
// ~/.hakase/models/whisper.
type STTConfig struct {
	// Model is the ggml model name without the ggml- prefix/.bin suffix,
	// e.g. "base-q5_1".
	Model string
	// Language is "auto" (detect) or an ISO code passed to whisper-cli -l.
	Language string
	// BinaryPath is the whisper-cli binary (default "whisper-cli").
	BinaryPath string
	// FFMpegPath is the ffmpeg binary (default "ffmpeg").
	FFMpegPath string
	// ModelsDir holds ggml model files (default ~/.hakase/models/whisper).
	ModelsDir string
	// MaxSeconds caps accepted voice-note duration (0 = no cap here; the
	// transport usually enforces its own guard first).
	MaxSeconds int
	// TimeoutSeconds bounds one transcription (default 180).
	TimeoutSeconds int
	// ModelURLBase overrides the download base for offline/mirrored setups
	// (default the whisper.cpp models repo on HuggingFace).
	ModelURLBase string
}

// DefaultModelURLBase is where ggml models auto-download from (the
// whisper.cpp models repo on HuggingFace).
const DefaultModelURLBase = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main"

// defaultModelsDir is ~/.hakase/models/whisper (HakaseHome-aware), resolved
// lazily because HOME may be redirected after init.
func defaultModelsDir() string {
	home := HakaseHome()
	if home == "" {
		return "models/whisper"
	}
	return filepath.Join(home, "models", "whisper")
}

// validModelName guards the model-name join into URLs and file paths.
var validModelName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// resolved fills defaults; used internally by the implementations.
func (c STTConfig) resolved() STTConfig {
	if c.Model == "" {
		c.Model = "base-q5_1"
	}
	if c.Language == "" {
		c.Language = "auto"
	}
	if c.BinaryPath == "" {
		c.BinaryPath = "whisper-cli"
	}
	if c.FFMpegPath == "" {
		c.FFMpegPath = "ffmpeg"
	}
	if c.ModelsDir == "" {
		c.ModelsDir = defaultModelsDir()
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 180
	}
	if c.ModelURLBase == "" {
		c.ModelURLBase = DefaultModelURLBase
	}
	return c
}

// modelPath is the on-disk ggml file for the configured model.
func (c STTConfig) modelPath() string {
	return filepath.Join(c.ModelsDir, "ggml-"+c.Model+".bin")
}

// modelURL is the download URL for the configured model.
func (c STTConfig) modelURL() string {
	return c.ModelURLBase + "/ggml-" + c.Model + ".bin"
}

// mimeExtension maps an audio MIME type to a temp-file extension (ffmpeg
// sniffs content anyway; the extension just helps human debugging).
func mimeExtension(mime string) string {
	switch {
	case strings.Contains(mime, "ogg"):
		return ".ogg"
	case strings.Contains(mime, "mpeg"), strings.Contains(mime, "mp3"):
		return ".mp3"
	case strings.Contains(mime, "mp4"), strings.Contains(mime, "m4a"):
		return ".m4a"
	case strings.Contains(mime, "wav"):
		return ".wav"
	case strings.Contains(mime, "webm"):
		return ".webm"
	default:
		return ".ogg"
	}
}

// HakaseHome is a var so tests (and a future HAKASE_HOME change) can
// redirect where the whisper models land; defaults to ~/.hakase.
var HakaseHome = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".hakase")
}

// Synthesizer turns text into OGG/Opus audio bytes, fully local. lang is
// the target language (ISO code from the transcriber, or "" for the
// default voice); when no voice is configured for it the default voice
// speaks instead.
type Synthesizer interface {
	// Synthesize renders the text to OGG/Opus for Telegram voice notes.
	Synthesize(ctx context.Context, text, lang string) ([]byte, error)
	// Availability returns nil when synthesis could run right now.
	Availability() error
}

// TTSConfig configures the Piper synthesizer.
type TTSConfig struct {
	// BinaryPath is the piper CLI (default "piper", with "piper-tts" as a
	// fallback name).
	BinaryPath string
	// VoicePath is the DEFAULT .onnx voice model file (required) — used
	// whenever no per-language voice matches.
	VoicePath string
	// Voices maps an ISO language code to that language's .onnx voice, so
	// replies can mirror the language whisper detected on the inbound voice
	// note. Languages without an entry (or whose file is missing) fall back
	// to VoicePath.
	Voices map[string]string
	// FFMpegPath is the ffmpeg binary (default "ffmpeg") for WAV→OGG.
	FFMpegPath string
}

// resolved fills defaults for the synthesizer config.
func (c TTSConfig) resolved() TTSConfig {
	if c.BinaryPath == "" {
		c.BinaryPath = "piper"
	}
	if c.FFMpegPath == "" {
		c.FFMpegPath = "ffmpeg"
	}
	return c
}

// voiceIsConfigured reports whether a usable voice model path is set.
func (c TTSConfig) voiceIsConfigured() bool {
	return strings.HasSuffix(c.VoicePath, ".onnx")
}
