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
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
		return fmt.Errorf("speech: ffmpeg not found (install ffmpeg, or set speech_to_text.ffmpeg_path)")
	}
	if _, err := exec.LookPath(w.cfg.BinaryPath); err != nil {
		return fmt.Errorf("speech: whisper-cli not found (build whisper.cpp — github.com/ggml-org/whisper.cpp: cmake -B build && cmake --build build — or set speech_to_text.binary_path)")
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
		return Transcript{}, fmt.Errorf("speech: ffmpeg not found (install ffmpeg, or set speech_to_text.ffmpeg_path)")
	}
	wavPath := filepath.Join(dir, "audio.wav")
	ffArgs := []string{ffbin,
		"-y", "-i", inPath, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le",
	}
	if w.cfg.MaxSeconds > 0 {
		// Decode at most max_seconds+1s. That is enough to tell an
		// over-long clip from an acceptable one while bounding decode CPU
		// and disk on a hostile upload. The truncation is never observable:
		// the length check below refuses such an input outright.
		ffArgs = append(ffArgs, "-t", strconv.Itoa(w.cfg.MaxSeconds+1))
	}
	ffArgs = append(ffArgs, wavPath)
	ffcmd := &exec.Cmd{Path: ffbin, Args: ffArgs}
	if out, err := util.CombinedOutputContext(ctx, ffcmd); err != nil {
		return Transcript{}, fmt.Errorf("speech: ffmpeg decode: %w: %s", err, tailRunOutput(string(out)))
	}

	// Enforce max_seconds on the decoded audio, not just on a
	// caller-supplied duration. Some transports (the web dictation
	// endpoint) have no duration to pass, and treating "unknown" as "fine"
	// let an unbounded clip reach whisper inference. The decode above was
	// already capped, so this measures the decoded WAV and refuses before
	// the expensive step.
	if w.cfg.MaxSeconds > 0 {
		secs, err := wavDurationSeconds(wavPath)
		if err != nil {
			return Transcript{}, fmt.Errorf("speech: probe duration: %w", err)
		}
		if secs > w.cfg.MaxSeconds {
			return Transcript{}, fmt.Errorf("%w: %ds > max_seconds %d", ErrTooLong, secs, w.cfg.MaxSeconds)
		}
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
		return Transcript{}, fmt.Errorf("speech: whisper-cli not found (build whisper.cpp — github.com/ggml-org/whisper.cpp: cmake -B build && cmake --build build — or set speech_to_text.binary_path)")
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
	text := strings.TrimSpace(string(raw))
	// whisper annotates non-speech audio with bracketed tags or bare
	// ellipses ("[ Inaudible ]", "[BLANK_AUDIO]", "[music]", "…"). Those are
	// not words: report an empty transcript so the transport tells the user
	// to try again instead of prompting the LLM with garbage.
	if nonSpeechRe.MatchString(text) {
		return Transcript{Language: detectedLanguage(base + ".json")}, nil
	}
	return Transcript{
		Text:     text,
		Language: detectedLanguage(base + ".json"),
	}, nil
}

// wavDurationSeconds returns the playing time of a PCM WAV file, in whole
// seconds, by walking the RIFF chunk list to the data chunk and dividing its
// size by the byte rate declared in the fmt chunk. Reading the declared rate
// (rather than assuming 16 kHz mono 16-bit) keeps the result correct if the
// decode step's format ever changes. Chunks are word-aligned, so an odd-sized
// chunk is followed by one pad byte that must be skipped.
func wavDurationSeconds(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	hdr := make([]byte, 12)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return 0, err
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return 0, fmt.Errorf("not a RIFF/WAVE file")
	}

	byteRate := 0
	for {
		chdr := make([]byte, 8)
		if _, err := io.ReadFull(f, chdr); err != nil {
			return 0, fmt.Errorf("truncated chunk header: %w", err)
		}
		id := string(chdr[0:4])
		size := int(binary.LittleEndian.Uint32(chdr[4:8]))
		pad := size % 2

		switch id {
		case "fmt ":
			if size < 16 {
				return 0, fmt.Errorf("fmt chunk too short (%d bytes)", size)
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(f, body); err != nil {
				return 0, fmt.Errorf("truncated fmt chunk: %w", err)
			}
			byteRate = int(binary.LittleEndian.Uint32(body[8:12]))
			if pad == 1 {
				if _, err := f.Seek(1, io.SeekCurrent); err != nil {
					return 0, err
				}
			}
		case "data":
			if byteRate <= 0 {
				return 0, fmt.Errorf("data chunk precedes a usable fmt chunk")
			}
			return size / byteRate, nil
		default:
			if _, err := f.Seek(int64(size+pad), io.SeekCurrent); err != nil {
				return 0, err
			}
		}
	}
}

// nonSpeechRe matches a transcript that is ENTIRELY a whisper non-speech
// annotation (bracketed tag or bare ellipsis). Whole-transcript only: a
// bracketed tag inside real speech stays.
var nonSpeechRe = regexp.MustCompile(`(?i)^\s*(\[[^\]]{0,40}\]|\.\.+|…+)\s*$`)

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
