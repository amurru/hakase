package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMemoryConfig(t *testing.T, body string) *Config {
	t.Helper()
	cfg, err := LoadConfig(writeMemoryConfigFile(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

func TestMemoryDefaultsOn(t *testing.T) {
	cfg := writeMemoryConfig(t, `{"provider":"openai","model_name":"m","api_key":"k"}`)
	if !MemoryEnabled(cfg) {
		t.Fatalf("memory should default to enabled")
	}
	if got := MemoryMaxPromptChars(cfg); got != DefaultMemoryMaxPromptChars {
		t.Fatalf("max_prompt_chars = %d, want %d", got, DefaultMemoryMaxPromptChars)
	}
	if got := MemoryMaxNotes(cfg); got != DefaultMemoryMaxNotes {
		t.Fatalf("max_notes = %d, want %d", got, DefaultMemoryMaxNotes)
	}
}

func TestMemoryExplicitlyDisabled(t *testing.T) {
	cfg := writeMemoryConfig(t, `{"memory":{"enabled":false}}`)
	if MemoryEnabled(cfg) {
		t.Fatalf("explicit enabled:false must disable memory")
	}
	// Nil-config accessors stay safe (enabled semantics).
	if MemoryEnabled(nil) != true {
		t.Fatalf("nil config should read as enabled")
	}
}

func TestMemoryEnabledFieldAbsentVsFalse(t *testing.T) {
	cfg := writeMemoryConfig(t, `{"memory":{}}`)
	if !MemoryEnabled(cfg) {
		t.Fatalf("absent enabled must stay on (pointer tri-state)")
	}
}

func TestMemoryEnvOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":"openai","model_name":"m","api_key":"k","memory":{"max_prompt_chars":1234}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAKASE_MEMORY_ENABLED", "false")
	t.Setenv("HAKASE_MEMORY_MAX_PROMPT_CHARS", "5000")
	t.Setenv("HAKASE_MEMORY_MAX_NOTES", "7")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if MemoryEnabled(cfg) {
		t.Fatalf("env enabled=false must win over file absence")
	}
	if got := MemoryMaxPromptChars(cfg); got != 5000 {
		t.Fatalf("env max_prompt_chars = %d, want 5000 (env wins over file)", got)
	}
	if got := MemoryMaxNotes(cfg); got != 7 {
		t.Fatalf("env max_notes = %d, want 7", got)
	}
}

func TestMemoryEnvOnlyConfig(t *testing.T) {
	t.Setenv("HAKASE_API_KEY", "k")
	t.Setenv("HAKASE_MEMORY_ENABLED", "true")
	t.Setenv("HAKASE_MEMORY_MAX_NOTES", "9")

	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("LoadConfig from env only: %v", err)
	}
	if !MemoryEnabled(cfg) || MemoryMaxNotes(cfg) != 9 {
		t.Fatalf("env-only memory config not applied: enabled=%v notes=%d", MemoryEnabled(cfg), MemoryMaxNotes(cfg))
	}
}

func TestMemoryValidationRejectsNegatives(t *testing.T) {
	if _, err := LoadConfig(writeMemoryConfigFile(t, `{"memory":{"max_prompt_chars":-5}}`)); err == nil || !strings.Contains(err.Error(), "max_prompt_chars") {
		t.Fatalf("want max_prompt_chars validation error, got %v", err)
	}
	if _, err := LoadConfig(writeMemoryConfigFile(t, `{"memory":{"max_notes":-1}}`)); err == nil || !strings.Contains(err.Error(), "max_notes") {
		t.Fatalf("want max_notes validation error, got %v", err)
	}
}

func writeMemoryConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
