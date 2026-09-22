package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadWithEnv writes a minimal valid config file and applies the given
// environment before loading. It returns the load result as-is so tests can
// assert both success and strict-parse failures.
func loadWithEnv(t *testing.T, env map[string]string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"provider":"openai","model_name":"m","api_key":"k"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	return LoadConfig(path)
}

// TestEnvBoolPolicy pins the repo-wide boolean override policy on
// HAKASE_DEBUG: 1/0, true/false, yes/no in any casing are valid; anything
// else is a load error naming the variable, never a silent false.
func TestEnvBoolPolicy(t *testing.T) {
	cases := []struct {
		value   string
		want    bool
		wantErr bool
	}{
		{"1", true, false},
		{"0", false, false},
		{"true", true, false},
		{"TRUE", true, false},
		{"True", true, false},
		{"false", false, false},
		{"FALSE", false, false},
		{"yes", true, false},
		{"Yes", true, false},
		{"no", false, false},
		{"NO", false, false},
		{"ture", false, true},
		{"enabled", false, true},
		{"on", false, true},
		{"off", false, true},
		{"2", false, true},
	}
	for _, tc := range cases {
		cfg, err := loadWithEnv(t, map[string]string{"HAKASE_DEBUG": tc.value})
		if tc.wantErr {
			if err == nil {
				t.Fatalf("HAKASE_DEBUG=%q: expected a load error, got none", tc.value)
			}
			if !strings.Contains(err.Error(), "HAKASE_DEBUG") {
				t.Fatalf("HAKASE_DEBUG=%q: error must name the variable, got: %v", tc.value, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("HAKASE_DEBUG=%q: %v", tc.value, err)
		}
		if cfg.Debug != tc.want {
			t.Fatalf("HAKASE_DEBUG=%q: debug = %v, want %v", tc.value, cfg.Debug, tc.want)
		}
	}
}

// TestEnvBoolOverridesWired checks that every boolean override goes through
// the shared policy and lands in its own config field (one valid true, one
// valid false, one policy violation each).
func TestEnvBoolOverridesWired(t *testing.T) {
	cases := []struct {
		env   string
		extra map[string]string // companions required once the toggle is on
		check func(*Config) bool
	}{
		{"HAKASE_SEARCH_EXPANSION", nil, func(c *Config) bool { return c.SearchExpansion }},
		{"HAKASE_SIDEKICK_ENABLED", map[string]string{"HAKASE_SIDEKICK_MODEL": "m"}, func(c *Config) bool { return c.Sidekick.Enabled != nil && *c.Sidekick.Enabled }},
		{"HAKASE_TELEGRAM_ENABLED", map[string]string{"HAKASE_TELEGRAM_BOT_TOKEN": "tok"}, func(c *Config) bool { return c.Channels.Telegram.Enabled != nil && *c.Channels.Telegram.Enabled }},
		{"HAKASE_MEMORY_ENABLED", nil, func(c *Config) bool { return c.Memory.Enabled != nil && *c.Memory.Enabled }},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			env := map[string]string{tc.env: "yes"}
			for k, v := range tc.extra {
				env[k] = v
			}
			cfg, err := loadWithEnv(t, env)
			if err != nil {
				t.Fatalf("%s=yes: %v", tc.env, err)
			}
			if !tc.check(cfg) {
				t.Fatalf("%s=yes: override did not reach its config field", tc.env)
			}
			env[tc.env] = "NO"
			cfg, err = loadWithEnv(t, env)
			if err != nil {
				t.Fatalf("%s=NO: %v", tc.env, err)
			}
			if tc.check(cfg) {
				t.Fatalf("%s=NO: override did not reach its config field", tc.env)
			}
			if _, err = loadWithEnv(t, map[string]string{tc.env: "yeah"}); err == nil {
				t.Fatalf("%s=yeah: expected a load error", tc.env)
			}
		})
	}
}

// TestEnvPositiveIntPolicy pins the numeric override policy on
// HAKASE_MAX_OUTPUT_TOKENS: positive integers only, bounded by the int32
// config field, and every invalid form is a named load error.
func TestEnvPositiveIntPolicy(t *testing.T) {
	cases := []struct {
		value   string
		want    int32
		wantErr bool
	}{
		{"4096", 4096, false},
		{"1", 1, false},
		{"2147483647", math.MaxInt32, false},
		{"abc", 0, true},
		{"12.5", 0, true},
		{"0", 0, true},
		{"-5", 0, true},
		{" 4096", 0, true},
		{"8589934592", 0, true}, // > math.MaxInt32: field cannot hold it
	}
	for _, tc := range cases {
		cfg, err := loadWithEnv(t, map[string]string{"HAKASE_MAX_OUTPUT_TOKENS": tc.value})
		if tc.wantErr {
			if err == nil {
				t.Fatalf("HAKASE_MAX_OUTPUT_TOKENS=%q: expected a load error, got none", tc.value)
			}
			if !strings.Contains(err.Error(), "HAKASE_MAX_OUTPUT_TOKENS") {
				t.Fatalf("HAKASE_MAX_OUTPUT_TOKENS=%q: error must name the variable, got: %v", tc.value, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("HAKASE_MAX_OUTPUT_TOKENS=%q: %v", tc.value, err)
		}
		if cfg.LoopGuard.MaxOutputTokens != tc.want {
			t.Fatalf("HAKASE_MAX_OUTPUT_TOKENS=%q: max_output_tokens = %d, want %d", tc.value, cfg.LoopGuard.MaxOutputTokens, tc.want)
		}
	}
}

// TestEnvMemoryNumericOverrides pins the memory limits on the same strict
// numeric policy: they are part of the repo-wide change, not exempt from it.
func TestEnvMemoryNumericOverrides(t *testing.T) {
	cfg, err := loadWithEnv(t, map[string]string{
		"HAKASE_MEMORY_MAX_PROMPT_CHARS": "5000",
		"HAKASE_MEMORY_MAX_NOTES":        "7",
	})
	if err != nil {
		t.Fatalf("valid memory overrides: %v", err)
	}
	if cfg.Memory.MaxPromptChars != 5000 || cfg.Memory.MaxNotes != 7 {
		t.Fatalf("memory overrides not applied: chars=%d notes=%d", cfg.Memory.MaxPromptChars, cfg.Memory.MaxNotes)
	}
	for _, env := range []string{"HAKASE_MEMORY_MAX_PROMPT_CHARS", "HAKASE_MEMORY_MAX_NOTES"} {
		t.Run(env, func(t *testing.T) {
			if _, err := loadWithEnv(t, map[string]string{env: "lots"}); err == nil {
				t.Fatalf("%s=lots: expected a load error (strict policy covers memory overrides)", env)
			}
			if _, err := loadWithEnv(t, map[string]string{env: "0"}); err == nil {
				t.Fatalf("%s=0: expected a load error", env)
			}
		})
	}
}
