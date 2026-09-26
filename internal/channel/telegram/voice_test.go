// voice_test.go - inbound voice-note pipeline tests with a fake transcriber
// (docs/telegram-voice/spec.md TV-005): download served by an httptest file
// server, echo-verification before the run, degrade paths.
package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	// modelErr fails the model warm-up.
	modelErr error
	// sawModelBudget records how much time EnsureModel's context was given,
	// so a test can pin which budget the download runs under.
	sawModelBudget time.Duration
	// sawModelBudgetSet distinguishes "no deadline" from "0".
	sawModelBudgetSet bool
}

func (f *fakeTranscriber) Transcribe(_ context.Context, _ []byte, _ string, _ int) (speech.Transcript, error) {
	return speech.Transcript{Text: f.transcript, Language: f.lang}, f.err
}

func (f *fakeTranscriber) Availability() error { return nil }

func (f *fakeTranscriber) EnsureModel(ctx context.Context) error {
	if dl, ok := ctx.Deadline(); ok {
		f.sawModelBudget = time.Until(dl)
		f.sawModelBudgetSet = true
	}
	return f.modelErr
}

// recordingDriver captures the prompt content handed to RunTurn.
type recordingDriver struct {
	mu      sync.Mutex
	api     *fakeAPI
	content *genai.Content
}

func (d *recordingDriver) RunTurn(_ context.Context, _ string, content *genai.Content, sink agentrun.EventSink) {
	d.mu.Lock()
	d.content = content
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
	// The echo is sent synchronously in handleVoice BEFORE startRun spawns
	// the run goroutine — but the status line races in between, so assert
	// existence rather than a strict position.
	echoFound := false
	for _, s := range api.sends() {
		if strings.Contains(s.text, "🎙 Heard:") && strings.Contains(s.text, "what is the capital of France") {
			echoFound = true
		}
	}
	if !echoFound {
		t.Fatalf("echo missing from sends: %+v", api.sends())
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

// TestVoiceModelDownloadUsesItsOwnBudget pins the separation on the Telegram
// path, mirroring the web handler. The first-use ggml pull is tens to hundreds
// of MiB over the network, so running it under timeout_seconds would leave a
// slow connection permanently unable to finish a first voice note.
func TestVoiceModelDownloadUsesItsOwnBudget(t *testing.T) {
	tr := &fakeTranscriber{transcript: "hello"}
	b, _, _ := newVoiceTestBot(t, tr)
	b.stt = config.TelegramSTTConfig{MaxSeconds: 120, TimeoutSeconds: 5, ModelTimeoutSeconds: 900}

	b.handleVoice(context.Background(), rootConv(200), voiceMessage(200, 5))

	if !tr.sawModelBudgetSet {
		t.Fatal("EnsureModel was called with no deadline, so the download is unbounded")
	}
	// Generous tolerance: this only has to distinguish the model budget from
	// the 5s transcription budget.
	if tr.sawModelBudget < 800*time.Second {
		t.Fatalf("model download got %s of budget, want ~900s (not the 5s transcription timeout)", tr.sawModelBudget)
	}
}

// TestVoiceModelDownloadBudgetFailsClosed pins the default. model_timeout_seconds
// is optional, and WithTimeout treats 0 as "no wrapper", so an unset value
// must not mean an unbounded download.
func TestVoiceModelDownloadBudgetFailsClosed(t *testing.T) {
	tr := &fakeTranscriber{transcript: "hello"}
	b, _, _ := newVoiceTestBot(t, tr)
	// ModelTimeoutSeconds deliberately left at 0.
	b.stt = config.TelegramSTTConfig{MaxSeconds: 120, TimeoutSeconds: 5}

	b.handleVoice(context.Background(), rootConv(200), voiceMessage(200, 5))

	if !tr.sawModelBudgetSet {
		t.Fatal("unset model_timeout_seconds left the download unbounded")
	}
	if tr.sawModelBudget < 1500*time.Second {
		t.Fatalf("expected the %ds default, got %s", config.DefaultSTTModelTimeout, tr.sawModelBudget)
	}
}

// TestVoiceModelDownloadFailureDoesNotLeak pins the same CWE-209 property the
// web handler has: the download error embeds the configured model_url_base and
// on-disk model paths, and a chat can contain people the operator would not
// show internal hosts to. The detail goes to the log, not the chat.
func TestVoiceModelDownloadFailureDoesNotLeak(t *testing.T) {
	tr := &fakeTranscriber{
		transcript: "hello",
		modelErr: errors.New(`speech: downloading whisper model "base-q5_1" from ` +
			`http://mirror.internal.corp/ggml-base-q5_1.bin: dial timeout`),
	}
	b, api, _ := newVoiceTestBot(t, tr)
	b.stt = config.TelegramSTTConfig{MaxSeconds: 120, TimeoutSeconds: 5, ModelTimeoutSeconds: 900}

	b.handleVoice(context.Background(), rootConv(200), voiceMessage(200, 5))

	sends := api.sends()
	if len(sends) == 0 {
		t.Fatal("expected a reply when the model download fails")
	}
	for _, s := range sends {
		if strings.Contains(s.text, "mirror.internal.corp") {
			t.Fatalf("chat reply leaked the model URL: %q", s.text)
		}
	}
	// The operator still gets an actionable line, without the internals.
	var found bool
	for _, s := range sends {
		if strings.Contains(s.text, "server logs") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a reply pointing at the server logs, got: %v", sends)
	}
}
