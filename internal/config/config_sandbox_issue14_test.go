package config

import (
	"strings"
	"testing"
)

// TestLoadConfigRefusesLandlock pins issue #14: sandbox.mode landlock is
// reserved but unimplemented, so config load must fail loudly instead of
// silently degrading to path-auditing-only exec.
func TestLoadConfigRefusesLandlock(t *testing.T) {
	path := writeTempConfig(t, `{
		"provider": "openai",
		"model_name": "gpt-4o-mini",
		"api_key": "test-key",
		"sandbox": {"mode": "landlock"}
	}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "landlock") {
		t.Fatalf("LoadConfig(landlock) = %v, want landlock refusal error", err)
	}
}

// TestLoadConfigAcceptsSupportedSandboxModes guards against over-correction:
// paths, bubblewrap, and off must keep loading.
func TestLoadConfigAcceptsSupportedSandboxModes(t *testing.T) {
	for _, mode := range []string{"paths", "bubblewrap", "off"} {
		path := writeTempConfig(t, `{
			"provider": "openai",
			"model_name": "gpt-4o-mini",
			"api_key": "test-key",
			"sandbox": {"mode": "`+mode+`"}
		}`)
		if _, err := LoadConfig(path); err != nil {
			t.Errorf("LoadConfig(%q) = %v, want nil", mode, err)
		}
	}
}
