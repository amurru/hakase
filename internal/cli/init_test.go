package cli

import (
	"os"
	"path/filepath"
	"testing"

	"amurru/hakase/internal/config"
)

// isolateInitEnv points HAKASE_HOME at a temp dir and clears the provider env
// vars so each test starts from a blank slate.
func isolateInitEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	t.Setenv("HAKASE_API_KEY", "")
	t.Setenv("HAKASE_PROVIDER", "")
	t.Setenv("HAKASE_MODEL", "")
	t.Setenv("HAKASE_BASE_URL", "")
	return home
}

func TestInitWritesMinimalConfigToHakaseHome(t *testing.T) {
	home := isolateInitEnv(t)

	code := RunInitCLI([]string{
		"--provider", "openai",
		"--api-key", "sk-test",
		"--model", "gpt-test",
		"--no-password",
	})
	if code != 0 {
		t.Fatalf("RunInitCLI: expected exit 0, got %d", code)
	}

	path := filepath.Join(home, "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode: expected 0600, got %o", perm)
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Provider != "openai" {
		t.Errorf("provider: expected openai, got %q", cfg.Provider)
	}
	if cfg.APIKey != "sk-test" {
		t.Errorf("api_key: expected sk-test, got %q", cfg.APIKey)
	}
	if cfg.ModelName != "gpt-test" {
		t.Errorf("model_name: expected gpt-test, got %q", cfg.ModelName)
	}
}

func TestInitDefaultsModelPerProvider(t *testing.T) {
	home := isolateInitEnv(t)

	code := RunInitCLI([]string{"--provider", "gemini", "--api-key", "k", "--no-password"})
	if code != 0 {
		t.Fatalf("RunInitCLI: expected exit 0, got %d", code)
	}

	cfg, err := config.LoadConfig(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if want := config.DefaultModelForProvider("gemini"); cfg.ModelName != want {
		t.Errorf("model_name: expected provider default %q, got %q", want, cfg.ModelName)
	}
}

func TestInitUsesEnvAPIKey(t *testing.T) {
	home := isolateInitEnv(t)
	t.Setenv("HAKASE_API_KEY", "env-key")

	code := RunInitCLI([]string{"--provider", "gemini", "--no-password"})
	if code != 0 {
		t.Fatalf("RunInitCLI: expected exit 0, got %d", code)
	}

	cfg, err := config.LoadConfig(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.APIKey != "env-key" {
		t.Errorf("api_key: expected env-key, got %q", cfg.APIKey)
	}
}

func TestInitRefusesOverwriteWithoutForce(t *testing.T) {
	home := isolateInitEnv(t)
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	code := RunInitCLI([]string{"--provider", "gemini", "--api-key", "k", "--no-password"})
	if code != 1 {
		t.Fatalf("RunInitCLI without --force: expected exit 1, got %d", code)
	}
	// The existing file must be untouched (still the empty seed object).
	if data, _ := os.ReadFile(path); string(data) != "{}" {
		t.Errorf("existing config was modified without --force: %q", data)
	}

	code = RunInitCLI([]string{"--provider", "gemini", "--api-key", "k", "--force", "--no-password"})
	if code != 0 {
		t.Fatalf("RunInitCLI with --force: expected exit 0, got %d", code)
	}
	if cfg, err := config.LoadConfig(path); err != nil || cfg.Provider != "gemini" {
		t.Errorf("config not overwritten with --force (err=%v)", err)
	}
}

func TestInitRejectsUnknownProvider(t *testing.T) {
	home := isolateInitEnv(t)

	code := RunInitCLI([]string{"--provider", "bogus", "--api-key", "k", "--no-password"})
	if code != 2 {
		t.Fatalf("RunInitCLI with bad provider: expected exit 2, got %d", code)
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); !os.IsNotExist(err) {
		t.Errorf("config should not be written for an invalid provider (stat err=%v)", err)
	}
}

func TestInitRequiresModelForOpenAICompatible(t *testing.T) {
	isolateInitEnv(t)

	code := RunInitCLI([]string{
		"--provider", "openai-compatible",
		"--base-url", "http://localhost:11434/v1",
		"--no-password",
	})
	if code != 2 {
		t.Fatalf("openai-compatible without --model: expected exit 2, got %d", code)
	}
}

func TestInitRequiresAPIKeyNonInteractive(t *testing.T) {
	isolateInitEnv(t)

	// No --api-key and no HAKASE_API_KEY; gemini requires one.
	code := RunInitCLI([]string{"--provider", "gemini", "--no-password"})
	if code != 2 {
		t.Fatalf("gemini without api key: expected exit 2, got %d", code)
	}
}

func TestInitLocalWritesWorkingDirectory(t *testing.T) {
	home := isolateInitEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)

	code := RunInitCLI([]string{"--local", "--provider", "gemini", "--api-key", "k", "--no-password"})
	if code != 0 {
		t.Fatalf("RunInitCLI --local: expected exit 0, got %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Errorf("--local should write ./config.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); !os.IsNotExist(err) {
		t.Errorf("--local should not write the home config (stat err=%v)", err)
	}
}

func TestInitOpenAICompatibleWritesBaseURL(t *testing.T) {
	home := isolateInitEnv(t)

	code := RunInitCLI([]string{
		"--provider", "openai-compatible",
		"--model", "llama-3.3-70b",
		"--base-url", "http://localhost:11434/v1",
		"--no-password",
	})
	if code != 0 {
		t.Fatalf("RunInitCLI: expected exit 0, got %d", code)
	}

	cfg, err := config.LoadConfig(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Provider != "openai-compatible" || cfg.ModelName != "llama-3.3-70b" {
		t.Errorf("unexpected config: provider=%q model=%q", cfg.Provider, cfg.ModelName)
	}
	if cfg.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("base_url: got %q", cfg.BaseURL)
	}
}
