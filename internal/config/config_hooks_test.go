// config_hooks_test.go - hooks config: defaults, validation, strict env
// overrides, envConfigSet participation.
package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksDefaults(t *testing.T) {
	cfg, err := loadWithEnv(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !HooksEnabled(cfg) {
		t.Error("hooks must default to enabled (tri-state nil = on)")
	}
}

func TestHooksExplicitFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"provider":"openai","model_name":"m","api_key":"k","hooks":{"enabled":false}}`
	if err := writeFileForTest(t, path, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if HooksEnabled(cfg) {
		t.Error("explicit enabled:false must disable hooks")
	}
}

func TestHooksBadBlockRejected(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"bad regex", `{"hooks":{"PreToolUse":[{"matcher":"([x","hooks":[{"command":["/bin/true"]}]}]}}`, "matcher"},
		{"bad type", `{"hooks":{"PreToolUse":[{"hooks":[{"type":"http","command":["/bin/true"]}]}]}}`, "type"},
		{"empty command", `{"hooks":{"PreToolUse":[{"hooks":[{"command":[]}]}]}}`, "command"},
		{"bad on_failure", `{"hooks":{"PreToolUse":[{"hooks":[{"command":["/bin/true"],"on_failure":"sometimes"}]}]}}`, "on_failure"},
		{"unknown event", `{"hooks":{"PreTooluse":[{"hooks":[{"command":["/bin/true"]}]}]}}`, "PreTooluse"},
		{"fail-closed post", `{"hooks":{"PostToolUse":[{"hooks":[{"command":["/bin/true"],"on_failure":"block"}]}]}}`, "PreToolUse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			prefix := `{"provider":"openai","model_name":"m","api_key":"k",`
			body := prefix + strings.TrimPrefix(tc.body, "{")
			if err := writeFileForTest(t, path, body); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestHooksValidBlockLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"provider":"openai","model_name":"m","api_key":"k","hooks":{"PreToolUse":[{"matcher":"^system_exec$","hooks":[{"name":"nope","command":["/bin/false"],"timeout":5,"on_failure":"block"}]}]}}`
	if err := writeFileForTest(t, path, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !HooksEnabled(cfg) {
		t.Error("valid hooks block must load enabled")
	}
	h := cfg.Hooks.PreToolUse[0].Hooks[0]
	if h.Timeout != 5 || h.OnFailure != "block" || h.Type != "command" {
		t.Errorf("defaults not applied as expected: %+v", h)
	}
}

func TestHooksEnvOverrides(t *testing.T) {
	cfg, err := loadWithEnv(t, map[string]string{"HAKASE_HOOKS_ENABLED": "0"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if HooksEnabled(cfg) {
		t.Error("HAKASE_HOOKS_ENABLED=0 must disable hooks")
	}
	if _, err := loadWithEnv(t, map[string]string{"HAKASE_HOOKS_ENABLED": "maybe"}); err == nil ||
		!strings.Contains(err.Error(), "HAKASE_HOOKS_ENABLED") {
		t.Fatalf("expected strict bool error naming the variable, got: %v", err)
	}
}

func TestHooksProjectLayerEnvAndDefaults(t *testing.T) {
	cfg, err := loadWithEnv(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !ProjectHooksEnabled(cfg) {
		t.Error("project layer must default to enabled (trust gate still applies)")
	}
	cfg, err = loadWithEnv(t, map[string]string{"HAKASE_HOOKS_PROJECT_ENABLED": "0"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if ProjectHooksEnabled(cfg) {
		t.Error("HAKASE_HOOKS_PROJECT_ENABLED=0 must disable the project layer")
	}
	if _, err := loadWithEnv(t, map[string]string{"HAKASE_HOOKS_PROJECT_ENABLED": "maybe"}); err == nil ||
		!strings.Contains(err.Error(), "HAKASE_HOOKS_PROJECT_ENABLED") {
		t.Fatalf("expected strict bool error naming the variable, got: %v", err)
	}
	if !ProjectHooksEnabled(nil) {
		t.Error("nil config must read project-enabled")
	}
}

func TestHooksSessionStartValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	// Non-empty matcher on SessionStart must fail loudly (it would never fire).
	body := `{"provider":"openai","model_name":"m","api_key":"k","hooks":{"SessionStart":[{"matcher":"x","hooks":[{"command":["/bin/true"]}]}]}}`
	if err := writeFileForTest(t, path, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unconditionally") {
		t.Fatalf("load = %v, want SessionStart-matcher error", err)
	}
	// Empty-matcher SessionStart loads, with defaults applied.
	body = `{"provider":"openai","model_name":"m","api_key":"k","hooks":{"SessionStart":[{"hooks":[{"command":["/bin/true"]}]}]}}`
	if err := writeFileForTest(t, path, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	h := cfg.Hooks.SessionStart[0].Hooks[0]
	if h.Timeout != 30 || h.OnFailure != "allow" || h.Type != "command" {
		t.Errorf("session defaults not applied: %+v", h)
	}
}

func TestHooksUserPromptSubmitValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"provider":"openai","model_name":"m","api_key":"k","hooks":{"UserPromptSubmit":[{"matcher":"x","hooks":[{"command":["/bin/true"]}]}]}}`
	if err := writeFileForTest(t, path, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unconditionally") {
		t.Fatalf("load = %v, want UserPromptSubmit-matcher error", err)
	}
	body = `{"provider":"openai","model_name":"m","api_key":"k","hooks":{"UserPromptSubmit":[{"hooks":[{"command":["/bin/true"],"on_failure":"block"}]}]}}`
	if err := writeFileForTest(t, path, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "PreToolUse") {
		t.Fatalf("load = %v, want fail-closed rejection", err)
	}
}
