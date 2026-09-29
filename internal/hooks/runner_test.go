package hooks

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
)

func errExecFailed() error { return errors.New("exec: binary not found") }

func TestParseVerdictMatrix(t *testing.T) {
	cases := []struct {
		name       string
		exit       int
		stdout     string
		stderr     string
		wantBlock  bool
		wantErr    bool
		wantReason string
		wantCtx    string
	}{
		{"exit2 blocks with stderr", 2, "", "no rm here\n", true, false, "no rm here", ""},
		{"exit2 empty stderr", 2, "", "", true, false, "hook exited 2", ""},
		{"exit0 empty allows", 0, "", "", false, false, "", ""},
		{"exit0 plaintext allows", 0, "just a note\n", "", false, false, "", ""},
		{"exit0 modern deny", 0, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"destructive"}}`, "", true, false, "destructive", ""},
		{"exit0 snake_case deny", 0, `{"hook_specific_output":{"permission_decision":"deny","reason":"nope"}}`, "", true, false, "nope", ""},
		{"exit0 legacy decision block", 0, `{"decision":"block","reason":"legacy"}`, "", true, false, "legacy", ""},
		{"exit0 allow stays allow", 0, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`, "", false, false, "", ""},
		{"exit0 context carried", 0, `{"hookSpecificOutput":{"additionalContext":"watch out"}}`, "", false, false, "", "watch out"},
		{"exit0 context list", 0, `{"hookSpecificOutput":{"additionalContext":["a","b"]}}`, "", false, false, "", "a\n\nb"},
		{"exit0 garbage json errors", 0, `{"decision":`, "", false, true, "", ""},
		{"exit1 is hook error", 1, "", "boom", false, true, "", ""},
		{"continue false blocks", 0, `{"continue":false}`, "", true, false, "continue:false", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := parseExit(30, tc.exit, []byte(tc.stdout), []byte(tc.stderr))
			if v.block != tc.wantBlock {
				t.Errorf("block = %v, want %v (reason %q err %q)", v.block, tc.wantBlock, v.reason, v.hookErr)
			}
			if tc.wantErr && v.hookErr == "" {
				t.Errorf("want hookErr, got none (reason %q)", v.reason)
			}
			if !tc.wantErr && v.hookErr != "" {
				t.Errorf("hookErr = %q, want none", v.hookErr)
			}
			if tc.wantReason != "" && !strings.Contains(v.reason, tc.wantReason) {
				t.Errorf("reason = %q, want containing %q", v.reason, tc.wantReason)
			}
			if tc.wantCtx != "" && v.additionalContext != tc.wantCtx {
				t.Errorf("context = %q, want %q", v.additionalContext, tc.wantCtx)
			}
		})
	}
}

func TestParseVerdictWiring(t *testing.T) {
	// Timeout and exec-failure branches of parseVerdict (the exit-code rows
	// live in TestParseVerdictMatrix via parseExit).
	if v := parseVerdict(30, nil, nil, true, nil); v.hookErr == "" || !strings.Contains(v.hookErr, "timed out") {
		t.Errorf("timeout = %+v, want timed-out hookErr", v)
	}
	if v := parseVerdict(30, nil, nil, false, errExecFailed()); v.hookErr == "" {
		t.Errorf("exec failure = %+v, want hookErr", v)
	}
}

func TestMatcherSemantics(t *testing.T) {
	r, err := NewRunner(Config{PreToolUse: []Group{
		{Matcher: "", Hooks: []Handler{{Command: []string{"sh"}}}},
		{Matcher: "system_exec", Hooks: []Handler{{Command: []string{"sh"}}}},
		{Matcher: "^git_commit$", Hooks: []Handler{{Command: []string{"sh"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.pre[0].matches("anything_at_all") {
		t.Error("empty matcher must match all")
	}
	if !r.pre[1].matches("my_system_exec_tool") {
		t.Error("unanchored matcher must substring-match")
	}
	if r.pre[1].matches("system") {
		t.Error("unanchored matcher must not match a superstring gap")
	}
	if !r.pre[2].matches("git_commit") {
		t.Error("anchored matcher must match exact")
	}
	if r.pre[2].matches("prefix_git_commit") {
		t.Error("anchored matcher must not substring-match")
	}
}

func TestNoOpRunner(t *testing.T) {
	var nilRunner *Runner
	if blocked, _ := nilRunner.CheckPreToolUse(context.Background(), "x", nil); blocked {
		t.Error("nil runner must allow")
	}
	if out := nilRunner.CheckPostToolUse(context.Background(), "x", nil, map[string]any{"a": 1}); out != nil {
		t.Error("nil runner must pass results through")
	}
	empty, err := NewRunner(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Enabled() {
		t.Error("group-less runner must be disabled")
	}
	off := false
	disabled, err := NewRunner(Config{Enabled: &off, PreToolUse: []Group{{Hooks: []Handler{{Command: []string{"sh"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled() {
		t.Error("explicitly disabled runner must be disabled")
	}
}

// --- process-spawning tests (unix shell; skipped on windows) ---

func testShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn hook tests are unix-only")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	return "/bin/sh"
}

func runnerWith(t *testing.T, groups ...Group) *Runner {
	t.Helper()
	r, err := NewRunner(Config{PreToolUse: groups})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

func TestPreToolUseExit2Blocks(t *testing.T) {
	sh := testShell(t)
	r := runnerWith(t, Group{Matcher: "^system_exec$", Hooks: []Handler{{Name: "nope", Command: []string{sh, "-c", "echo denied >&2; exit 2"}}}})
	blocked, res := r.CheckPreToolUse(context.Background(), "system_exec", map[string]any{"command": "rm -rf /"})
	if !blocked {
		t.Fatal("exit-2 hook must block")
	}
	if res["blocked_by"] != "hook" || res["hook"] != "nope" {
		t.Errorf("block result = %v, want blocked_by=hook hook=nope", res)
	}
	if s, _ := res["error"].(string); !strings.Contains(s, "denied") {
		t.Errorf("block error = %q, want stderr reason", s)
	}
	// Non-matching tool must pass.
	if blocked, _ := r.CheckPreToolUse(context.Background(), "read_file", nil); blocked {
		t.Error("non-matching tool must not be blocked")
	}
}

func TestPreToolUseExit0JSONBlocks(t *testing.T) {
	sh := testShell(t)
	body := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"json says no"}}`
	r := runnerWith(t, Group{Hooks: []Handler{{Command: []string{sh, "-c", "printf '%s' '" + body + "'"}}}})
	blocked, res := r.CheckPreToolUse(context.Background(), "any_tool", nil)
	if !blocked {
		t.Fatal("JSON-deny hook must block")
	}
	if s, _ := res["error"].(string); !strings.Contains(s, "json says no") {
		t.Errorf("block error = %q", s)
	}
}

func TestPreToolUseFailOpenAndClosed(t *testing.T) {
	sh := testShell(t)
	open, err := NewRunner(Config{PreToolUse: []Group{{Hooks: []Handler{{Command: []string{sh, "-c", "exit 1"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var warns []string
	open.SetLog(func(s string) { warns = append(warns, s) })
	if blocked, _ := open.CheckPreToolUse(context.Background(), "t", nil); blocked {
		t.Error("on_failure:allow (default) must fail open")
	}
	if len(warns) == 0 {
		t.Error("fail-open path must log a warning")
	}
	closed, err := NewRunner(Config{PreToolUse: []Group{{Hooks: []Handler{{Command: []string{sh, "-c", "exit 1"}, OnFailure: OnFailureBlock}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if blocked, res := closed.CheckPreToolUse(context.Background(), "t", nil); !blocked || res["blocked_by"] != "hook" {
		t.Errorf("on_failure:block must fail closed, got %v", res)
	}
}

func TestPreToolUseDenyWinsOrdering(t *testing.T) {
	sh := testShell(t)
	r := runnerWith(t, Group{Hooks: []Handler{
		{Command: []string{sh, "-c", "exit 0"}},
		{Name: "second", Command: []string{sh, "-c", "exit 2"}},
		{Name: "never", Command: []string{sh, "-c", "exit 2"}},
	}})
	blocked, res := r.CheckPreToolUse(context.Background(), "t", nil)
	if !blocked {
		t.Fatal("second handler must block")
	}
	if res["hook"] != "second" {
		t.Errorf("first block wins, got %v", res["hook"])
	}
}

func TestPostToolUseAppendsContext(t *testing.T) {
	sh := testShell(t)
	r, err := NewRunner(Config{PostToolUse: []Group{{Hooks: []Handler{
		{Command: []string{sh, "-c", `printf '{"hookSpecificOutput":{"additionalContext":"lint clean"}}'`}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	orig := map[string]any{"ok": true}
	out := r.CheckPostToolUse(context.Background(), "write_file", nil, orig)
	if out == nil {
		t.Fatal("context hook must override the result")
	}
	if out["ok"] != true {
		t.Error("override must preserve the original result keys")
	}
	if out[AdditionalContextKey] != "lint clean" {
		t.Errorf("context = %v", out[AdditionalContextKey])
	}
	if _, dup := orig[AdditionalContextKey]; dup {
		t.Error("override must copy, never mutate the original result")
	}
}

func TestPostToolUseExit2IsContextNotBlock(t *testing.T) {
	sh := testShell(t)
	r, err := NewRunner(Config{PostToolUse: []Group{{Hooks: []Handler{
		{Command: []string{sh, "-c", "echo post says no >&2; exit 2"}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	out := r.CheckPostToolUse(context.Background(), "t", nil, map[string]any{"ok": true})
	if out == nil {
		t.Fatal("exit-2 on PostToolUse must surface as context, not vanish")
	}
	if s, _ := out[AdditionalContextKey].(string); !strings.Contains(s, "post says no") {
		t.Errorf("context = %q", s)
	}
}

func TestHookTimeoutKills(t *testing.T) {
	sh := testShell(t)
	r, err := NewRunner(Config{PreToolUse: []Group{{Hooks: []Handler{
		{Command: []string{sh, "-c", "sleep 30"}, Timeout: 1, OnFailure: OnFailureBlock},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	blocked, res := r.CheckPreToolUse(context.Background(), "t", nil)
	if !blocked {
		t.Fatal("timed-out fail-closed hook must block")
	}
	if s, _ := res["error"].(string); !strings.Contains(s, "timed out") {
		t.Errorf("timeout block error = %q", s)
	}
}

func TestHookEnvRedaction(t *testing.T) {
	t.Setenv("HAKASE_TEST_API_KEY", "supersecret")
	t.Setenv("HAKASE_TEST_PLAIN", "visible")
	env := hookEnv("PreToolUse", payload{ToolName: "t"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "supersecret") {
		t.Error("KEY-named env value must be redacted from hook env")
	}
	if !strings.Contains(joined, "HAKASE_TEST_PLAIN=visible") {
		t.Error("non-secret env must pass through")
	}
	for _, want := range []string{"HAKASE_HOOK_EVENT=PreToolUse", "HAKASE_TOOL_NAME=t"} {
		if !strings.Contains(joined, want) {
			t.Errorf("hook env missing %q", want)
		}
	}
}

func TestHookPayloadSessionBestEffort(t *testing.T) {
	p := buildPayload(context.Background(), EventPreToolUse, "t", nil, nil)
	if p.HookEventName != EventPreToolUse || p.ToolName != "t" {
		t.Errorf("payload = %+v", p)
	}
	if p.Timestamp == "" || p.CWD == "" {
		t.Errorf("payload must carry timestamp and cwd, got %+v", p)
	}
}
