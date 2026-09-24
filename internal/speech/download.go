// download.go - one-time whisper model fetch (docs/telegram-voice/spec.md
// TV-001). The ONLY outbound network touch in the speech package: audio
// bytes never leave the machine. Atomic install (tmp+rename, 0600) with a
// size sanity floor so a 404 HTML page never masquerades as a ggml model.
package speech

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// modelMinBytes is the sanity floor for a downloaded ggml file (the
// smallest quantized models are tens of MiB); a var so tests can lower it.
var modelMinBytes int64 = 5 << 20 // 5 MiB

// downloadClient is a var for test injection.
var downloadClient = &http.Client{}

// ensureModel returns the path to the configured ggml model, downloading it
// on first use (missing file). Concurrent callers are serialized upstream by
// the transcription queue.
func (w *WhisperCLI) ensureModel(ctx context.Context) (string, error) {
	path := w.cfg.modelPath()
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path, nil
	}
	url := w.cfg.modelURL()
	if err := os.MkdirAll(w.cfg.ModelsDir, 0700); err != nil {
		return "", fmt.Errorf("speech: model dir: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("speech: model download request: %w", err)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("speech: downloading whisper model %q from %s: %w (offline? download the ggml file manually into channels.telegram.speech_to_text.models_dir)", w.cfg.Model, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("speech: whisper model download for %q got HTTP %d from %s", w.cfg.Model, resp.StatusCode, url)
	}

	tmp, err := os.CreateTemp(w.cfg.ModelsDir, ".dl-*.tmp")
	if err != nil {
		return "", fmt.Errorf("speech: model temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("speech: whisper model download interrupted: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("speech: model sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("speech: model temp close: %w", err)
	}
	if n < modelMinBytes {
		return "", fmt.Errorf("speech: whisper model download for %q looks wrong (%d bytes, want at least %d) — refusing to use it", w.cfg.Model, n, modelMinBytes)
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		return "", fmt.Errorf("speech: model chmod: %w", err)
	}
	final := w.cfg.modelPath()
	if err := os.Rename(tmpName, final); err != nil {
		return "", fmt.Errorf("speech: model install: %w", err)
	}
	return final, nil
}
