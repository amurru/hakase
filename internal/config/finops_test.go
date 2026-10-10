package config

import (
	"testing"

	"amurru/hakase/internal/finops"
)

func TestFinOpsDefaultsOff(t *testing.T) {
	path := writeTempConfig(t, `{"provider":"gemini"}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.FinOps.Enabled {
		t.Error("finops must default to disabled")
	}
	if cfg.FinOps.Currency != "USD" || cfg.FinOps.Budgets.Enforce != "warn" {
		t.Errorf("defaults not applied: %+v", cfg.FinOps)
	}
	if finops.Active() != nil {
		t.Error("default config must not enable live recording")
	}
}

func TestFinOpsValidation(t *testing.T) {
	path := writeTempConfig(t, `{"provider":"gemini","finops":{"enabled":true,"budgets":{"enforce":"nuke"}}}`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("bad enforce value must fail")
	}
	path = writeTempConfig(t, `{"provider":"gemini","finops":{"enabled":true,"budgets":{"daily_usd":-1}}}`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("negative budget must fail")
	}
	path = writeTempConfig(t, `{"provider":"gemini","finops":{"enabled":true,"budgets":{"cache_warn_ratio":2}}}`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("cache_warn_ratio > 1 must fail")
	}
	path = writeTempConfig(t, `{"provider":"gemini","finops":{"enabled":true,"prices":{"overrides":{"m":{"input_per_1m":-1}}}}}}`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("negative price must fail")
	}
}

func TestFinOpsLoadAndRecording(t *testing.T) {
	t.Setenv("HAKASE_HOME", t.TempDir())
	path := writeTempConfig(t, `{"provider":"gemini","finops":{"enabled":true,"budgets":{"daily_usd":5,"enforce":"warn"},"ledger":{"per_tool":true}}}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.FinOps.Enabled || cfg.FinOps.Budgets.DailyUSD != 5 {
		t.Errorf("finops block not loaded: %+v", cfg.FinOps)
	}
	s := finops.Active()
	if s == nil {
		t.Fatal("enabled config must activate live recording")
	}
	if len(s.Overrides) != 0 || s.Table.Version == "" {
		t.Errorf("bad live settings: %+v", s)
	}
	finops.Configure(finops.Settings{})
}

func TestFinOpsEnvOverrides(t *testing.T) {
	t.Setenv("HAKASE_HOME", t.TempDir())
	t.Setenv("HAKASE_FINOPS_ENABLED", "1")
	t.Setenv("HAKASE_FINOPS_ENFORCE", "block")
	t.Setenv("HAKASE_BUDGET_DAILY_USD", "7.5")
	path := writeTempConfig(t, `{"provider":"gemini"}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.FinOps.Enabled || cfg.FinOps.Budgets.Enforce != "block" || cfg.FinOps.Budgets.DailyUSD != 7.5 {
		t.Errorf("env overrides not applied: %+v", cfg.FinOps)
	}
	finops.Configure(finops.Settings{})

	t.Setenv("HAKASE_FINOPS_ENFORCE", "nuke")
	if _, err := LoadConfig(path); err == nil {
		t.Error("bad HAKASE_FINOPS_ENFORCE must fail")
	}
}
