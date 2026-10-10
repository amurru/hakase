package config

import (
	"testing"
)

// TestAuditValidate pins forward config validation.
func TestAuditValidate(t *testing.T) {
	if err := (AuditConfig{}.Validate()); err != nil {
		t.Errorf("zero AuditConfig: %v, want nil", err)
	}
	if err := (AuditConfig{ForwardURL: "https://siem.example.com/hook", ForwardFormat: "json"}.Validate()); err != nil {
		t.Errorf("valid forward: %v, want nil", err)
	}
	if err := (AuditConfig{ForwardFormat: "xml"}.Validate()); err == nil {
		t.Error("bad format accepted, want error")
	}
	if err := (AuditConfig{ForwardURL: "file:///tmp/x"}.Validate()); err == nil {
		t.Error("non-http URL accepted, want error")
	}
}

// TestAuthWebRolesValidate pins role tier validation at load.
func TestAuthWebRolesValidate(t *testing.T) {
	path := writeTempConfig(t, `{"auth": {"web_roles": {"amy": "admin", "bob": "bogus"}}}`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("LoadConfig accepted bogus web role, want error")
	}
	path = writeTempConfig(t, `{"auth": {"web_roles": {"amy": "approver"}}}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Auth.WebRoles["amy"] != "approver" {
		t.Errorf("web_roles = %v, want amy=approver", cfg.Auth.WebRoles)
	}
}
