// voice_test.go - inbound voice-note pipeline tests with a fake transcriber
// (docs/telegram-voice/spec.md TV-005): download served by an httptest file
// server, echo-verification before the run, degrade paths.
package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/speech"

	"github.com/go-telegram/bot/models"
	"google.golang.org/genai"
)

// fakeTranscriber scripts transcription outcomes.
type fakeTranscriber struct {
	transcript string
	lang       string
	err        error
}

func (f *fakeTranscriber) Transcribe(_ context.Context, _ []byte, _ string, _ int) (speech.Transcript, error) {
	return speech.Transcript{Text: f.transcript, Language: f.lang}, f.err
}

func (f *fakeTranscriber) Availability() error { return nil }

// recordingDriver captures the prompt content handed to RunTurn and records
// how many Telegram sends had already happened when the turn started (the
// echo must precede the run).
type recordingDriver struct {
	mu             sync.Mutex
	api            *fakeAPI
	content        *genai.Content
	sendsBeforeRun int
}

func (d *recordingDriver) RunTurn(_ context.Context, _ string, content *genai.Content, sink agentrun.EventSink) {
	d.mu.Lock()
	d.content = content
	d.sendsBeforeRun = len(d.api.sends())
	d.mu.Unlock()
	sink.OnDone("sess")
}

func voiceMessage(userID int64, durationSec int) *models.Message {
	m := privateMessage(userID, "")
	m.Voice = &models.Voice{
		FileID:   "voice-file-1",
		Duration: durationSec,
		MimeType: "audio/ogg",
	}
	return m
}

// newVoiceTestBot pairs user 200, attaches a fake transcriber backed by a
// fake file server, and returns the pieces the tests assert on.
func newVoiceTestBot(t *testing.T, tr *fakeTranscriber) (*Bot, *fakeAPI, *recordingDriver) {
	t.Helper()
	b, api, _, _ := newTestBot(t)

	code, err := b.auth.EnsurePairingCode()
	if err != nil {
		t.Fatalf("pairing code: %v", err)
	}
	b.handleMessage(context.Background(), privateMessage(200, "/start "+code))
	if !b.auth.IsAllowed(200) {
		t.Fatal("pairing failed in voice test setup")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("fake-ogg-bytes"))
	}))
	t.Cleanup(ts.Close)

	b.transcriber = tr
	b.voiceQueue = speech.NewQueue(3)
	b.stt = config.TelegramSTTConfig{MaxSeconds: 120, TimeoutSeconds: 5}
	b.fileBaseURL = ts.URL

	d := &recordingDriver{api: api}
	b.driver = d
	return b, api, d
}

func TestVoiceHappyPathEchoesThenRuns(t *testing.T) {
	tr := &fakeTranscriber{transcript: "what is the capital of France"}
	b, api, d := newVoiceTestBot(t, tr)

	b.handleMessage(context.Background(), voiceMessage(200, 5))
	waitRunDone(t, b, rootConv(200))

	sends := api.sends()
	var heard int
	for _, s := range sends {
		if strings.Contains(s.text, "🎙 Heard:") && strings.Contains(s.text, "what is the capital of France") {
			heard++
		}
	}
	if heard != 1 {
		t.Fatalf("expected exactly one echo with the transcript, sends: %+v", sends)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.content == nil {
		t.Fatal("no run was started")
	}
	if len(d.content.Parts) != 1 || d.content.Parts[0].Text != "what is the capital of France" {
		t.Fatalf("run prompt should be the transcript, got %+v", d.content.Parts)
	}
	if d.sendsBeforeRun < 1 || !strings.Contains(api.sends()[d.sendsBeforeRun-1].text, "🎙 Heard:") {
		t.Fatalf("echo must be sent before the run starts; sends before run = %d", d.sendsBeforeRun)
	}
}

func TestVoiceSetupHintWhenDisabled(t *testing.T) {
	// No transcriber attached: the default (speech_to_text disabled) shape.
	b, api, _, _ := newTestBot(t)
	code, err := b.auth.EnsurePairingCode()
	if err != nil {
		t.Fatalf("pairing code: %v", err)
	}
	b.handleMessage(context.Background(), privateMessage(200, "/start "+code))

	b.handleMessage(context.Background(), voiceMessage(200, 5))

	sends := api.sends()
	if len(sends) == 0 || !strings.Contains(sends[len(sends)-1].text, "not transcribed yet") {
		t.Fatalf("expected the setup hint, sends: %+v", sends)
	}
}

func TestVoiceTooLongRefused(t *testing.T) {
	tr := &fakeTranscriber{transcript: "x"}
	b, api, d := newVoiceTestBot(t, tr)
	b.stt.MaxSeconds = 10

	b.handleMessage(context.Background(), voiceMessage(200, 30))

	sends := api.sends()
	if len(sends) == 0 || !strings.Contains(sends[len(sends)-1].text, "limit is 10s") {
		t.Fatalf("expected the duration refusal, sends: %+v", sends)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.content != nil {
		t.Fatal("no run should start for an over-length voice note")
	}
}

func TestVoiceBusyAndFailures(t *testing.T) {
	t.Run("busy queue", func(t *testing.T) {
		b, api, d := newVoiceTestBot(t, &fakeTranscriber{err: speech.ErrBusy})
		b.handleMessage(context.Background(), voiceMessage(200, 5))
		sends := api.sends()
		if len(sends) == 0 || !strings.Contains(sends[len(sends)-1].text, "queue is full") {
			t.Fatalf("expected the busy message, sends: %+v", sends)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.content != nil {
			t.Fatal("no run should start on busy")
		}
	})

	t.Run("transcription failure", func(t *testing.T) {
		b, api, _ := newVoiceTestBot(t, &fakeTranscriber{err: context.DeadlineExceeded})
		b.handleMessage(context.Background(), voiceMessage(200, 5))
		sends := api.sends()
		if len(sends) == 0 || !strings.Contains(sends[len(sends)-1].text, "Transcription failed") {
			t.Fatalf("expected the failure message, sends: %+v", sends)
		}
	})

	t.Run("empty transcript", func(t *testing.T) {
		b, api, _ := newVoiceTestBot(t, &fakeTranscriber{})
		b.handleMessage(context.Background(), voiceMessage(200, 5))
		sends := api.sends()
		if len(sends) == 0 || !strings.Contains(sends[len(sends)-1].text, "couldn't hear anything") {
			t.Fatalf("expected the nothing-heard message, sends: %+v", sends)
		}
	})
}
