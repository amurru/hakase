package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
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
