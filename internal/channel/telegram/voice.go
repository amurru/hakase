// voice.go - inbound voice notes: download → local whisper.cpp transcription
// → echo-verification → ordinary prompt (issue #19,
// docs/telegram-voice/spec.md TV-003). Mirrors the photo path's degrade-
// gracefully posture: missing tooling yields an actionable hint, never a run
// failure and never silence. Audio bytes stay local and are never persisted —
// the transcript is the only trace (spec decision [D5]).
package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"amurru/hakase/internal/speech"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// maxVoiceBytes caps a voice-note download at the Bot API getFile ceiling
// (20 MB); Telegram voice notes are OGG/Opus and rarely exceed a few MB.
const maxVoiceBytes = 20 << 20

// voiceSetupHint is sent when STT is disabled in config (actionable, names
// the config block and the binaries).
const voiceSetupHint = "🎙 Voice notes are not transcribed yet.\n\nEnable voice by setting <code>channels.telegram.speech_to_text.enabled = true</code> in config.json and installing:\n• ffmpeg (audio decode)\n• whisper.cpp — <a href=\"https://github.com/ggml-org/whisper.cpp\">ggml-org/whisper.cpp</a> (local transcription; the model auto-downloads on first use)\n\nUntil then, text works exactly as before. See docs/telegram-voice/."

// transcriber is the speech seam (satisfied by *speech.WhisperCLI; tests
// substitute a fake).
type transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mime string, durationSec int) (string, error)
	Availability() error
}

// handleVoice runs the inbound voice pipeline.
func (b *Bot) handleVoice(ctx context.Context, c conv, m *models.Message) {
	v := m.Voice
	if v == nil {
		return
	}
	if b.transcriber == nil {
		b.sendText(ctx, c, voiceSetupHint, nil, false)
		return
	}
	if b.stt.MaxSeconds > 0 && v.Duration > b.stt.MaxSeconds {
		b.sendText(ctx, c, fmt.Sprintf("🎙 That voice note is %ds — the limit is %ds (channels.telegram.speech_to_text.max_seconds). Trim it or raise the cap.", v.Duration, b.stt.MaxSeconds), nil, false)
		return
	}
	if err := b.transcriber.Availability(); err != nil {
		b.sendText(ctx, c, "🎙 Voice transcription is not usable right now: "+esc(err.Error()), nil, false)
		return
	}

	audio, err := b.downloadVoice(ctx, v.FileID)
	if err != nil {
		b.log("voice download failed: %v", err)
		b.sendText(ctx, c, "⚠️ Could not download the voice note: "+esc(err.Error()), nil, false)
		return
	}

	// Serialized transcription: one at a time (CPU-bound), bounded queue,
	// bounded duration (speech_to_text.timeout_seconds).
	var transcript string
	var terr error
	err = b.voiceQueue.Run(ctx, func(ctx context.Context) error {
		tctx, cancel := speech.WithTimeout(ctx, b.stt.TimeoutSeconds)
		defer cancel()
		transcript, terr = b.transcriber.Transcribe(tctx, audio, v.MimeType, v.Duration)
		return terr
	})
	switch {
	case errors.Is(err, speech.ErrBusy):
		b.sendText(ctx, c, "🎙 The transcription queue is full — try again in a moment.", nil, false)
		return
	case err != nil:
		b.log("voice transcription failed: %v", err)
		b.sendText(ctx, c, "⚠️ Transcription failed: "+esc(err.Error()), nil, false)
		return
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		b.sendText(ctx, c, "🎙 I couldn't hear anything intelligible in that voice note.", nil, false)
		return
	}

	// Echo-verification BEFORE the run: a mis-transcription can be stopped
	// with /stop (issue step 4 — no confirmation gate).
	echo := transcript
	if len(echo) > 800 {
		echo = echo[:800] + "…"
	}
	b.sendText(ctx, c, "🎙 Heard:\n"+esc(echo), nil, false)

	b.startRun(ctx, c, m.ID, transcript, nil, nil, nil)
}

// downloadVoice fetches the voice file via getFile + the bot file URL,
// mirroring the photo path. bytes are capped at maxVoiceBytes.
func (b *Bot) downloadVoice(ctx context.Context, fileID string) ([]byte, error) {
	f, err := b.api.GetFile(ctx, &tgbot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("getFile: %w", err)
	}
	if f == nil || f.FilePath == "" {
		return nil, errors.New("empty file path from Telegram")
	}
	url := b.fileBaseURL + "/file/bot" + b.token + "/" + f.FilePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("file download got HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxVoiceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxVoiceBytes {
		return nil, errors.New("voice note exceeds the 20 MB Bot API download limit")
	}
	return data, nil
}
