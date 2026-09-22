// config_tracing_test.go - tracing config section: defaults, validation
// bounds, strict env overrides (errors name the variable), and the
// envConfigSet convention (config can come entirely from HAKASE_TRACING_*).
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTracingDefaults pins the applied defaults: enabled stays off (the
// feature default), endpoint gets the OTLP convention, ratio 1.
func TestTracingDefaults(t *testing.T) {
	cfg, err := loadWithEnv(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Tracing.Enabled {
		t.Error("tracing.enabled must default to false")
	}
	if cfg.Tracing.Endpoint != DefaultTracingEndpoint {
		t.Errorf("endpoint default = %q, want %q", cfg.Tracing.Endpoint, DefaultTracingEndpoint)
	}
	if cfg.Tracing.SampleRatio != DefaultTracingSampleRatio {
		t.Errorf("sample_ratio default = %v, want %v", cfg.Tracing.SampleRatio, DefaultTracingSampleRatio)
	}
}

// TestTracingFileConfigAndBounds covers a well-formed file section and the
// loud rejections.
func TestTracingFileConfigAndBounds(t *testing.T) {
	t.Run("valid section", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		body := `{"provider":"openai","model_name":"m","api_key":"k","tracing":{"enabled":true,"endpoint":"https://otel.example.com/v1/traces","sample_ratio":0.25,"headers":{"authorization":"Basic x"}}}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !cfg.Tracing.Enabled || cfg.Tracing.Endpoint != "https://otel.example.com/v1/traces" || cfg.Tracing.SampleRatio != 0.25 {
			t.Fatalf("tracing section not applied: %+v", cfg.Tracing)
		}
		if cfg.Tracing.Headers["authorization"] != "Basic x" {
			t.Fatalf("headers not applied: %+v", cfg.Tracing.Headers)
		}
	})

	t.Run("ratio above 1 rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		body := `{"provider":"openai","model_name":"m","api_key":"k","tracing":{"enabled":true,"sample_ratio":1.5}}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "tracing.sample_ratio") {
			t.Fatalf("expected tracing.sample_ratio error, got: %v", err)
		}
	})

	t.Run("empty header key rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		body := `{"provider":"openai","model_name":"m","api_key":"k","tracing":{"enabled":true,"headers":{" ":"v"}}}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "tracing.headers") {
			t.Fatalf("expected tracing.headers error, got: %v", err)
		}
	})
}

// TestTracingEnvOverrides covers the strict parse policy and precedence.
func TestTracingEnvOverrides(t *testing.T) {
	cfg, err := loadWithEnv(t, map[string]string{
		"HAKASE_TRACING_ENABLED":      "1",
		"HAKASE_TRACING_ENDPOINT":     "https://collector:4318",
		"HAKASE_TRACING_SAMPLE_RATIO": "0.5",
		"HAKASE_TRACING_HEADERS":      "A=B, C=D",
	})
	if err != nil {
		t.Fatalf("load with env: %v", err)
	}
	if !cfg.Tracing.Enabled || cfg.Tracing.Endpoint != "https://collector:4318" || cfg.Tracing.SampleRatio != 0.5 {
		t.Fatalf("env overrides not applied: %+v", cfg.Tracing)
	}
	if cfg.Tracing.Headers["A"] != "B" || cfg.Tracing.Headers["C"] != "D" {
		t.Fatalf("headers parse wrong: %+v", cfg.Tracing.Headers)
	}
}

func TestTracingEnvStrictParsing(t *testing.T) {
	cases := []struct {
		name, env, wantErr string
	}{
		{"bad bool", "HAKASE_TRACING_ENABLED=ture", "HAKASE_TRACING_ENABLED"},
		{"bad ratio", "HAKASE_TRACING_SAMPLE_RATIO=2", "HAKASE_TRACING_SAMPLE_RATIO"},
		{"nan ratio", "HAKASE_TRACING_SAMPLE_RATIO=abc", "HAKASE_TRACING_SAMPLE_RATIO"},
		{"bad headers", "HAKASE_TRACING_HEADERS=NOEQUALS", "HAKASE_TRACING_HEADERS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, v, _ := strings.Cut(tc.env, "=")
			_, err := loadWithEnv(t, map[string]string{k: v})
			if err == nil {
				t.Fatalf("%s=%q: expected a load error, got none", k, v)
			}
			if !strings.Contains(err.Error(), k) {
				t.Fatalf("error must name the variable %s, got: %v", k, err)
			}
		})
	}
}

// TestTracingEnvOnlyConfig pins the envConfigSet convention: with no config
// file at all, HAKASE_TRACING_* alone is enough to build a config.
func TestTracingEnvOnlyConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	t.Setenv("HAKASE_TRACING_ENABLED", "yes")
	t.Setenv("HAKASE_TRACING_ENDPOINT", "http://localhost:4318")
	cfg, err := LoadConfig(missing)
	if err != nil {
		t.Fatalf("env-only load: %v", err)
	}
	if !cfg.Tracing.Enabled {
		t.Fatal("HAKASE_TRACING_ENABLED=yes did not enable tracing")
	}
}
