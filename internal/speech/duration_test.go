// duration_test.go - the max_seconds bound on the transcription pipeline.
//
// The bound has to hold even when the caller cannot supply a duration (the
// web dictation endpoint has no duration to pass, and treating "unknown" as
// "fine" let an arbitrarily long clip reach whisper inference). These tests
// pin that behavior plus the WAV duration probe it relies on.
package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testSampleRate = 16000
	testChannels   = 1
	testBits       = 16
)

// testByteRate is the fmt-chunk byte rate the pipeline's decode produces.
var testByteRate = testSampleRate * testChannels * testBits / 8 // 32000

// buildWAV writes a 16 kHz mono 16-bit PCM WAV of the given duration and
// returns its path. When withList is set a LIST chunk of odd size is emitted
// first, so the duration probe has to walk the chunk list and honour the
// word-alignment pad instead of assuming a bare 44-byte header.
func buildWAV(t *testing.T, dir string, seconds int, withList bool) string {
	t.Helper()

	data := bytes.Repeat([]byte{0x01, 0x02}, testByteRate*seconds/2)

	var body bytes.Buffer
	if withList {
		// Odd-sized chunk: RIFF pads it to an even boundary.
		list := []byte("INFOignored")
		body.WriteString("LIST")
		binary.Write(&body, binary.LittleEndian, uint32(len(list)))
		body.Write(list)
		body.WriteByte(0) // pad
	}
	body.WriteString("fmt ")
	binary.Write(&body, binary.LittleEndian, uint32(16))
	binary.Write(&body, binary.LittleEndian, uint16(1)) // PCM format
	binary.Write(&body, binary.LittleEndian, uint16(testChannels))
	binary.Write(&body, binary.LittleEndian, uint32(testSampleRate))
	binary.Write(&body, binary.LittleEndian, uint32(testByteRate))
	binary.Write(&body, binary.LittleEndian, uint16(testChannels*testBits/8))
	binary.Write(&body, binary.LittleEndian, uint16(testBits))
	body.WriteString("data")
	binary.Write(&body, binary.LittleEndian, uint32(len(data)))
	body.Write(data)

	var out bytes.Buffer
	out.WriteString("RIFF")
	binary.Write(&out, binary.LittleEndian, uint32(body.Len()+4)) // +4 for "WAVE"
	out.WriteString("WAVE")
	out.Write(body.Bytes())

	path := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatalf("write wav: %v", err)
	}
	return path
}

// fakeFFmpegEmittingWAV stands in for the decode: it writes a prebuilt WAV
// to ffmpeg's output path and appends its own argv to a log so the test can
// assert on the decode bound.
func fakeFFmpegEmittingWAV(wavPath, argvLog string) string {
	return `for last do :; done
printf '%s\n' "$*" >> '` + argvLog + `'
cp '` + wavPath + `' "$last"
`
}

// seedModel pre-creates the ggml model file so the pipeline never reaches
// the network download path (Go tests here are hermetic by contract).
func seedModel(t *testing.T, modelsDir, model string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(modelsDir, "ggml-"+model+".bin"), []byte("ggml-fake"), 0o600); err != nil {
		t.Fatalf("seed model: %v", err)
	}
}

func TestWavDurationSeconds(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, tc := range []struct {
		name     string
		seconds  int
		withList bool
	}{
		{"plain", 3, false},
		{"list-chunk", 7, true},
		{"zero", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wavDurationSeconds(buildWAV(t, dir, tc.seconds, tc.withList))
			if err != nil {
				t.Fatalf("wavDurationSeconds: %v", err)
			}
			if got != tc.seconds {
				t.Fatalf("duration = %d, want %d", got, tc.seconds)
			}
		})
	}
}

func TestWavDurationSeconds_RejectsNonWAV(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "not.wav")
	if err := os.WriteFile(path, []byte("fake-bytes"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wavDurationSeconds(path); err == nil {
		t.Fatal("expected an error for a non-RIFF file, got nil")
	}
}

// TestTranscribe_EnforcesMaxSecondsWithUnknownDuration is the regression test
// for the web dictation path: it passes no duration, so the bound used to be
// skipped and whisper ran on whatever was uploaded.
func TestTranscribe_EnforcesMaxSecondsWithUnknownDuration(t *testing.T) {
	skipWindows(t)

	binDir := t.TempDir()
	workDir := t.TempDir()
	modelsDir := t.TempDir()
	seedModel(t, modelsDir, "basemodel")
	argvLog := filepath.Join(workDir, "ffmpeg-argv.txt")
	ffmpeg := writeFakeBin(t, binDir, "ffmpeg",
		fakeFFmpegEmittingWAV(buildWAV(t, workDir, 30, false), argvLog))
	whisper := writeFakeBin(t, binDir, "whisper-cli", fakeWhisperOf())

	w := NewWhisperCLI(STTConfig{
		Model:          "basemodel",
		Language:       "auto",
		BinaryPath:     whisper,
		FFMpegPath:     ffmpeg,
		ModelsDir:      modelsDir,
		MaxSeconds:     5,
		TimeoutSeconds: 30,
	})

	// durationSec 0 == unknown, which is what the web handler passes.
	_, err := w.Transcribe(context.Background(), []byte("fake audio"), "audio/webm", 0)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("expected ErrTooLong for a 30s clip with max_seconds=5, got %v", err)
	}

	// The decode itself must be bounded, so a hostile upload cannot make
	// ffmpeg process the whole file before the check runs.
	argv, readErr := os.ReadFile(argvLog)
	if readErr != nil {
		t.Fatalf("read ffmpeg argv: %v", readErr)
	}
	if !strings.Contains(string(argv), "-t 6") {
		t.Fatalf("ffmpeg argv missing the -t decode bound, got %q", strings.TrimSpace(string(argv)))
	}
}

// A clip inside the bound still transcribes: the new check must not reject
// ordinary dictation.
func TestTranscribe_UnknownDurationWithinBound(t *testing.T) {
	skipWindows(t)

	binDir := t.TempDir()
	workDir := t.TempDir()
	modelsDir := t.TempDir()
	seedModel(t, modelsDir, "basemodel")
	ffmpeg := writeFakeBin(t, binDir, "ffmpeg",
		fakeFFmpegEmittingWAV(buildWAV(t, workDir, 3, true), filepath.Join(workDir, "argv.txt")))
	whisper := writeFakeBin(t, binDir, "whisper-cli", fakeWhisperOf())

	w := NewWhisperCLI(STTConfig{
		Model:          "basemodel",
		Language:       "auto",
		BinaryPath:     whisper,
		FFMpegPath:     ffmpeg,
		ModelsDir:      modelsDir,
		MaxSeconds:     120,
		TimeoutSeconds: 30,
	})

	tr, err := w.Transcribe(context.Background(), []byte("fake audio"), "audio/webm", 0)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if tr.Text != "hello transcript" {
		t.Fatalf("text = %q, want the fake whisper transcript", tr.Text)
	}
}

// A caller-supplied duration must keep its early-refusal path: Telegram
// passes the real duration and never pays for the decode.
func TestTranscribe_CallerSuppliedDurationRefusesEarly(t *testing.T) {
	skipWindows(t)

	binDir := t.TempDir()
	workDir := t.TempDir()
	modelsDir := t.TempDir()
	seedModel(t, modelsDir, "basemodel")
	argvLog := filepath.Join(workDir, "ffmpeg-argv.txt")
	ffmpeg := writeFakeBin(t, binDir, "ffmpeg",
		fakeFFmpegEmittingWAV(buildWAV(t, workDir, 30, false), argvLog))
	whisper := writeFakeBin(t, binDir, "whisper-cli", fakeWhisperOf())

	w := NewWhisperCLI(STTConfig{
		Model:          "basemodel",
		Language:       "auto",
		BinaryPath:     whisper,
		FFMpegPath:     ffmpeg,
		ModelsDir:      modelsDir,
		MaxSeconds:     5,
		TimeoutSeconds: 30,
	})

	_, err := w.Transcribe(context.Background(), []byte("fake audio"), "audio/webm", 900)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("expected ErrTooLong, got %v", err)
	}
	if _, statErr := os.Stat(argvLog); statErr == nil {
		t.Fatal("ffmpeg ran despite a caller-supplied duration already exceeding the bound")
	}
}

// An unset max_seconds stays unset: 0 means "no limit configured" on the
// library side, and the callers that must fail closed set the default
// themselves.
func TestTranscribe_NoMaxSecondsSkipsProbe(t *testing.T) {
	skipWindows(t)

	binDir := t.TempDir()
	workDir := t.TempDir()
	modelsDir := t.TempDir()
	seedModel(t, modelsDir, "basemodel")
	ffmpeg := writeFakeBin(t, binDir, "ffmpeg",
		fakeFFmpegEmittingWAV(buildWAV(t, workDir, 3, false), filepath.Join(workDir, "argv.txt")))
	whisper := writeFakeBin(t, binDir, "whisper-cli", fakeWhisperOf())

	w := NewWhisperCLI(STTConfig{
		Model:          "basemodel",
		Language:       "auto",
		BinaryPath:     whisper,
		FFMpegPath:     ffmpeg,
		ModelsDir:      modelsDir,
		TimeoutSeconds: 30,
	})

	// The fake ffmpeg emits a real WAV, but with no bound configured the
	// probe must not run at all - asserted here by the -t flag being absent.
	if _, err := w.Transcribe(context.Background(), []byte("fake audio"), "audio/webm", 0); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	argv, err := os.ReadFile(filepath.Join(workDir, "argv.txt"))
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	if strings.Contains(string(argv), "-t ") {
		t.Fatalf("unexpected decode bound with no max_seconds: %q", strings.TrimSpace(string(argv)))
	}
}
