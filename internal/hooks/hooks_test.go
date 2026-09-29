package hooks

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDefaultsApplied(t *testing.T) {
	c := Config{PreToolUse: []Group{{Matcher: "x", Hooks: []Handler{{Command: []string{"/bin/true"}}}}}}
	c.ApplyDefaults()
	h := c.PreToolUse[0].Hooks[0]
	if h.Type != HookTypeCommand {
		t.Errorf("type default = %q, want %q", h.Type, HookTypeCommand)
	}
	if h.Timeout != DefaultTimeoutSeconds {
		t.Errorf("timeout default = %d, want %d", h.Timeout, DefaultTimeoutSeconds)
	}
	if h.OnFailure != DefaultOnFailure {
		t.Errorf("on_failure default = %q, want %q", h.OnFailure, DefaultOnFailure)
	}
}

func TestValidateMatrix(t *testing.T) {
	valid := func() Config {
		c := Config{PreToolUse: []Group{{Matcher: "^system_exec$", Hooks: []Handler{{Name: "x", Command: []string{"/bin/true"}}}}}}
		c.ApplyDefaults()
		return c
	}
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"valid", func(c *Config) {}, ""},
		{"bad regex", func(c *Config) { c.PreToolUse[0].Matcher = "([unclosed" }, "matcher"},
		{"bad type", func(c *Config) { c.PreToolUse[0].Hooks[0].Type = "http" }, "type"},
		{"empty command", func(c *Config) { c.PreToolUse[0].Hooks[0].Command = nil }, "command"},
		{"blank argv0", func(c *Config) { c.PreToolUse[0].Hooks[0].Command = []string{"  "} }, "command"},
		{"negative timeout", func(c *Config) { c.PreToolUse[0].Hooks[0].Timeout = -1 }, "timeout"},
		{"bad on_failure", func(c *Config) { c.PreToolUse[0].Hooks[0].OnFailure = "sometimes" }, "on_failure"},
		{"empty group", func(c *Config) { c.PreToolUse[0].Hooks = nil }, "no hooks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr == "" && err != nil {
				t.Fatalf("Validate = %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Validate = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateRejectsFailClosedPostToolUse(t *testing.T) {
	c := Config{PostToolUse: []Group{{Hooks: []Handler{{Command: []string{"/bin/true"}, OnFailure: OnFailureBlock}}}}}
	c.ApplyDefaults()
	// ApplyDefaults does not touch an explicit block; Validate must reject it.
	c.PostToolUse[0].Hooks[0].OnFailure = OnFailureBlock
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "PreToolUse") {
		t.Fatalf("Validate = %v, want PreToolUse-only error", err)
	}
}

func TestUnmarshalRejectsUnknownEvent(t *testing.T) {
	var c Config
	body := `{"enabled":true,"PreTooluse":[{"matcher":"x","hooks":[{"command":["/bin/true"]}]}]}`
	if err := json.Unmarshal([]byte(body), &c); err == nil || !strings.Contains(err.Error(), "PreTooluse") {
		t.Fatalf("unmarshal = %v, want unknown-key error naming PreTooluse", err)
	}
}

func TestFingerprintChangesWithScriptBytes(t *testing.T) {
	dir := t.TempDir()
	p1 := dir + "/hook.sh"
	if err := os.WriteFile(p1, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	h1 := Handler{Command: []string{p1}}
	f1 := h1.Fingerprint()
	if !strings.HasPrefix(f1, "sha256:") {
		t.Fatalf("fingerprint %q must carry the sha256: prefix", f1)
	}
	// Same path, rewritten body: the fingerprint must change (name-stable
	// rewrites are exactly the gemini-cli#27900 attack).
	if err := os.WriteFile(p1, []byte("#!/bin/sh\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if f2 := h1.Fingerprint(); f2 == f1 {
		t.Error("fingerprint unchanged after script body rewrite; names must never be trust identities")
	}
	// Different argv, same absence of file: must differ too.
	h2 := Handler{Command: []string{p1, "--other"}}
	if h2.Fingerprint() == f1 {
		t.Error("fingerprint unchanged after argv change")
	}
}

func TestEnabledTriState(t *testing.T) {
	if !Enabled(nil) {
		t.Error("nil config must read enabled (MemoryConfig pattern)")
	}
	var c Config
	if !Enabled(&c) {
		t.Error("absent enabled must read enabled")
	}
	f := false
	c.Enabled = &f
	if Enabled(&c) {
		t.Error("explicit false must disable")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := TruncateRunes("héllo wörld", 5); got != "héllo" {
		t.Errorf("truncate = %q, want rune-safe prefix", got)
	}
	if got := TruncateRunes("short", 10); got != "short" {
		t.Errorf("truncate short = %q, want unchanged", got)
	}
}
