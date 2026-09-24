// file.go - attached media files (audio, video, documents): downloaded and
// attached to the prompt as native genai parts, so audio-capable /
// multimodal models receive the file itself (issue #19 follow-up: only
// voice NOTES are transcribed by whisper — attached files go to the
// configured model, which consumes them respectively). Photos already had
// this path (photos.go); this extends it to the other media message types,
// which were previously dropped silently or lost their caption.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	hakasesession "amurru/hakase/internal/session"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"google.golang.org/genai"
)

// maxFileBytes caps media downloads at the Bot API getFile ceiling (20 MB).
const maxFileBytes = 20 << 20

// handleFile is the shared inbound path for audio / video / document
// messages: the file becomes a native inline part (the model consumes it
// respectively — audio-capable models hear it, multimodal models see it),
// the caption (if any) is the prompt text. Download failures degrade to an
// actionable message; the caption is never lost.
func (b *Bot) handleFile(ctx context.Context, c conv, m *models.Message, fileID, fileName, mime, kind string) {
	data, err := b.downloadFile(ctx, fileID)
	if err != nil {
		b.log("%s download failed: %v", kind, err)
		b.sendText(ctx, c, "⚠️ Could not download the "+kind+": "+esc(err.Error()), nil, false)
		return
	}
	if mime == "" {
		mime = "application/octet-stream"
	}

	label := "@" + fileName
	parts := []*genai.Part{genai.NewPartFromBytes(data, mime)}
	refs := []hakasesession.AttachmentRef{{
		Name:  fileName,
		Path:  "",
		MIME:  mime,
		Label: label,
	}}
	manifest := []string{fmt.Sprintf("%s %s (attached %s, %d KB)", label, fileName, mime, len(data)/1024)}

	b.startRun(ctx, c, m.ID, strings.TrimSpace(m.Caption), parts, refs, manifest, "", false)
}

// handleAudioFile handles Telegram "music"/audio messages.
func (b *Bot) handleAudioFile(ctx context.Context, c conv, m *models.Message) {
	a := m.Audio
	name := a.FileName
	if name == "" && a.Title != "" {
		name = a.Title
	}
	if name == "" {
		name = fmt.Sprintf("audio-%ds", a.Duration)
	}
	b.handleFile(ctx, c, m, a.FileID, name, a.MimeType, "audio file")
}

// handleVideoFile handles video messages.
func (b *Bot) handleVideoFile(ctx context.Context, c conv, m *models.Message) {
	v := m.Video
	name := v.FileName
	if name == "" {
		name = fmt.Sprintf("video-%ds", v.Duration)
	}
	b.handleFile(ctx, c, m, v.FileID, name, v.MimeType, "video")
}

// handleDocumentFile handles files sent as documents.
func (b *Bot) handleDocumentFile(ctx context.Context, c conv, m *models.Message) {
	d := m.Document
	name := d.FileName
	if name == "" {
		name = "document"
	}
	b.handleFile(ctx, c, m, d.FileID, name, d.MimeType, "document")
}

// downloadFile fetches any Bot API file by id (shared by voice notes and
// attached media), capped at the getFile ceiling.
func (b *Bot) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
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
		// Transport errors embed the full request URL, which contains the
		// bot token — strip it before the error can reach the chat or log.
		return nil, errors.New(strings.ReplaceAll(err.Error(), b.token, "<redacted>"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("file download got HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, errors.New("file exceeds the 20 MB Bot API download limit")
	}
	return data, nil
}
