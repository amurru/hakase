package config

import (
	"path/filepath"
	"testing"
)

// TestApprovalModeValidate pins strict mode validation at load.
func TestApprovalModeValidate(t *testing.T) {
	for _, mode := range []string{"", "interactive", "deny", "allow"} {
		if err := (ApprovalConfig{Mode: mode}.Validate()); err != nil {
			t.Errorf("mode %q: %v, want nil", mode, err)
		}
	}
	if err := (ApprovalConfig{Mode: "sometimes"}.Validate()); err == nil {
		t.Error("mode sometimes: nil error, want error")
	}
}

// TestLoadConfigRejectsBadApprovalMode pins fail-loud startup on a typo.
func TestLoadConfigRejectsBadApprovalMode(t *testing.T) {
	path := writeTempConfig(t, `{"approval": {"mode": "permissive"}}`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("LoadConfig accepted approval.mode=permissive, want error")
	}
}

// TestPermissionsEnabledDefault pins loading enabled by default.
func TestPermissionsEnabledDefault(t *testing.T) {
	var c PermissionsConfig
	if !c.LoadEnabled() {
		t.Error("zero PermissionsConfig disabled, want enabled")
	}
	f := false
	c.Enabled = &f
	if c.LoadEnabled() {
		t.Error("explicit false enabled, want disabled")
	}
}

// TestPermissionsEnvOverrides pins the plumbing env vars.
func TestPermissionsEnvOverrides(t *testing.T) {
	path := writeTempConfig(t, `{}`)
	t.Setenv("HAKASE_PERMISSIONS_ENABLED", "false")
	t.Setenv("HAKASE_PERMISSIONS_ENTERPRISE_PATH", filepath.Join("x", "ent.json"))
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Permissions.LoadEnabled() {
		t.Error("HAKASE_PERMISSIONS_ENABLED=false ignored")
	}
	if cfg.Permissions.EnterprisePath != filepath.Join("x", "ent.json") {
		t.Errorf("enterprise path = %q, want override", cfg.Permissions.EnterprisePath)
	}
}
