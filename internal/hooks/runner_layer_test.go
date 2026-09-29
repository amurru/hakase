package hooks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"amurru/hakase/internal/project"
)

// sessionCtx carries an ADK-style SessionID over a real Go context (the
// zero-dependency stand-in for a registered run).
type sessionCtx struct {
	context.Context
	sessionID string
}

func (c sessionCtx) SessionID() string { return c.sessionID }

func projectCtx(t *testing.T, root, session string) context.Context {
	t.Helper()
	// NOTE: the root goes through the process identity (SetCurrentRoot),
	// not project.WithRoot: WithRoot wraps the context in a valueCtx that
	// hides the SessionID method from type assertion, while production
	// ADK contexts carry both natively.
	if root != "" {
		project.SetCurrentRoot(root)
		t.Cleanup(func() { project.SetCurrentRoot("") })
	}
	ctx := context.Background()
	if session != "" {
		ctx = sessionCtx{Context: ctx, sessionID: session}
	}
	return ctx
}

// layeredRunner builds a user+project runner directly (no trust store file):
// project handlers are gated by the given checker.
func layeredRunner(t *testing.T, user Config, root, projectBody string, trust TrustChecker) *Runner {
	t.Helper()
	if projectBody != "" {
		dir := filepath.Join(root, ".hakase")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(projectBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewRunner(user)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r.SetTrustStore(trust)
	return r
}

type mapTrust map[string]bool

func (m mapTrust) Trusted(fp string) bool { return m[fp] }

func TestProjectHookSkippedWhenUntrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	root := t.TempDir()
	r := layeredRunner(t, Config{}, root,
		`{"PreToolUse":[{"hooks":[{"name":"evil","command":["/bin/sh","-c","exit 2"]}]}]}`,
		mapTrust{})
	var warns []string
	r.SetLog(func(s string) { warns = append(warns, s) })

	blocked, _ := r.CheckPreToolUse(projectCtx(t, root, ""), "any_tool", nil)
	if blocked {
		t.Fatal("untrusted project hook must never block (it must not run at all)")
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "hakase hooks trust") {
		t.Errorf("skip must warn with the trust command, got %v", warns)
	}
	// Warn-once: a second call skips silently.
	r.CheckPreToolUse(projectCtx(t, root, ""), "any_tool", nil)
	if len(warns) != 1 {
		t.Errorf("untrusted skip must warn once per process, got %d warnings", len(warns))
	}
}

func TestProjectHookRunsWhenTrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	root := t.TempDir()
	// Trust map is filled after load (fingerprint is content-addressed).
	r := layeredRunner(t, Config{}, root,
		`{"PreToolUse":[{"matcher":"^system_exec$","hooks":[{"name":"guard","command":["/bin/sh","-c","echo no >&2; exit 2"]}]}]}`,
		mapTrust{})
	snaps := r.ProjectSnapshots(root)
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %v, want 1", snaps)
	}
	if snaps[0].Trusted {
		t.Error("project hook must start untrusted")
	}
	if snaps[0].Layer != "project" {
		t.Errorf("layer = %q, want project", snaps[0].Layer)
	}
	trust := mapTrust{snaps[0].Fingerprint: true}
	r.SetTrustStore(trust)

	blocked, res := r.CheckPreToolUse(projectCtx(t, root, ""), "system_exec", nil)
	if !blocked {
		t.Fatal("trusted project hook must enforce the gate")
	}
	if res["blocked_by"] != "hook" || res["hook"] != "guard" {
		t.Errorf("block result = %v", res)
	}
}

func TestTrustLapsesOnScriptRewrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	root := t.TempDir()
	script := filepath.Join(root, "guard.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := layeredRunner(t, Config{}, root,
		`{"PreToolUse":[{"hooks":[{"command":["`+script+`"]}]}]}`,
		mapTrust{})
	fp1 := r.ProjectSnapshots(root)[0].Fingerprint
	r.SetTrustStore(mapTrust{fp1: true})
	if blocked, _ := r.CheckPreToolUse(projectCtx(t, root, ""), "t", nil); blocked {
		t.Fatal("trusted allow-hook must allow")
	}
	// Rewrite the body (the gemini-cli#27900 attack): same path, new bytes.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fp2 := r.ProjectSnapshots(root)[0].Fingerprint
	if fp2 == fp1 {
		t.Fatal("fingerprint must change when the script body changes")
	}
	blocked, _ := r.CheckPreToolUse(projectCtx(t, root, ""), "t", nil)
	if blocked {
		t.Fatal("rewritten script must NOT run under the old trust (fail closed by skipping)")
	}
}

func TestUserLayerRunsBeforeProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	root := t.TempDir()
	user := Config{PreToolUse: []Group{{Hooks: []Handler{{Name: "user-first", Command: []string{"/bin/sh", "-c", "echo user >&2; exit 2"}}}}}}
	user.ApplyDefaults()
	r := layeredRunner(t, user, root,
		`{"PreToolUse":[{"hooks":[{"name":"project-second","command":["/bin/sh","-c","exit 2"]}]}]}`,
		mapTrust{})
	for _, s := range r.ProjectSnapshots(root) {
		r.trust.(mapTrust)[s.Fingerprint] = true
	}
	_, res := r.CheckPreToolUse(projectCtx(t, root, ""), "t", nil)
	if res["hook"] != "user-first" {
		t.Errorf("first block wins: got %v, want the user hook's denial", res["hook"])
	}
}

func TestMasterDisabledKillsProjectLayer(t *testing.T) {
	root := t.TempDir()
	off := false
	r := layeredRunner(t, Config{Enabled: &off}, root,
		`{"PreToolUse":[{"hooks":[{"command":["/bin/sh","-c","exit 2"]}]}]}`,
		mapTrust{"sha256:anything": true})
	if r.Enabled() {
		t.Error("master-disabled runner must be disabled")
	}
	// Even a wildcard-trusting store must not matter: the layer is off.
	if blocked, _ := r.CheckPreToolUse(projectCtx(t, root, ""), "t", nil); blocked {
		t.Error("master-disabled runner must allow everything")
	}
	if r.HasSessionStart() {
		t.Error("master-disabled runner must not offer SessionStart")
	}
}

func TestProjectLayerKillSwitch(t *testing.T) {
	root := t.TempDir()
	off := false
	r, err := NewRunner(Config{Project: ProjectConfig{Enabled: &off}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".hakase")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(`{"PreToolUse":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := r.ProjectSnapshots(root); got != nil {
		t.Errorf("disabled project layer must yield no snapshots, got %v", got)
	}
}

func TestSessionStartFiresOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	user := Config{SessionStart: []Group{{Hooks: []Handler{{Command: []string{"/bin/sh", "-c", `printf '{"hookSpecificOutput":{"additionalContext":"projctx"}}'`}}}}}}
	user.ApplyDefaults()
	r, err := NewRunner(user)
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasSessionStart() {
		t.Fatal("session groups must advertise SessionStart")
	}
	ctx := projectCtx(t, t.TempDir(), "sess-1")
	if got := r.RunSessionStart(ctx); got != "projctx" {
		t.Errorf("first fire = %q, want injected context", got)
	}
	if got := r.RunSessionStart(ctx); got != "" {
		t.Errorf("second fire = %q, want empty (once per session)", got)
	}
	// A different session fires independently.
	ctx2 := projectCtx(t, t.TempDir(), "sess-2")
	if got := r.RunSessionStart(ctx2); got != "projctx" {
		t.Errorf("other session = %q, want independent fire", got)
	}
}

func TestSessionStartPlainStdoutIsContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	user := Config{SessionStart: []Group{{Hooks: []Handler{{Command: []string{"/bin/sh", "-c", "echo hello-project"}}}}}}
	user.ApplyDefaults()
	r, err := NewRunner(user)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.RunSessionStart(projectCtx(t, t.TempDir(), "s")); got != "hello-project" {
		t.Errorf("plain stdout = %q, want verbatim context", got)
	}
}

func TestSessionStartExit2WarnsAndRollsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	user := Config{SessionStart: []Group{{Hooks: []Handler{{Command: []string{"/bin/sh", "-c", "echo cantblock >&2; exit 2"}}}}}}
	user.ApplyDefaults()
	r, err := NewRunner(user)
	if err != nil {
		t.Fatal(err)
	}
	var warns []string
	r.SetLog(func(s string) { warns = append(warns, s) })
	ctx := projectCtx(t, t.TempDir(), "s")
	if got := r.RunSessionStart(ctx); got != "" {
		t.Errorf("exit-2 SessionStart must inject nothing, got %q", got)
	}
	if len(warns) == 0 {
		t.Error("exit-2 SessionStart must warn")
	}
	// Empty result rolls back: still eligible (a fixed hook fires later).
	if got := r.RunSessionStart(ctx); got != "" {
		t.Errorf("rolled-back session must stay eligible, got %q", got)
	}
}

func TestSessionStartProjectTrustGated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	root := t.TempDir()
	r := layeredRunner(t, Config{}, root,
		`{"SessionStart":[{"hooks":[{"name":"evil-start","command":["/bin/sh","-c","echo pwned"]}]}]}`,
		mapTrust{})
	ctx := projectCtx(t, root, "sess-p")
	if got := r.RunSessionStart(ctx); got != "" {
		t.Errorf("untrusted project SessionStart must inject nothing, got %q", got)
	}
	// The empty result must NOT mark the session fired: a mid-session trust
	// grant fires on the next call (the rollback property).
	for _, s := range r.ProjectSnapshots(root) {
		r.trust.(mapTrust)[s.Fingerprint] = true
	}
	if got := r.RunSessionStart(ctx); got != "pwned" {
		t.Errorf("post-trust fire = %q, want the context", got)
	}
	if got := r.RunSessionStart(ctx); got != "" {
		t.Errorf("second post-trust fire = %q, want empty", got)
	}
}
