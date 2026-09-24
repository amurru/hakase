// file_test.go - attached media files (audio / video / documents) go to the
// model as native inline parts; captions are never lost (issue #19
// follow-up: only voice NOTES are transcribed).
package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"google.golang.org/genai"
)

func newFileTestBot(t *testing.T) (*Bot, *fakeAPI, *recordingDriver) {
	t.Helper()
	b, api, _, _ := newTestBot(t)

	code, err := b.auth.EnsurePairingCode()
	if err != nil {
		t.Fatalf("pairing code: %v", err)
	}
	b.handleMessage(context.Background(), privateMessage(200, "/start "+code))
	if !b.auth.IsAllowed(200) {
		t.Fatal("pairing failed in file test setup")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("FILEBYTES"))
	}))
	t.Cleanup(ts.Close)
	b.fileBaseURL = ts.URL

	d := &recordingDriver{api: api}
	b.driver = d
	return b, api, d
}

func audioMessage(userID int64, caption string) *models.Message {
	m := privateMessage(userID, "")
	m.Caption = caption // media messages carry the prompt in Caption, not Text
	m.Audio = &models.Audio{FileID: "aud-1", MimeType: "audio/mpeg", Duration: 42}
	return m
}

func documentMessage(userID int64, caption, mime, name string) *models.Message {
	m := privateMessage(userID, "")
	m.Caption = caption
	m.Document = &models.Document{FileID: "doc-1", MimeType: mime, FileName: name}
	return m
}

func inlineParts(content *genai.Content) []*genai.Part {
	var out []*genai.Part
	for _, p := range content.Parts {
		if p.InlineData != nil {
			out = append(out, p)
		}
	}
	return out
}

func textParts(content *genai.Content) string {
	var sb strings.Builder
	for _, p := range content.Parts {
		if p.Text != "" {
			sb.WriteString(p.Text)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// TestAudioFilePassedToModel pins the directive: an attached audio FILE is
// a native inline part (not whisper), with the caption as the prompt.
func TestAudioFilePassedToModel(t *testing.T) {
	b, _, d := newFileTestBot(t)

	b.handleMessage(context.Background(), audioMessage(200, "what is in this clip?"))
	waitRunDone(t, b, rootConv(200))

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.content == nil {
		t.Fatal("no run started for the audio file")
	}
	in := inlineParts(d.content)
	if len(in) != 1 || in[0].InlineData.MIMEType != "audio/mpeg" {
		t.Fatalf("expected one audio/mpeg inline part, got %+v", d.content.Parts)
	}
	if string(in[0].InlineData.Data) != "FILEBYTES" {
		t.Fatalf("inline data %q, want the downloaded bytes", in[0].InlineData.Data)
	}
	if !strings.Contains(textParts(d.content), "what is in this clip?") {
		t.Fatalf("caption missing from the prompt: %q", textParts(d.content))
	}
}

// TestCaptionedDocumentNeverLost pins the silent-drop fix: a captioned
// document produces both the file part and the caption prompt.
func TestCaptionedDocumentNeverLost(t *testing.T) {
	b, _, d := newFileTestBot(t)

	b.handleMessage(context.Background(), documentMessage(200, "summarize this report", "application/pdf", "report.pdf"))
	waitRunDone(t, b, rootConv(200))

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.content == nil {
		t.Fatal("captioned document produced no run (the old silent drop)")
	}
	if len(inlineParts(d.content)) != 1 {
		t.Fatalf("expected the pdf inline part, got %+v", d.content.Parts)
	}
	if !strings.Contains(textParts(d.content), "summarize this report") {
		t.Fatalf("caption lost: %q", textParts(d.content))
	}
}

// TestDocumentWithoutCaptionStillRuns pins the captionless case: the file
// alone is enough of a prompt.
func TestDocumentWithoutCaptionStillRuns(t *testing.T) {
	b, _, d := newFileTestBot(t)

	b.handleMessage(context.Background(), documentMessage(200, "", "application/pdf", "report.pdf"))
	waitRunDone(t, b, rootConv(200))

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.content == nil {
		t.Fatal("captionless document produced no run")
	}
	if len(inlineParts(d.content)) != 1 {
		t.Fatalf("expected the pdf inline part, got %+v", d.content.Parts)
	}
}
