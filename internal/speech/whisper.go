// whisper.go - local transcription via whisper.cpp's whisper-cli, fed by
// ffmpeg-decoded 16 kHz mono WAV (docs/telegram-voice/spec.md TV-001).
//
// Exec safety (same pattern as internal/vision's SVG rasterizers): binaries
// are resolved with exec.LookPath up front and invoked via &exec.Cmd{Path,
// Args} struct literals — no shell is ever involved, and no dynamic argv is
// ever assembled from request data. Every argv value is either a literal
// flag, a fixed filename inside the temp directory this package creates, or
// a validated config setting (model file name charset-checked, language
// code regexp-checked — "auto" is a native whisper-cli value, so -l is
// always passed literally). Audio CONTENT — including the sender-declared
// MIME type — never enters argv: the payload is written to one fixed-name
// file and ffmpeg sniffs the format from the bytes.
package speech

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"amurru/hakase/internal/util"
)

// validLanguage constrains the whisper-cli -l value to language codes or
// the literal "auto" (whisper-cli's native auto-detect).
var validLanguage = regexp.MustCompile(`^auto$|^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{1,16})*$`)

// WhisperCLI transcribes audio with a local whisper.cpp build. The zero
// value is not usable; construct with NewWhisperCLI.
type WhisperCLI struct {
	cfg STTConfig
}

// NewWhisperCLI builds a transcriber from the config (defaults applied).
func NewWhisperCLI(cfg STTConfig) *WhisperCLI {
	return &WhisperCLI{cfg: cfg.resolved()}
}

// Availability names exactly what is missing: ffmpeg, the whisper-cli
// binary, or a malformed model/language setting. A missing MODEL FILE is
// not an error — it auto-downloads on first transcription.
func (w *WhisperCLI) Availability() error {
	if !validModelName.MatchString(w.cfg.Model) {
		return fmt.Errorf("speech: invalid whisper model name %q (want e.g. base-q5_1)", w.cfg.Model)
	}
	if !validLanguage.MatchString(w.cfg.Language) {
		return fmt.Errorf("speech: invalid whisper language %q (want \"auto\" or an ISO code like en/de/zh)", w.cfg.Language)
	}
	if _, err := exec.LookPath(w.cfg.FFMpegPath); err != nil {
		return fmt.Errorf("speech: ffmpeg not found (install ffmpeg, or set channels.telegram.speech_to_text.ffmpeg_path)")
	}
	if _, err := exec.LookPath(w.cfg.BinaryPath); err != nil {
		return fmt.Errorf("speech: whisper-cli not found (build whisper.cpp — github.com/ggml-org/whisper.cpp: cmake -B build && cmake --build build — or set channels.telegram.speech_to_text.binary_path)")
	}
	return nil
}

// Transcribe runs the local pipeline: write audio → ffmpeg to 16 kHz mono
// WAV → whisper-cli → read the transcript + detected language. All work
// happens in a temp directory that is removed on return; audio bytes never
// leave the machine. mime is accepted for interface stability but
// deliberately unused — ffmpeg detects the container from the bytes, so no
// remote-declared value ever reaches a filename or argv.
func (w *WhisperCLI) Transcribe(ctx context.Context, audio []byte, mime string, durationSec int) (Transcript, error) {
	if err := ctx.Err(); err != nil {
		return Transcript{}, err
	}
	if w.cfg.MaxSeconds > 0 && durationSec > w.cfg.MaxSeconds {
		return Transcript{}, fmt.Errorf("%w: %ds > max_seconds %d", ErrTooLong, durationSec, w.cfg.MaxSeconds)
	}
	if err := w.Availability(); err != nil {
		return Transcript{}, err
	}

	dir, err := os.MkdirTemp("", "hakase-asr-")
	if err != nil {
		return Transcript{}, fmt.Errorf("speech: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	inPath := filepath.Join(dir, "input.audio")
	if err := os.WriteFile(inPath, audio, 0600); err != nil {
		return Transcript{}, fmt.Errorf("speech: write input: %w", err)
	}

	// ffmpeg: any input container → 16 kHz mono 16-bit WAV for whisper.
	ffbin, err := exec.LookPath(w.cfg.FFMpegPath)
	if err != nil {
		return Transcript{}, fmt.Errorf("speech: ffmpeg not found (install ffmpeg, or set channels.telegram.speech_to_text.ffmpeg_path)")
	}
	wavPath := filepath.Join(dir, "audio.wav")
	ffcmd := &exec.Cmd{Path: ffbin, Args: []string{ffbin,
		"-y", "-i", inPath, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wavPath,
	}}
	if out, err := util.CombinedOutputContext(ctx, ffcmd); err != nil {
		return Transcript{}, fmt.Errorf("speech: ffmpeg decode: %w: %s", err, tailRunOutput(string(out)))
	}

	modelPath, err := w.ensureModel(ctx)
	if err != nil {
		return Transcript{}, err
	}

	// whisper-cli: -l auto is the binary's native auto-detect, so the flag
	// is always passed and the argv stays fully literal. -oj emits the
	// result JSON whose result.language carries the detected language
	// (best-effort: voice mirroring degrades to the default voice).
	whbin, err := exec.LookPath(w.cfg.BinaryPath)
	if err != nil {
		return Transcript{}, fmt.Errorf("speech: whisper-cli not found (build whisper.cpp — github.com/ggml-org/whisper.cpp: cmake -B build && cmake --build build — or set channels.telegram.speech_to_text.binary_path)")
	}
	base := filepath.Join(dir, "transcript")
	whcmd := &exec.Cmd{Path: whbin, Args: []string{whbin,
		"-f", wavPath, "-m", modelPath, "-np", "-otxt", "-oj", "-of", base,
		"-l", w.cfg.Language,
	}}
	if out, err := util.CombinedOutputContext(ctx, whcmd); err != nil {
		return Transcript{}, fmt.Errorf("speech: whisper-cli: %w: %s", err, tailRunOutput(string(out)))
	}

	raw, err := os.ReadFile(base + ".txt")
	if err != nil {
		return Transcript{}, fmt.Errorf("speech: whisper transcript missing: %w", err)
	}
	return Transcript{
		Text:     strings.TrimSpace(string(raw)),
		Language: detectedLanguage(base + ".json"),
	}, nil
}

// whisperResultJSON is the slice of whisper-cli's -oj output the pipeline
// needs: the detected language.
type whisperResultJSON struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
}

// detectedLanguage extracts the language from a whisper -oj result file,
// "" when absent or unreadable (language mirroring is best-effort; the
// transcript itself is the deliverable).
func detectedLanguage(jsonPath string) string {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return ""
	}
	var parsed whisperResultJSON
	if json.Unmarshal(data, &parsed) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Result.Language))
}

// tailRunOutput keeps error messages readable (ffmpeg progress noise is
// long; the interesting part is at the end).
func tailRunOutput(out string) string {
	const max = 400
	s := strings.TrimSpace(out)
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}
