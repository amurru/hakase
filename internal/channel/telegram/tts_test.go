// tts_test.go - voice-reply wiring tests (docs/telegram-voice/spec.md
// TV-004): /voice mode persistence, off/auto/on semantics, stream
// suppression, and the synthesis-failure text fallback.
package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/speech"

	"google.golang.org/genai"
)

// fakeSynthesizer scripts synthesis outcomes and records what was spoken
// and in which language.
type fakeSynthesizer struct {
	mu       sync.Mutex
	lastText string
	lastLang string
	calls    int
	err      error
}

func (f *fakeSynthesizer) Synthesize(_ context.Context, text, lang string) ([]byte, error) {
	f.mu.Lock()
	f.lastText = text
	f.lastLang = lang
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return []byte("OGGDATA"), nil
}

func (f *fakeSynthesizer) Availability() error { return nil }

func (f *fakeSynthesizer) spoken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastText
}

func (f *fakeSynthesizer) spokenLang() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastLang
}

func (f *fakeSynthesizer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// streamingDriver answers every turn with one fixed text delta.
type streamingDriver struct {
	text string
}

func (d *streamingDriver) RunTurn(_ context.Context, sessionID string, _ *genai.Content, sink agentrun.EventSink) {
	sink.OnStream(sessionID, d.text, "")
	sink.OnDone(sessionID)
}

// newTTSTestBot pairs user 200, attaches the synthesizer, and returns the
// pieces the TTS tests assert on.
func newTTSTestBot(t *testing.T, synth *fakeSynthesizer) (*Bot, *fakeAPI, *streamingDriver) {
	t.Helper()
	b, api, _, _ := newTestBot(t)

	code, err := b.auth.EnsurePairingCode()
	if err != nil {
		t.Fatalf("pairing code: %v", err)
	}
	b.handleMessage(context.Background(), privateMessage(200, "/start "+code))
	if !b.auth.IsAllowed(200) {
		t.Fatal("pairing failed in tts test setup")
	}

	b.synthesizer = synth
	b.ttsMaxChars = config.DefaultTelegramTTSMaxChars
	d := &streamingDriver{text: "The capital of France is **Paris**."}
	b.driver = d
	return b, api, d
}

func setVoiceMode(t *testing.T, b *Bot, mode string) {
	t.Helper()
	b.handleMessage(context.Background(), privateMessage(200, "/voice "+mode))
}

func TestTTSOnModeReplacesTextWithVoice(t *testing.T) {
	synth := &fakeSynthesizer{}
	b, api, _ := newTTSTestBot(t, synth)
	setVoiceMode(t, b, "on")

	b.handleMessage(context.Background(), privateMessage(200, "what is the capital of France?"))
	waitRunDone(t, b, rootConv(200))

	voices := api.voiceSends()
	if len(voices) != 1 || voices[0].bytes == 0 {
		t.Fatalf("expected exactly one voice send with bytes, got %+v", voices)
	}
	// The text answer must be suppressed: nothing in the text sends may
	// carry the answer (status lines carry only tool/elapsed text).
	for _, s := range api.sends() {
		if strings.Contains(s.text, "Paris") {
			t.Fatalf("answer text leaked into text sends in voice mode: %q", s.text)
		}
	}
	// The synthesizer received markdown-stripped speech.
	if got := synth.spoken(); got != "The capital of France is Paris." {
		t.Fatalf("spoken text %q, want markdown stripped", got)
	}
}

func TestTTSAutoSpeaksOnlyVoiceTurns(t *testing.T) {
	synth := &fakeSynthesizer{}
	b, api, _ := newTTSTestBot(t, synth)
	setVoiceMode(t, b, "auto")

	// A text turn keeps its text answer.
	b.handleMessage(context.Background(), privateMessage(200, "typed question"))
	waitRunDone(t, b, rootConv(200))
	if got := len(api.voiceSends()); got != 0 {
		t.Fatalf("auto mode spoke a text turn (%d voice sends)", got)
	}
	found := false
	for _, s := range api.sends() {
		if strings.Contains(s.text, "Paris") {
			found = true
		}
	}
	if !found {
		t.Fatal("text answer missing in auto mode after a text turn")
	}

	// A voice turn gets the voice reply: attach the STT fakes and send one.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("fake-ogg-bytes"))
	}))
	defer ts.Close()
	b.transcriber = &fakeTranscriber{transcript: "spoken question", lang: "en"}
	b.voiceQueue = speech.NewQueue(3)
	b.stt = config.TelegramSTTConfig{MaxSeconds: 120, TimeoutSeconds: 5}
	b.fileBaseURL = ts.URL

	b.handleMessage(context.Background(), voiceMessage(200, 3))
	waitRunDone(t, b, rootConv(200))
	if got := len(api.voiceSends()); got != 1 {
		t.Fatalf("auto mode did not speak the voice turn: %d voice sends", got)
	}
}

// TestTTSVoiceLanguageMirrors pins the multilingual loop: whisper's
// detected language rides through the turn and reaches the synthesizer, so
// the reply can be spoken with a matching voice.
func TestTTSVoiceLanguageMirrors(t *testing.T) {
	synth := &fakeSynthesizer{}
	b, api, _ := newTTSTestBot(t, synth)
	setVoiceMode(t, b, "auto")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("fake-ogg-bytes"))
	}))
	defer ts.Close()
	b.transcriber = &fakeTranscriber{transcript: "wo ist der Bahnhof?", lang: "de"}
	b.voiceQueue = speech.NewQueue(3)
	b.stt = config.TelegramSTTConfig{MaxSeconds: 120, TimeoutSeconds: 5}
	b.fileBaseURL = ts.URL

	b.handleMessage(context.Background(), voiceMessage(200, 3))
	waitRunDone(t, b, rootConv(200))

	if got := len(api.voiceSends()); got != 1 {
		t.Fatalf("expected the German voice turn to be spoken, got %d voice sends", got)
	}
	if got := synth.spokenLang(); got != "de" {
		t.Fatalf("synthesizer language = %q, want de (mirrored)", got)
	}
}

func TestTTSOffByDefault(t *testing.T) {
	synth := &fakeSynthesizer{}
	b, api, _ := newTTSTestBot(t, synth)

	b.handleMessage(context.Background(), privateMessage(200, "question"))
	waitRunDone(t, b, rootConv(200))

	if got := len(api.voiceSends()); got != 0 {
		t.Fatalf("no voice expected with mode off, got %d", got)
	}
	if got := synth.callCount(); got != 0 {
		t.Fatalf("synthesizer called %d times with mode off", got)
	}
	found := false
	for _, s := range api.sends() {
		if strings.Contains(s.text, "Paris") {
			found = true
		}
	}
	if !found {
		t.Fatal("text answer missing with mode off")
	}
}

func TestTTSSynthesisFailureFallsBackToText(t *testing.T) {
	synth := &fakeSynthesizer{err: errors.New("voice model missing")}
	b, api, _ := newTTSTestBot(t, synth)
	setVoiceMode(t, b, "on")

	b.handleMessage(context.Background(), privateMessage(200, "question"))
	waitRunDone(t, b, rootConv(200))

	if got := len(api.voiceSends()); got != 0 {
		t.Fatalf("no voice expected on synthesis failure, got %d", got)
	}
	found := false
	for _, s := range api.sends() {
		if strings.Contains(s.text, "Paris") {
			found = true
		}
	}
	if !found {
		t.Fatal("text fallback missing after synthesis failure — the answer must never be lost")
	}
}

func TestVoiceCommandPersistsAndValidates(t *testing.T) {
	synth := &fakeSynthesizer{}
	b, api, _ := newTTSTestBot(t, synth)
	ctx := context.Background()

	b.handleMessage(ctx, privateMessage(200, "/voice xyz"))
	if last := api.sends(); len(last) == 0 || !strings.Contains(last[len(last)-1].text, "Usage") {
		t.Fatalf("expected usage message, sends: %+v", last)
	}

	b.handleMessage(ctx, privateMessage(200, "/voice on"))
	if got := b.store.Get().Chats[chatKey(200)].VoiceMode; got != "on" {
		t.Fatalf("voice mode = %q, want on", got)
	}

	b.handleMessage(ctx, privateMessage(200, "/voice auto"))
	if got := b.store.Get().Chats[chatKey(200)].VoiceMode; got != "auto" {
		t.Fatalf("voice mode = %q, want auto", got)
	}

	// Bare /voice reports the current mode.
	b.handleMessage(ctx, privateMessage(200, "/voice"))
	last := api.sends()
	if len(last) == 0 || !strings.Contains(last[len(last)-1].text, "auto") {
		t.Fatalf("expected a status message naming the mode, sends: %+v", last)
	}

	b.handleMessage(ctx, privateMessage(200, "/voice off"))
	if got := b.store.Get().Chats[chatKey(200)].VoiceMode; got != "off" {
		t.Fatalf("voice mode = %q, want off", got)
	}
}

func TestTTSMaxCharsTruncates(t *testing.T) {
	synth := &fakeSynthesizer{}
	b, _, _ := newTTSTestBot(t, synth)
	b.ttsMaxChars = 20
	setVoiceMode(t, b, "on")

	b.handleMessage(context.Background(), privateMessage(200, "question"))
	waitRunDone(t, b, rootConv(200))

	if got := synth.spoken(); !strings.HasSuffix(got, "[truncated for voice]") {
		t.Fatalf("spoken text not truncated: %q", got)
	}
}
