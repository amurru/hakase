// piper.go - local speech synthesis via the Piper TTS CLI
// (docs/telegram-voice/spec.md TV-001). The seam is implemented but NOT
// wired into any transport yet (spec decision [D1]: STT ships first; TTS is
// the stretch phase). Piper today means the maintained piper-tts CLI
// (OHF-Voice/piper1-gpl — the original C++ rhasspy/piper was archived
// 2025-10); the classic `-m <voice> -f <out.wav>` stdin-text invocation is
// kept, with binary/voice paths overridable in config to absorb CLI drift.
package speech

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"amurru/hakase/internal/util"
)

// PiperTTS synthesizes text with a local piper binary and converts the WAV
// to OGG/Opus for Telegram voice notes.
type PiperTTS struct {
	cfg TTSConfig
}

// resolvePiperBin finds the Piper CLI: the configured path/name first, then
// the alternative package names — distros ship it as `piper` (upstream,
// rhasspy/piper) or `piper-tts` (e.g. Arch's piper-tts-bin). An explicitly
// configured absolute path is used as-is (no silent fallback); a lookup
// failure of the default name falls through to the alternatives.
func resolvePiperBin(configured string) string {
	var candidates []string
	if configured != "" && configured != "piper" && configured != "piper-tts" {
		// Explicit custom path: honor it exactly.
		if p, err := exec.LookPath(configured); err == nil {
			return p
		}
		return ""
	}
	for _, name := range []string{"piper", "piper-tts"} {
		candidates = append(candidates, name)
	}
	for _, name := range candidates {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// piperBin resolves the CLI or reports an actionable error.
func (p *PiperTTS) piperBin() (string, error) {
	if bin := resolvePiperBin(p.cfg.BinaryPath); bin != "" {
		return bin, nil
	}
	return "", fmt.Errorf("speech: piper not found (pip install piper-tts, pacman -S piper-tts-bin, or set channels.telegram.text_to_speech.binary_path)")
}

// NewPiperTTS builds a synthesizer from the config (defaults applied).
func NewPiperTTS(cfg TTSConfig) *PiperTTS {
	return &PiperTTS{cfg: cfg.resolved()}
}

// Availability names exactly what is missing: piper binary, ffmpeg, or the
// voice model file.
func (p *PiperTTS) Availability() error {
	if !p.cfg.voiceIsConfigured() {
		return fmt.Errorf("speech: text_to_speech.voice_path must point to a Piper .onnx voice model (download from huggingface.co/rhasspy/piper-voices)")
	}
	if _, err := os.Stat(p.cfg.VoicePath); err != nil {
		return fmt.Errorf("speech: piper voice model %s not found: %w", p.cfg.VoicePath, err)
	}
	if _, err := exec.LookPath(p.cfg.FFMpegPath); err != nil {
		return fmt.Errorf("speech: ffmpeg not found (install ffmpeg, or set channels.telegram.text_to_speech.ffmpeg_path)")
	}
	if _, err := p.piperBin(); err != nil {
		return err
	}
	return nil
}

// Synthesize renders text to OGG/Opus: piper → WAV → ffmpeg → OGG
// (Telegram voice notes require OGG/Opus). All intermediate files live in a
// temp directory removed on return.
func (p *PiperTTS) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if err := p.Availability(); err != nil {
		return nil, err
	}
	if text == "" {
		return nil, fmt.Errorf("speech: nothing to synthesize")
	}

	dir, err := os.MkdirTemp("", "hakase-tts-")
	if err != nil {
		return nil, fmt.Errorf("speech: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	// piper: text on stdin → WAV on disk.
	pipbin, err := p.piperBin()
	if err != nil {
		return nil, err
	}
	wavPath := filepath.Join(dir, "speech.wav")
	pipcmd := &exec.Cmd{Path: pipbin, Args: []string{pipbin,
		"-m", p.cfg.VoicePath, "-f", wavPath,
	}}
	pipcmd.Stdin = strings.NewReader(text)
	if out, err := util.CombinedOutputContext(ctx, pipcmd); err != nil {
		return nil, fmt.Errorf("speech: piper synthesis: %w: %s", err, tailRunOutput(string(out)))
	}

	// ffmpeg: WAV → OGG/Opus (Telegram voice-note container).
	ffbin, err := exec.LookPath(p.cfg.FFMpegPath)
	if err != nil {
		return nil, fmt.Errorf("speech: ffmpeg not found (install ffmpeg, or set channels.telegram.text_to_speech.ffmpeg_path)")
	}
	oggPath := filepath.Join(dir, "speech.ogg")
	ffcmd := &exec.Cmd{Path: ffbin, Args: []string{ffbin,
		"-y", "-i", wavPath, "-c:a", "libopus", "-b:a", "32k", oggPath,
	}}
	if out, err := util.CombinedOutputContext(ctx, ffcmd); err != nil {
		return nil, fmt.Errorf("speech: ffmpeg ogg encode: %w: %s", err, tailRunOutput(string(out)))
	}

	ogg, err := os.ReadFile(oggPath)
	if err != nil {
		return nil, fmt.Errorf("speech: read synthesized audio: %w", err)
	}
	return ogg, nil
}
