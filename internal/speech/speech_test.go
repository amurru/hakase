// speech_test.go - the local speech pipeline with FAKE binaries: no real
// whisper.cpp/piper/ffmpeg is needed (docs/telegram-voice/spec.md TV-005).
// Fake binaries are shell scripts on a temp dir; ffmpeg-writes/output-file
// behavior is scripted per test.
package speech

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFakeBin drops an executable shell script into dir and returns its
// path (usable directly as a config binary path — LookPath accepts
// absolute paths with the exec bit).
func writeFakeBin(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return path
}

// fakeLastArgWriter writes content to the LAST argument path (the output
// file for both ffmpeg invocations). POSIX sh only: `for last do` iterates
// positional params, leaving `last` on the final one — CI's /bin/sh is dash
// and rejects bash-isms like ${*: -1}.
const fakeLastArgWriter = `for last do :; done
printf 'fake-bytes' > "$last"
`

// fakeWhisperOf parses "-of <base>" and writes "<base>.txt" with fixed text
// plus "<base>.json" with a detected language (exercising the -oj parse).
const fakeWhisperOf = `base=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-of" ]; then base="$a"; fi
  prev="$a"
done
printf 'hello transcript' > "$base.txt"
printf '{"result":{"language":"de"}}' > "$base.json"
`

// fakeWhisperPlain is the language-less variant (no JSON emitted): the
// pipeline must degrade to an empty detected language.
const fakeWhisperPlain = `base=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-of" ]; then base="$a"; fi
  prev="$a"
done
printf 'hello transcript' > "$base.txt"
`

// skipWindows skips tests that execute shell-script fake binaries: windows
// CI has no /bin/sh and LookPath requires .exe/.bat extensions there, so
// the fake-binary pipeline mechanics only translate to POSIX (mirrors the
// #22 windows-suite convention).
func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binaries are POSIX-only")
	}
}

func TestTranscribePipelineWithFakes(t *testing.T) {
	skipWindows(t)
	binDir := t.TempDir()
	modelsDir := t.TempDir()
	ffmpeg := writeFakeBin(t, binDir, "ffmpeg", fakeLastArgWriter)
	whisper := writeFakeBin(t, binDir, "whisper-cli", fakeWhisperOf)

	// Pre-seed the model so the download path is not exercised here.
	modelPath := filepath.Join(modelsDir, "ggml-basemodel.bin")
	if err := os.WriteFile(modelPath, []byte("ggml-fake"), 0o600); err != nil {
		t.Fatalf("seed model: %v", err)
	}

	w := NewWhisperCLI(STTConfig{
		Model:      "basemodel",
		Language:   "auto",
		BinaryPath: whisper,
		FFMpegPath: ffmpeg,
		ModelsDir:  modelsDir,
	})
	if err := w.Availability(); err != nil {
		t.Fatalf("availability: %v", err)
	}
	text, err := w.Transcribe(context.Background(), []byte("fake-ogg-bytes"), "audio/ogg", 7)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if text.Text != "hello transcript" {
		t.Fatalf("transcript %q, want %q", text.Text, "hello transcript")
	}
	if text.Language != "de" {
		t.Fatalf("detected language %q, want de (from the -oj JSON)", text.Language)
	}
}

func TestAvailabilityNamesMissingBinary(t *testing.T) {
	skipWindows(t)
	binDir := t.TempDir()
	ffmpeg := writeFakeBin(t, binDir, "ffmpeg", fakeLastArgWriter)
	w := NewWhisperCLI(STTConfig{
		Model:      "basemodel",
		BinaryPath: filepath.Join(binDir, "whisper-cli-missing"),
		FFMpegPath: ffmpeg,
		ModelsDir:  t.TempDir(),
	})
	err := w.Availability()
	if err == nil || !strings.Contains(err.Error(), "whisper-cli not found") {
		t.Fatalf("availability error should name whisper-cli, got: %v", err)
	}

	w2 := NewWhisperCLI(STTConfig{
		Model:      "bad model name!",
		BinaryPath: "whisper-cli",
		FFMpegPath: "ffmpeg",
		ModelsDir:  t.TempDir(),
	})
	if err := w2.Availability(); err == nil || !strings.Contains(err.Error(), "model name") {
		t.Fatalf("expected model-name validation error, got: %v", err)
	}
	w3 := NewWhisperCLI(STTConfig{Model: "basemodel", Language: "not a lang!!", BinaryPath: "whisper-cli", FFMpegPath: "ffmpeg", ModelsDir: t.TempDir()})
	if err := w3.Availability(); err == nil || !strings.Contains(err.Error(), "language") {
		t.Fatalf("expected language validation error, got: %v", err)
	}
}

func TestTranscribeDurationCap(t *testing.T) {
	skipWindows(t)
	binDir := t.TempDir()
	w := NewWhisperCLI(STTConfig{
		Model:      "basemodel",
		BinaryPath: writeFakeBin(t, binDir, "whisper-cli", fakeWhisperOf),
		FFMpegPath: writeFakeBin(t, binDir, "ffmpeg", fakeLastArgWriter),
		ModelsDir:  t.TempDir(),
		MaxSeconds: 5,
	})
	_, err := w.Transcribe(context.Background(), []byte("x"), "audio/ogg", 30)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
}

func TestModelAutoDownload(t *testing.T) {
	skipWindows(t)
	oldFloor := modelMinBytes
	modelMinBytes = 8
	t.Cleanup(func() { modelMinBytes = oldFloor })

	var hits int
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		if r.URL.Path != "/ggml-basemodel.bin" {
			t.Errorf("unexpected model path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte("0123456789ABCDEF")) // 16 bytes ≥ lowered floor
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	binDir := t.TempDir()
	w := NewWhisperCLI(STTConfig{
		Model:        "basemodel",
		BinaryPath:   writeFakeBin(t, binDir, "whisper-cli", fakeWhisperOf),
		FFMpegPath:   writeFakeBin(t, binDir, "ffmpeg", fakeLastArgWriter),
		ModelsDir:    modelsDir,
		ModelURLBase: ts.URL,
	})

	text, err := w.Transcribe(context.Background(), []byte("x"), "audio/ogg", 1)
	if err != nil {
		t.Fatalf("Transcribe with download: %v", err)
	}
	if text.Text != "hello transcript" {
		t.Fatalf("transcript %q", text.Text)
	}
	mu.Lock()
	got := hits
	mu.Unlock()
	if got != 1 {
		t.Fatalf("model downloaded %d times, want exactly 1", got)
	}
	// Second run: cached, no second download.
	if _, err := w.Transcribe(context.Background(), []byte("x"), "audio/ogg", 1); err != nil {
		t.Fatalf("second Transcribe: %v", err)
	}
	mu.Lock()
	got = hits
	mu.Unlock()
	if got != 1 {
		t.Fatalf("model re-downloaded: %d hits, want 1", got)
	}

	// Installed file is 0600.
	info, err := os.Stat(filepath.Join(modelsDir, "ggml-basemodel.bin"))
	if err != nil {
		t.Fatalf("stat model: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Errorf("model mode %v, want 0600", info.Mode().Perm())
	}
}

func TestModelDownloadRejectsTinyPayload(t *testing.T) {
	skipWindows(t)
	oldFloor := modelMinBytes
	modelMinBytes = 1 << 20
	t.Cleanup(func() { modelMinBytes = oldFloor })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not found</html>"))
	}))
	defer ts.Close()

	binDir := t.TempDir()
	w := NewWhisperCLI(STTConfig{
		Model:        "basemodel",
		BinaryPath:   writeFakeBin(t, binDir, "whisper-cli", fakeWhisperOf),
		FFMpegPath:   writeFakeBin(t, binDir, "ffmpeg", fakeLastArgWriter),
		ModelsDir:    t.TempDir(),
		ModelURLBase: ts.URL,
	})
	_, err := w.Transcribe(context.Background(), []byte("x"), "audio/ogg", 1)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("expected size-sanity refusal, got: %v", err)
	}
}

func TestQueueSerializesAndRefusesWhenFull(t *testing.T) {
	// Depth 2 = 1 running + 1 waiting.
	q := NewQueue(2)

	release := make(chan struct{})
	run1Done := make(chan struct{})
	run2Done := make(chan struct{})
	entered := make(chan struct{})

	// Caller 1 takes the worker slot and blocks inside fn.
	go func() {
		defer close(run1Done)
		_ = q.Run(context.Background(), func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	// Caller 2 takes the only waiting slot.
	go func() {
		defer close(run2Done)
		_ = q.Run(context.Background(), func(context.Context) error { return nil })
	}()
	// Give caller 2 a beat to take the slot before the third arrives.
	time.Sleep(20 * time.Millisecond)

	if err := q.Run(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("third caller err = %v, want ErrBusy", err)
	}

	close(release)
	<-run1Done
	select {
	case <-run2Done:
	case <-time.After(2 * time.Second):
		t.Fatal("queued caller never ran")
	}
}

func TestPiperBinaryNameFallback(t *testing.T) {
	skipWindows(t)
	// The default CLI name "piper" is missing; distros shipping the binary
	// as "piper-tts" (e.g. Arch piper-tts-bin) must still resolve.
	binDir := t.TempDir()
	voice := filepath.Join(t.TempDir(), "voice.onnx")
	if err := os.WriteFile(voice, []byte("onnx"), 0o600); err != nil {
		t.Fatalf("voice: %v", err)
	}
	piperTTS := writeFakeBin(t, binDir, "piper-tts", `out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-f" ]; then out="$a"; fi
  prev="$a"
done
printf 'RIFFfake-wav' > "$out"
`)
	ffmpeg := writeFakeBin(t, binDir, "ffmpeg", fakeLastArgWriter)
	t.Setenv("PATH", binDir) // only binDir is searchable

	p := NewPiperTTS(TTSConfig{BinaryPath: "", VoicePath: voice, FFMpegPath: ffmpeg})
	if err := p.Availability(); err != nil {
		t.Fatalf("availability should fall back to piper-tts, got: %v", err)
	}
	if _, err := p.Synthesize(context.Background(), "hello", ""); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	_ = piperTTS
}

// fakePiperMarker writes "voice:<model arg>" into its -f output, so a test
// can tell WHICH voice model the synthesizer selected.
const fakePiperMarker = `m=""
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-m" ]; then m="$a"; fi
  if [ "$prev" = "-f" ]; then out="$a"; fi
  prev="$a"
done
printf 'voice:%s' "$m" > "$out"
`

// fakeFFMpegCopy copies the -i input to the last argument, passing the
// piper marker through to the "ogg" so tests can read it back.
const fakeFFMpegCopy = `in=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-i" ]; then in="$a"; fi
  prev="$a"
done
for last do :; done
cp "$in" "$last"
`

// TestPiperVoiceLanguageSelection pins the multilingual behavior: a
// configured per-language voice is used when the language matches; an
// unconfigured language, an empty language, and a configured language with
// a missing file all fall back to the default voice.
func TestPiperVoiceLanguageSelection(t *testing.T) {
	skipWindows(t)
	binDir := t.TempDir()
	voiceEn := filepath.Join(t.TempDir(), "en_US-amy-medium.onnx")
	voiceDe := filepath.Join(t.TempDir(), "de_DE-thorsten-medium.onnx")
	for _, v := range []string{voiceEn, voiceDe} {
		if err := os.WriteFile(v, []byte("onnx"), 0o600); err != nil {
			t.Fatalf("voice seed: %v", err)
		}
	}
	p := NewPiperTTS(TTSConfig{
		BinaryPath: writeFakeBin(t, binDir, "piper", fakePiperMarker),
		VoicePath:  voiceEn,
		Voices:     map[string]string{"de": voiceDe},
		FFMpegPath: writeFakeBin(t, binDir, "ffmpeg", fakeFFMpegCopy),
	})

	deOGG, err := p.Synthesize(context.Background(), "hallo", "de")
	if err != nil {
		t.Fatalf("synthesize de: %v", err)
	}
	if !strings.Contains(string(deOGG), "de_DE-thorsten-medium.onnx") {
		t.Fatalf("de request used the wrong voice: %q", deOGG)
	}

	frOGG, err := p.Synthesize(context.Background(), "bonjour", "fr")
	if err != nil {
		t.Fatalf("synthesize fr: %v", err)
	}
	if !strings.Contains(string(frOGG), "en_US-amy-medium.onnx") {
		t.Fatalf("fr request did not fall back to the default voice: %q", frOGG)
	}

	defOGG, err := p.Synthesize(context.Background(), "hello", "")
	if err != nil {
		t.Fatalf("synthesize default: %v", err)
	}
	if !strings.Contains(string(defOGG), "en_US-amy-medium.onnx") {
		t.Fatalf("default request did not use the default voice: %q", defOGG)
	}

	// A configured language whose file is missing on disk also falls back.
	p2 := NewPiperTTS(TTSConfig{
		BinaryPath: writeFakeBin(t, binDir, "piper", fakePiperMarker),
		VoicePath:  voiceEn,
		Voices:     map[string]string{"ja": filepath.Join(t.TempDir(), "ja_JP-missing.onnx")},
		FFMpegPath: writeFakeBin(t, binDir, "ffmpeg", fakeFFMpegCopy),
	})
	jpOGG, err := p2.Synthesize(context.Background(), "hello", "ja")
	if err != nil {
		t.Fatalf("synthesize ja: %v", err)
	}
	if !strings.Contains(string(jpOGG), "en_US-amy-medium.onnx") {
		t.Fatalf("missing voice file did not fall back: %q", jpOGG)
	}
}

func TestPiperSeamWithFakes(t *testing.T) {
	skipWindows(t)
	binDir := t.TempDir()
	voice := filepath.Join(t.TempDir(), "voice.onnx")
	if err := os.WriteFile(voice, []byte("onnx"), 0o600); err != nil {
		t.Fatalf("voice: %v", err)
	}
	piper := writeFakeBin(t, binDir, "piper", `out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-f" ]; then out="$a"; fi
  prev="$a"
done
printf 'RIFFfake-wav' > "$out"
`)
	p := NewPiperTTS(TTSConfig{
		BinaryPath: piper,
		VoicePath:  voice,
		FFMpegPath: writeFakeBin(t, binDir, "ffmpeg", fakeLastArgWriter),
	})
	ogg, err := p.Synthesize(context.Background(), "hello there", "")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(ogg) == 0 {
		t.Fatal("empty ogg output")
	}
	if err := p.Availability(); err != nil {
		t.Fatalf("availability: %v", err)
	}

	// Missing voice file → actionable availability error.
	missing := NewPiperTTS(TTSConfig{BinaryPath: piper, VoicePath: filepath.Join(t.TempDir(), "nope.onnx"), FFMpegPath: "ffmpeg"})
	if err := missing.Availability(); err == nil || !strings.Contains(err.Error(), "voice model") {
		t.Fatalf("expected voice-model error, got: %v", err)
	}
}
