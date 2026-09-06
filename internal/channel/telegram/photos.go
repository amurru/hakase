package telegram

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"google.golang.org/genai"

	hakasesession "amurru/hakase/internal/session"
)

// Photo size and album buffering caps (photos on the public Bot API are far
// below the 20MB getFile ceiling, but the cap mirrors the web handler's
// inline-image budget). Album flush delay follows Hermes' brief-buffer
// pattern so a 3-photo burst becomes one prompt.
const (
	maxPhotoBytes     = 10 * 1024 * 1024
	mediaGroupFlushAt = 1500 * time.Millisecond
)

// mediaGroup buffers an album until the flush timer fires.
type mediaGroupBuf struct {
	photos   []photoAttach
	caption  string
	c        conv
	promptID int // first album message: receipts + pin anchor
	timer    *time.Timer
}

// photoAttach is one downloaded photo.
type photoAttach struct {
	data []byte
	name string
}

// handlePhoto routes a photo message: albums buffer briefly and flush as one
// prompt; single photos run immediately. A caption is required — it is the
// prompt (albums use their first caption).
func (b *Bot) handlePhoto(ctx context.Context, m *models.Message) {
	c := convFromMessage(m)
	if strings.TrimSpace(m.Caption) == "" && m.MediaGroupID == "" {
		b.sendText(ctx, c, "📸 Add a caption telling me what to do with the photo — the caption is the prompt.", nil, false)
		return
	}
	if m.MediaGroupID != "" {
		b.bufferAlbumPhoto(ctx, m)
		return
	}
	photo, err := b.downloadPhoto(ctx, m.Photo)
	if err != nil {
		b.sendText(ctx, c, "⚠️ "+err.Error(), nil, false)
		return
	}
	parts, refs, manifest := photoContent([]photoAttach{photo})
	b.startRun(ctx, c, m.ID, strings.TrimSpace(m.Caption), parts, refs, manifest)
}

// bufferAlbumPhoto accumulates an album's photos, flushing the whole group as
// one run when the timer fires.
func (b *Bot) bufferAlbumPhoto(ctx context.Context, m *models.Message) {
	photo, err := b.downloadPhoto(ctx, m.Photo)
	if err != nil {
		b.sendText(ctx, convFromMessage(m), "⚠️ "+err.Error(), nil, false)
		return
	}

	b.mediaMu.Lock()
	defer b.mediaMu.Unlock()
	group, ok := b.mediaGroup[m.MediaGroupID]
	if !ok {
		group = &mediaGroupBuf{c: convFromMessage(m)}
		b.mediaGroup[m.MediaGroupID] = group
		group.timer = time.AfterFunc(mediaGroupFlushAt, func() {
			b.flushMediaGroup(m.MediaGroupID)
		})
	}
	group.photos = append(group.photos, photo)
	if group.promptID == 0 {
		group.promptID = m.ID
	}
	if strings.TrimSpace(m.Caption) != "" {
		group.caption = strings.TrimSpace(m.Caption)
	}
}

// flushMediaGroup submits a buffered album as one prompt (detached context:
// the flush fires from a timer, not an update handler).
func (b *Bot) flushMediaGroup(groupID string) {
	b.mediaMu.Lock()
	group, ok := b.mediaGroup[groupID]
	delete(b.mediaGroup, groupID)
	b.mediaMu.Unlock()
	if !ok || len(group.photos) == 0 || group.c.chatID == 0 {
		return
	}
	parts, refs, manifest := photoContent(group.photos)
	b.startRun(context.Background(), group.c, group.promptID, group.caption, parts, refs, manifest)
}

// photoContent converts downloaded photos into genai parts, session
// attachment refs, and manifest lines (the pasted-image branch of the web
// handler's buildAttachmentParts).
func photoContent(photos []photoAttach) ([]*genai.Part, []hakasesession.AttachmentRef, []string) {
	parts := make([]*genai.Part, 0, len(photos))
	refs := make([]hakasesession.AttachmentRef, 0, len(photos))
	manifest := make([]string, 0, len(photos))
	for i, p := range photos {
		name := fmt.Sprintf("photo_%d.jpg", i+1)
		parts = append(parts, genai.NewPartFromBytes(p.data, "image/jpeg"))
		label := "@" + name
		refs = append(refs, hakasesession.AttachmentRef{
			Name:  name,
			Path:  "",
			MIME:  "image/jpeg",
			Label: label,
		})
		manifest = append(manifest, fmt.Sprintf("%s %s (pasted image/jpeg, %d KB)", label, name, len(p.data)/1024))
	}
	return parts, refs, manifest
}

// downloadPhoto fetches the largest photo variant via getFile + the file
// download URL.
func (b *Bot) downloadPhoto(ctx context.Context, sizes []models.PhotoSize) (photoAttach, error) {
	if len(sizes) == 0 {
		return photoAttach{}, fmt.Errorf("message carries no photo")
	}
	largest := sizes[len(sizes)-1]
	file, err := b.api.GetFile(ctx, &tgbot.GetFileParams{FileID: largest.FileID})
	if err != nil {
		return photoAttach{}, fmt.Errorf("cannot resolve file: %v", err)
	}
	if file.FilePath == "" {
		return photoAttach{}, fmt.Errorf("telegram returned no file path")
	}
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.token, file.FilePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return photoAttach{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return photoAttach{}, fmt.Errorf("cannot download photo: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return photoAttach{}, fmt.Errorf("photo download failed: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes+1))
	if err != nil {
		return photoAttach{}, fmt.Errorf("cannot read photo: %v", err)
	}
	if len(data) > maxPhotoBytes {
		return photoAttach{}, fmt.Errorf("photo too large (max %d MB)", maxPhotoBytes/(1024*1024))
	}
	return photoAttach{data: data, name: "photo.jpg"}, nil
}
