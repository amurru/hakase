package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/sandbox"
)

func TestFilesHandlerSandbox(t *testing.T) {
	t.Run("nil_sandbox_rejects_read", func(t *testing.T) {
		prev := sandbox.CurrentSandbox
		defer func() { sandbox.CurrentSandbox = prev }()
		sandbox.CurrentSandbox = nil

		api := &FileAPI{}
		req := httptest.NewRequest("GET", "/api/files?path=README.md", nil)
		w := httptest.NewRecorder()
		api.ReadFile(w, req)

		if w.Code < 400 {
			t.Fatalf("expected 4xx when sandbox is nil, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("paths_mode_allows_workspace_read", func(t *testing.T) {
		dir := t.TempDir()
		testFile := filepath.Join(dir, "hello.txt")
		if err := os.WriteFile(testFile, []byte("hello world"), 0o644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		prev := sandbox.CurrentSandbox
		defer func() { sandbox.CurrentSandbox = prev }()
		sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{
			WorkspaceRoots: []string{dir},
		})

		api := &FileAPI{}
		req := httptest.NewRequest("GET", "/api/files?path="+testFile, nil)
		w := httptest.NewRecorder()
		api.ReadFile(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for workspace file, got %d: %s", w.Code, w.Body.String())
		}

		var resp fileContentResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp.Content != hctx.WrapUntrustedData("hello world") {
			t.Fatalf("expected wrapped content, got %q", resp.Content)
		}
	})

	t.Run("paths_mode_rejects_outside_workspace", func(t *testing.T) {
		dir := t.TempDir()

		prev := sandbox.CurrentSandbox
		defer func() { sandbox.CurrentSandbox = prev }()
		sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{
			WorkspaceRoots: []string{dir},
		})

		api := &FileAPI{}
		req := httptest.NewRequest("GET", "/api/files?path=/etc/passwd", nil)
		w := httptest.NewRecorder()
		api.ReadFile(w, req)

		if w.Code < 400 {
			t.Fatalf("expected 4xx for /etc/passwd outside workspace, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]string
		json.NewDecoder(w.Body).Decode(&resp)
		if resp["error"] == "" {
			t.Fatal("expected error message in response")
		}
	})

	t.Run("hidden_files_still_readable_within_workspace", func(t *testing.T) {
		dir := t.TempDir()
		hiddenFile := filepath.Join(dir, ".hidden-config")
		if err := os.WriteFile(hiddenFile, []byte("secret=value"), 0o644); err != nil {
			t.Fatalf("failed to create hidden test file: %v", err)
		}

		prev := sandbox.CurrentSandbox
		defer func() { sandbox.CurrentSandbox = prev }()
		sandbox.CurrentSandbox = sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{
			WorkspaceRoots: []string{dir},
		})

		api := &FileAPI{}
		req := httptest.NewRequest("GET", "/api/files?path="+hiddenFile, nil)
		w := httptest.NewRecorder()
		api.ReadFile(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for hidden workspace file, got %d: %s", w.Code, w.Body.String())
		}

		var resp fileContentResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp.Content != hctx.WrapUntrustedData("secret=value") {
			t.Fatalf("expected wrapped content, got %q", resp.Content)
		}
	})
}
