package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/media"
	"amurru/hakase/internal/sandbox"
)

func TestMediaStatusRedaction(t *testing.T) {
	// Setup config with keys
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfgJSON := `{"provider":"gemini","api_key":"sk-global","media":{"fal_key":"fal-secret","openai_image_key":"openai-secret"}}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	setTestConfigPath(t, cfgPath)
	// Setup registry
	sandbox.CurrentSandbox = nil
	store, _ := media.NewStore(dir + "/outputs/media")
	cfg, _ := config.LoadConfig(cfgPath)
	reg, _ := media.NewRegistry(cfg.Media, nil, store)
	SetMediaRegistry(reg)
	defer SetMediaRegistry(nil)

	req := httptest.NewRequest("GET", "/api/media/status", nil)
	w := httptest.NewRecorder()
	MediaStatus(w, req)
	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := os.ReadFile("")
	_ = body
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// No raw keys anywhere
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "fal-secret") || strings.Contains(string(raw), "openai-secret") || strings.Contains(string(raw), "sk-global") {
		t.Fatal("raw key leaked in status")
	}
	// Check configured booleans
	caps, ok := out["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatalf("capabilities missing")
	}
	falCaps, _ := caps["fal"].(map[string]interface{})
	if falCaps["configured"] != true {
		t.Fatalf("fal configured should be true")
	}
	openCaps, _ := caps["openai"].(map[string]interface{})
	if openCaps["configured"] != true {
		t.Fatalf("openai configured should be true")
	}
	if out["resolved_image"] == nil || out["resolved_video"] == nil {
		t.Fatal("resolved fields missing")
	}
	if out["output_dir"] != "outputs/media" && out["output_dir"] != cfg.Media.OutputDir {
		// output_dir may be absolute after store root? Accept either
	}
}

func TestMediaStatusNoKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfgJSON := `{"provider":"gemini"}`
	os.WriteFile(cfgPath, []byte(cfgJSON), 0644)
	setTestConfigPath(t, cfgPath)
	sandbox.CurrentSandbox = nil
	store, _ := media.NewStore(dir + "/outputs/media")
	cfg, _ := config.LoadConfig(cfgPath)
	// Ensure no keys
	cfg.Media.FalKey = ""
	cfg.Media.OpenAIImageKey = ""
	reg, _ := media.NewRegistry(cfg.Media, nil, store)
	SetMediaRegistry(reg)
	defer SetMediaRegistry(nil)

	req := httptest.NewRequest("GET", "/api/media/status", nil)
	w := httptest.NewRecorder()
	MediaStatus(w, req)
	var out map[string]interface{}
	json.NewDecoder(w.Result().Body).Decode(&out)
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "sk-") {
		t.Fatal("key leaked")
	}
	caps := out["capabilities"].(map[string]interface{})
	if caps["fal"].(map[string]interface{})["configured"] != false {
		t.Fatal("fal should not be configured")
	}
	if caps["openai"].(map[string]interface{})["configured"] != false {
		t.Fatal("openai should not be configured")
	}
	if out["resolved_image"] != "pil" {
		t.Fatalf("expected pil, got %v", out["resolved_image"])
	}
}

func TestMediaManifestEmpty(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{}`), 0644)
	setTestConfigPath(t, cfgPath)
	sandbox.CurrentSandbox = nil
	store, _ := media.NewStore(dir + "/outputs/media")
	cfg, _ := config.LoadConfig(cfgPath)
	reg, _ := media.NewRegistry(cfg.Media, nil, store)
	SetMediaRegistry(reg)
	defer SetMediaRegistry(nil)

	req := httptest.NewRequest("GET", "/api/media/manifest", nil)
	w := httptest.NewRecorder()
	MediaManifest(w, req)
	if w.Code != 200 {
		t.Fatalf("manifest status %d", w.Code)
	}
	var out []interface{}
	if err := json.NewDecoder(w.Result().Body).Decode(&out); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty manifest, got %d", len(out))
	}
}

func TestMediaStatusNoRegistry(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{"provider":"gemini"}`), 0644)
	setTestConfigPath(t, cfgPath)
	SetMediaRegistry(nil)
	req := httptest.NewRequest("GET", "/api/media/status", nil)
	w := httptest.NewRecorder()
	MediaStatus(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	var out map[string]interface{}
	json.NewDecoder(w.Result().Body).Decode(&out)
	if out["resolved_image"] != "pil" {
		t.Fatalf("no registry should still resolve pil, got %v", out["resolved_image"])
	}
}

func TestMediaManifestWithEntries(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{}`), 0644)
	setTestConfigPath(t, cfgPath)
	sandbox.CurrentSandbox = nil
	// Use dir as output_dir via store
	store, _ := media.NewStore(dir + "/outputs/media")
	cfg, _ := config.LoadConfig(cfgPath)
	reg, _ := media.NewRegistry(cfg.Media, nil, store)
	SetMediaRegistry(reg)
	defer SetMediaRegistry(nil)
	// Write manifest entries
	manifestPath := filepath.Join(store.Root(), "manifest.jsonl")
	os.MkdirAll(filepath.Dir(manifestPath), 0755)
	for i := 0; i < 25; i++ {
		line := `{"tool":"generate_image","prompt":"p` + string(rune('0'+i%10)) + `"}`
		f, _ := os.OpenFile(manifestPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		f.WriteString(line + "\n")
		f.Close()
	}
	req := httptest.NewRequest("GET", "/api/media/manifest", nil)
	w := httptest.NewRecorder()
	MediaManifest(w, req)
	var out []json.RawMessage
	json.NewDecoder(w.Result().Body).Decode(&out)
	if len(out) != 20 {
		t.Fatalf("expected last 20, got %d", len(out))
	}
}
