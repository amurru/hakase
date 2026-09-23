// config_session_test.go - session.snapshots config: defaults, validation,
// strict env overrides, envConfigSet participation.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionSnapshotsDefaults(t *testing.T) {
	cfg, err := loadWithEnv(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !SessionSnapshotsEnabled(cfg) {
		t.Error("session.snapshots must default to enabled")
	}
	if SessionSnapshotsMax(cfg) != DefaultSessionSnapshotsMax {
		t.Errorf("max default = %d, want %d", SessionSnapshotsMax(cfg), DefaultSessionSnapshotsMax)
	}
}

func TestSessionSnapshotsExplicitFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"provider":"openai","model_name":"m","api_key":"k","session":{"snapshots":{"enabled":false,"max":7}}}`
	if err := writeFileForTest(t, path, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if SessionSnapshotsEnabled(cfg) {
		t.Error("explicit enabled:false must disable snapshots")
	}
	if SessionSnapshotsMax(cfg) != 7 {
		t.Errorf("max = %d, want 7", SessionSnapshotsMax(cfg))
	}
}

func TestSessionSnapshotsNegativeMaxRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"provider":"openai","model_name":"m","api_key":"k","session":{"snapshots":{"max":-1}}}`
	if err := writeFileForTest(t, path, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "session.snapshots.max") {
		t.Fatalf("expected session.snapshots.max error, got: %v", err)
	}
}

func TestSessionSnapshotsEnvOverrides(t *testing.T) {
	cfg, err := loadWithEnv(t, map[string]string{
		"HAKASE_SESSION_SNAPSHOTS_ENABLED": "0",
		"HAKASE_SESSION_SNAPSHOTS_MAX":     "9",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if SessionSnapshotsEnabled(cfg) {
		t.Error("HAKASE_SESSION_SNAPSHOTS_ENABLED=0 must disable snapshots")
	}
	if SessionSnapshotsMax(cfg) != 9 {
		t.Errorf("max = %d, want 9", SessionSnapshotsMax(cfg))
	}
}

func TestSessionSnapshotsEnvStrictParsing(t *testing.T) {
	t.Run("bad bool", func(t *testing.T) {
		if _, err := loadWithEnv(t, map[string]string{"HAKASE_SESSION_SNAPSHOTS_ENABLED": "maybe"}); err == nil ||
			!strings.Contains(err.Error(), "HAKASE_SESSION_SNAPSHOTS_ENABLED") {
			t.Fatalf("expected strict bool error naming the variable, got: %v", err)
		}
	})
	t.Run("bad max", func(t *testing.T) {
		if _, err := loadWithEnv(t, map[string]string{"HAKASE_SESSION_SNAPSHOTS_MAX": "0"}); err == nil ||
			!strings.Contains(err.Error(), "HAKASE_SESSION_SNAPSHOTS_MAX") {
			t.Fatalf("expected strict positive-int error naming the variable, got: %v", err)
		}
	})
}

// writeFileForTest writes a config body to path (helper mirroring
// loadWithEnv's file setup for cases that need custom bodies).
func writeFileForTest(t *testing.T, path, body string) error {
	t.Helper()
	return os.WriteFile(path, []byte(body), 0o600)
}
