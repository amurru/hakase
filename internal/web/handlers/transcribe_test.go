package handlers

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTranscribeHandler_MissingFile(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.Close()

	req := httptest.NewRequest("POST", "/api/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	Transcribe(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestTranscribeHandler_EmptyFile(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "test.webm")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	_ = part // empty content
	writer.Close()

	req := httptest.NewRequest("POST", "/api/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	Transcribe(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestTranscribeHandler_UnavailableWhisper(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "test.webm")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	_, _ = part.Write([]byte("fake audio content"))
	writer.Close()

	req := httptest.NewRequest("POST", "/api/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	Transcribe(rec, req)

	// Since whisper-cli/ffmpeg might not be installed in the test environment,
	// status code should be 503 Service Unavailable (or 500/200 depending on env).
	if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError && rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}
}

// TestTranscribeHandler_RejectsOversizedUpload pins the resource bound. The
// handler's ParseMultipartForm argument is only an in-memory threshold, so
// without a hard body cap an authenticated client could stream an
// arbitrarily large upload and have it read fully into memory.
func TestTranscribeHandler_RejectsOversizedUpload(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "huge.webm")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	// One byte past the 25 MiB cap, written in chunks so the test does not
	// hold a 25 MiB buffer of its own for long.
	chunk := make([]byte, 1<<20)
	for written := 0; written <= maxTranscribeBody; written += len(chunk) {
		if _, err := part.Write(chunk); err != nil {
			t.Fatalf("write oversized part: %v", err)
		}
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	Transcribe(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for an oversized upload, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

// writeFakeBinary writes an executable shell script and returns its path. The
// handler resolves both tools through speech.Availability, so a stand-in
// script drives the pipeline without either real binary installed.
func writeFakeBinary(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}

// skipWindows skips tests that execute shell-script fake binaries: windows CI
// has no /bin/sh, and LookPath there requires .exe/.bat extensions, so the
// fake-binary pipeline mechanics only translate to POSIX. Mirrors the
// equivalent guard in internal/speech and the #22 windows-suite convention.
func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binaries are POSIX-only")
	}
}

// TestTranscribeHandler_HonorsSTTTimeout pins speech_to_text.timeout_seconds
// on the web dictation path. r.Context() cannot end this work - it cancels
// only when the client disconnects - so a decode that hangs would otherwise
// hold the request and the ffmpeg process for as long as the browser waits.
func TestTranscribeHandler_HonorsSTTTimeout(t *testing.T) {
	skipWindows(t)
	home := isolateHome(t)
	// HAKASE_HOME is what config.ResolveConfigPath consults; pinning it keeps
	// the test off any config.json in the package directory.
	t.Setenv("HAKASE_HOME", home)

	// ffmpeg stands in for a decode that never returns. whisper-cli only has
	// to exist and be executable for Availability to pass, and the bound must
	// land on ffmpeg, which Transcribe runs first. The exec builtin replaces
	// the shell rather than forking, so the killed process is the sleeper: a
	// forked grandchild would survive the kill while still holding the output
	// pipes open, and cmd.Wait would block until it finished on its own.
	stall := filepath.Join(home, "ffmpeg-stall")
	writeFakeBinary(t, stall, "#!/bin/sh\nexec sleep 15\n")
	whisperBin := filepath.Join(home, "whisper-cli")
	writeFakeBinary(t, whisperBin, "#!/bin/sh\nexit 0\n")

	// Seed the model so EnsureModel short-circuits on its os.Stat fast path:
	// this test is about the decode bound, and must not reach the network.
	modelsDir := filepath.Join(home, "models")
	if err := os.MkdirAll(modelsDir, 0o700); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "ggml-base-q5_1.bin"), []byte("seeded"), 0o600); err != nil {
		t.Fatalf("seed model: %v", err)
	}

	cfg := fmt.Sprintf(`{
		"speech_to_text": {
			"model": "base-q5_1",
			"language": "auto",
			"binary_path": %q,
			"ffmpeg_path": %q,
			"models_dir": %q,
			"max_seconds": 120,
			"timeout_seconds": 1,
			"model_timeout_seconds": 10
		}
	}`, whisperBin, stall, modelsDir)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "test.webm")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := part.Write([]byte("fake audio content")); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	start := time.Now()
	Transcribe(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for a stalled transcription, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	// The stand-in sleeps 15s while timeout_seconds is 1, so a correct handler
	// returns in about a second. The ceiling only has to separate "timed out"
	// from "waited for the process"; it is not a latency assertion.
	if elapsed > 6*time.Second {
		t.Fatalf("transcription was not bounded by timeout_seconds: took %s (stall is 15s)", elapsed)
	}
}

// TestTranscribeHandler_ModelErrorDoesNotLeakConfig pins CWE-209 on the model
// download path. ensureModel's errors embed the configured model_url_base and
// on-disk model paths, and any authenticated client can hit this endpoint, so
// the response must not carry them. The failure is forced with a server that
// returns 500, which makes ensureModel build exactly such an error.
func TestTranscribeHandler_ModelErrorDoesNotLeakConfig(t *testing.T) {
	skipWindows(t)
	home := isolateHome(t)
	t.Setenv("HAKASE_HOME", home)

	// Refuse the download, and make the host part of the URL distinctive so
	// the assertion below cannot pass by accident.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	ffmpeg := filepath.Join(home, "ffmpeg-noop")
	writeFakeBinary(t, ffmpeg, "#!/bin/sh\nexec true\n")
	whisperBin := filepath.Join(home, "whisper-cli")
	writeFakeBinary(t, whisperBin, "#!/bin/sh\nexit 0\n")

	modelsDir := filepath.Join(home, "models")
	cfg := fmt.Sprintf(`{
		"speech_to_text": {
			"model": "base-q5_1",
			"language": "auto",
			"binary_path": %q,
			"ffmpeg_path": %q,
			"models_dir": %q,
			"model_url_base": %q,
			"max_seconds": 120,
			"timeout_seconds": 5,
			"model_timeout_seconds": 30
		}
	}`, whisperBin, ffmpeg, modelsDir, srv.URL)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "test.webm")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := part.Write([]byte("fake audio content")); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	Transcribe(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when the model download fails, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	for _, secret := range []string{srv.URL, "ggml-base-q5_1.bin", modelsDir, "base-q5_1"} {
		if strings.Contains(got, secret) {
			t.Fatalf("503 body leaked %q: %s", secret, got)
		}
	}
	// The message must still say something actionable about the failure.
	if !strings.Contains(got, "speech model unavailable") {
		t.Fatalf("expected a fixed client-safe message, got: %s", got)
	}
}

// TestTranscribeHandler_ModelDownloadHasItsOwnBudget pins the separation: the
// first-use model pull is a network transfer of tens or hundreds of MiB, so
// bounding it by timeout_seconds (here 1s) would make a slow connection
// permanently unable to complete a first run. It must run under
// model_timeout_seconds instead.
func TestTranscribeHandler_ModelDownloadHasItsOwnBudget(t *testing.T) {
	skipWindows(t)
	home := isolateHome(t)
	t.Setenv("HAKASE_HOME", home)

	// Stalls for longer than timeout_seconds, then serves a body above the
	// speech package's modelMinBytes floor so the download is accepted.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2500 * time.Millisecond)
		_, _ = w.Write(make([]byte, 6<<20))
	}))
	defer srv.Close()

	// ffmpeg exits immediately and writes no WAV, so the transcription step
	// fails fast - this test is only about how long the download was allowed
	// to take, not about the transcript.
	ffmpeg := filepath.Join(home, "ffmpeg-noop")
	writeFakeBinary(t, ffmpeg, "#!/bin/sh\nexec true\n")
	whisperBin := filepath.Join(home, "whisper-cli")
	writeFakeBinary(t, whisperBin, "#!/bin/sh\nexit 0\n")

	modelsDir := filepath.Join(home, "models")
	cfg := fmt.Sprintf(`{
		"speech_to_text": {
			"model": "base-q5_1",
			"language": "auto",
			"binary_path": %q,
			"ffmpeg_path": %q,
			"models_dir": %q,
			"model_url_base": %q,
			"max_seconds": 120,
			"timeout_seconds": 1,
			"model_timeout_seconds": 30
		}
	}`, whisperBin, ffmpeg, modelsDir, srv.URL)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "test.webm")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := part.Write([]byte("fake audio content")); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	start := time.Now()
	Transcribe(rec, req)
	elapsed := time.Since(start)

	// The download outliving timeout_seconds is the whole point: had it run
	// under the transcription bound, the handler would have given up at 1s.
	if elapsed < 2*time.Second {
		t.Fatalf("model download was cut off by timeout_seconds: returned in %s (server stalls 2.5s)", elapsed)
	}
	modelFile := filepath.Join(modelsDir, "ggml-base-q5_1.bin")
	info, err := os.Stat(modelFile)
	if err != nil {
		t.Fatalf("model was not downloaded: %v (body: %s)", err, rec.Body.String())
	}
	if info.Size() == 0 {
		t.Fatal("downloaded model is empty")
	}
}
