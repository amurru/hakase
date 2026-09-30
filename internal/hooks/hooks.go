// Package hooks implements user-configurable tool-lifecycle hooks
// (docs/hooks/spec.md, issue #20 Tier-2 item).
//
// First cut: PreToolUse (blockable) + PostToolUse (observability only),
// command handlers in exec form (argv, no shell), user-scope config only.
// Project-scope hooks and the content-hash trust store are Phase 2.
//
// This is a leaf package: it imports only the stdlib plus the leaf
// internal/interfaces and internal/project packages. It deliberately does
// NOT import internal/config (which imports this package for the on-disk
// shape) and does NOT import internal/agent (matching the internal/sleep
// precedent). The ADK callback signatures live in internal/agent, which
// adapts them to Runner.CheckPreToolUse/CheckPostToolUse.
package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Lifecycle events. PreToolUse/PostToolUse fire around tool calls;
// SessionStart fires once per session (see RunSessionStart);
// UserPromptSubmit fires on every user prompt (see RunUserPromptSubmit).
// Anything else in config is a load-time error.
const (
	EventPreToolUse       = "PreToolUse"
	EventPostToolUse      = "PostToolUse"
	EventSessionStart     = "SessionStart"
	EventUserPromptSubmit = "UserPromptSubmit"
)

// Handler defaults and limits.
const (
	// DefaultTimeoutSeconds bounds one handler run. 30s (Crush/Goose-style),
	// not Claude Code's 600s: a blocking gate on an interactive turn must
	// not hang for minutes.
	DefaultTimeoutSeconds = 30
	// DefaultOnFailure is fail-open: a broken hook never wedges a session.
	// Users who want the gate strict set on_failure:block on PreToolUse.
	DefaultOnFailure = "allow"
	// HookTypeCommand is the only handler type in v1.
	HookTypeCommand = "command"
	// MaxReasonRunes truncates hook-supplied block reasons.
	MaxReasonRunes = 500
	// maxScriptHashBytes bounds how much of a local hook script is mixed
	// into the content fingerprint.
	maxScriptHashBytes = 1 << 20
)

// On-failure policies.
const (
	OnFailureAllow = "allow"
	OnFailureBlock = "block"
)

// Handler is one command to run when its group matches. Command is an argv
// array executed with no shell (Claude Code's exec form); shell strings are
// not supported in v1.
type Handler struct {
	// Name is free text for logs, `hakase hooks list`, and audit entries.
	// It is NEVER a trust identity (see Fingerprint).
	Name string `json:"name,omitempty"`
	// Type must be "command" (the only v1 type).
	Type string `json:"type,omitempty"`
	// Command is the argv to exec. Must be non-empty.
	Command []string `json:"command,omitempty"`
	// Timeout bounds one run in seconds. 0 = DefaultTimeoutSeconds.
	Timeout int `json:"timeout,omitempty"`
	// OnFailure decides a hook error (non-2 exit, timeout, exec failure,
	// unparseable stdout): "allow" (default, proceed) or "block" (deny).
	// "block" is only meaningful on PreToolUse; Config.Validate rejects it
	// on PostToolUse handlers.
	OnFailure string `json:"on_failure,omitempty"`
	// Enabled tri-state: nil (default) = on; explicit false skips the
	// handler on every event without executing it. Toggling never affects
	// the content fingerprint (Fingerprint hashes argv + script bytes
	// only), so disable/enable never lapses trust.
	Enabled *bool `json:"enabled,omitempty"`
}

// IsEnabled reports whether the handler may execute. Nil (unset) means
// on, matching the Enabled/ProjectConfig tri-state pattern.
func (h Handler) IsEnabled() bool {
	return h.Enabled == nil || *h.Enabled
}

// Group pairs a tool-name matcher with the handlers to run, in order.
type Group struct {
	// Matcher is an unanchored regex tested against the tool name
	// (Claude-compatible: "system_exec" matches any name containing it;
	// anchor ^...$ for exact). Empty matches every tool.
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []Handler `json:"hooks,omitempty"`
}

// Config is the on-disk "hooks" block. internal/config embeds this type
// directly so there is exactly one shape.
type Config struct {
	// Enabled tri-state: nil (default) = on; explicit false disables hooks
	// entirely (MemoryConfig pattern). With no groups configured the runner
	// is a no-op either way.
	Enabled *bool `json:"enabled,omitempty"`
	// PreToolUse groups run before the tool; they can block.
	PreToolUse []Group `json:"PreToolUse,omitempty"`
	// PostToolUse groups run after the tool; observability only.
	PostToolUse []Group `json:"PostToolUse,omitempty"`
	// SessionStart groups run once per session; their stdout/context is
	// injected into the first turn. Matchers must be empty (no meaningful
	// match target exists at session start).
	SessionStart []Group `json:"SessionStart,omitempty"`
	// UserPromptSubmit groups run on every user prompt; their
	// stdout/context is prepended to the turn. Matchers must be empty
	// (Claude parity: the event carries no matcher target).
	UserPromptSubmit []Group `json:"UserPromptSubmit,omitempty"`
	// Project tunes the project layer (<root>/.hakase/hooks.json).
	Project ProjectConfig `json:"project,omitempty"`
}

// ProjectConfig tunes project-scope hooks (Phase 2, spec HK-101).
type ProjectConfig struct {
	// Enabled tri-state: nil (default) = the project layer loads (hooks
	// still need per-hook trust before they execute); explicit false
	// disables project files entirely.
	Enabled *bool `json:"enabled,omitempty"`
}

// ApplyDefaults fills zero values. Call before Validate (NewRunner and
// LoadConfig both do this, so the order is safe anywhere).
func (c *Config) ApplyDefaults() {
	if c == nil {
		return
	}
	for i := range c.PreToolUse {
		c.PreToolUse[i].applyDefaults()
	}
	for i := range c.PostToolUse {
		c.PostToolUse[i].applyDefaults()
	}
	for i := range c.SessionStart {
		c.SessionStart[i].applyDefaults()
	}
	for i := range c.UserPromptSubmit {
		c.UserPromptSubmit[i].applyDefaults()
	}
}

func (g *Group) applyDefaults() {
	for i := range g.Hooks {
		h := &g.Hooks[i]
		if h.Type == "" {
			h.Type = HookTypeCommand
		}
		if h.Timeout == 0 {
			h.Timeout = DefaultTimeoutSeconds
		}
		if h.OnFailure == "" {
			h.OnFailure = DefaultOnFailure
		}
	}
}

// Validate checks the whole block. Bad matcher regex, non-command types,
// empty commands, negative timeouts, bad on_failure values,
// on_failure:block off PreToolUse, and non-empty SessionStart matchers are
// all errors with the group/handler index attached.
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	for i := range c.PreToolUse {
		if err := c.PreToolUse[i].validate(fmt.Sprintf("hooks.PreToolUse[%d]", i), EventPreToolUse); err != nil {
			return err
		}
	}
	for i := range c.PostToolUse {
		if err := c.PostToolUse[i].validate(fmt.Sprintf("hooks.PostToolUse[%d]", i), EventPostToolUse); err != nil {
			return err
		}
	}
	for i := range c.SessionStart {
		if err := c.SessionStart[i].validate(fmt.Sprintf("hooks.SessionStart[%d]", i), EventSessionStart); err != nil {
			return err
		}
	}
	for i := range c.UserPromptSubmit {
		if err := c.UserPromptSubmit[i].validate(fmt.Sprintf("hooks.UserPromptSubmit[%d]", i), EventUserPromptSubmit); err != nil {
			return err
		}
	}
	return nil
}

func (g *Group) validate(where string, event string) error {
	if g.Matcher != "" {
		if event == EventSessionStart || event == EventUserPromptSubmit {
			return fmt.Errorf("invalid %s.matcher %q: %s handlers run unconditionally; a matcher would silently never fire", where, g.Matcher, event)
		}
		if _, err := regexp.Compile(g.Matcher); err != nil {
			return fmt.Errorf("invalid %s.matcher %q: %v", where, g.Matcher, err)
		}
	}
	if len(g.Hooks) == 0 {
		return fmt.Errorf("invalid %s: no hooks (group with a matcher but no handlers never fires)", where)
	}
	for i := range g.Hooks {
		if err := g.Hooks[i].validate(fmt.Sprintf("%s.hooks[%d]", where, i), event); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) validate(where string, event string) error {
	if h.Type != HookTypeCommand {
		return fmt.Errorf("invalid %s.type %q: only %q is supported in v1", where, h.Type, HookTypeCommand)
	}
	if len(h.Command) == 0 || strings.TrimSpace(h.Command[0]) == "" {
		return fmt.Errorf("invalid %s.command: must be a non-empty argv array (exec form, no shell)", where)
	}
	if h.Timeout < 0 {
		return fmt.Errorf("invalid %s.timeout %d: must be >= 0 (0 = default %ds)", where, h.Timeout, DefaultTimeoutSeconds)
	}
	if h.OnFailure != OnFailureAllow && h.OnFailure != OnFailureBlock {
		return fmt.Errorf("invalid %s.on_failure %q: must be %q or %q", where, h.OnFailure, OnFailureAllow, OnFailureBlock)
	}
	if event != EventPreToolUse && h.OnFailure == OnFailureBlock {
		return fmt.Errorf("invalid %s.on_failure %q: fail-closed is only meaningful on PreToolUse (%s cannot block)", where, h.OnFailure, event)
	}
	return nil
}

// UnmarshalJSON rejects unknown top-level keys so a mistyped event name
// (e.g. "PreTooluse") fails loudly instead of silently never firing.
func (c *Config) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type plain Config // avoid recursion
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	for k := range raw {
		switch k {
		case "enabled", "PreToolUse", "PostToolUse", "SessionStart", "UserPromptSubmit", "project":
		default:
			return fmt.Errorf("invalid hooks.%s: unknown key (want one of enabled, PreToolUse, PostToolUse, SessionStart, UserPromptSubmit, project)", k)
		}
	}
	*c = Config(p)
	return nil
}

// ProjectLayerEnabled reports the project-layer switch: nil (absent) = on
// (project files load but their hooks still need per-hook trust before
// they execute); explicit false disables project files entirely.
func ProjectLayerEnabled(c *Config) bool {
	if c == nil || c.Project.Enabled == nil {
		return true
	}
	return *c.Project.Enabled
}

// Enabled reports the tri-state switch: nil (absent) = on, matching the
// MemoryConfig pattern. Note this only gates the block; a config with no
// groups is a no-op runner either way.
func Enabled(c *Config) bool {
	if c == nil || c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// Fingerprint content-addresses one handler for display (`hakase hooks
// list`) and for the Phase-2 trust store: the hash covers the resolved argv
// plus the bytes of a local script when argv[0] is a file on disk. It is
// NEVER the handler name: an attacker who can rewrite a script body while
// keeping its name must change the fingerprint (gemini-cli#27900).
func (h *Handler) Fingerprint() string {
	sum := sha256.New()
	for _, a := range h.Command {
		sum.Write([]byte(a))
		sum.Write([]byte{0})
	}
	if len(h.Command) > 0 {
		if f, err := os.Open(h.Command[0]); err == nil {
			// ReadFull (not a single Read): a short read must not produce
			// a run-dependent fingerprint for the same file.
			buf := make([]byte, maxScriptHashBytes)
			n, _ := io.ReadFull(f, buf)
			sum.Write(buf[:n])
			_ = f.Close()
		}
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil))
}

// TruncateRunes caps s at n runes (hook output is attacker-influenced only
// in the project-scope future, but bounded reasons keep audit/model text
// sane regardless).
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
