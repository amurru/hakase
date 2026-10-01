package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func errExecFailed() error { return errors.New("exec: binary not found") }

func errHookBoom() error { return errors.New("tool exploded") }

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
	if out := nilRunner.CheckPostToolUse(context.Background(), "x", nil, map[string]any{"a": 1}, nil); out != nil {
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
	out := r.CheckPostToolUse(context.Background(), "write_file", nil, orig, nil)
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
	out := r.CheckPostToolUse(context.Background(), "t", nil, map[string]any{"ok": true}, nil)
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

// TestPostToolUseFailedToolPreservesError pins the ADK constraint behind
// CheckPostToolUse: ADK drops the tool error whenever an AfterTool callback
// returns a non-nil result, so a context override on a failed call would
// silently convert the failure into a success. The override must be
// suppressed (nil) and the context must go to the warn log instead.
func TestPostToolUseFailedToolPreservesError(t *testing.T) {
	sh := testShell(t)
	r, err := NewRunner(Config{PostToolUse: []Group{{Hooks: []Handler{
		{Command: []string{sh, "-c", `printf '{"hookSpecificOutput":{"additionalContext":"lint clean"}}'`}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	var warns []string
	r.SetLog(func(s string) { warns = append(warns, s) })
	toolErr := errHookBoom()
	out := r.CheckPostToolUse(context.Background(), "write_file", nil, map[string]any{"ok": false}, toolErr)
	if out != nil {
		t.Errorf("failed tool must pass through un-overridden, got %v", out)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "lint clean") {
		t.Errorf("undelivered context must be logged, got %v", warns)
	}
}

// TestMissingBinaryFailsOpen is portable (no shell needed): a hook pointing
// at a nonexistent binary is an exec failure, which per on_failure:allow
// must warn and let the tool run — never wedge a session over a bad path.
func TestMissingBinaryFailsOpen(t *testing.T) {
	r, err := NewRunner(Config{PreToolUse: []Group{{Hooks: []Handler{
		{Command: []string{"/nonexistent-dir-12345/no-such-hook"}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	var warns []string
	r.SetLog(func(s string) { warns = append(warns, s) })
	if blocked, _ := r.CheckPreToolUse(context.Background(), "t", nil); blocked {
		t.Error("missing hook binary must fail open")
	}
	if len(warns) == 0 {
		t.Error("missing binary must log a warning")
	}
}

// TestUpdatedInputWarns pins the v1 limitation: a hook returning
// updatedInput (arg rewriting, Claude feature) is ignored, but loudly —
// the warn log names it instead of silently dropping a ported hook's
// rewrite.
func TestUpdatedInputWarns(t *testing.T) {
	v := parseExit(30, 0, []byte(`{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"command":"ls"}}}`), nil)
	if v.hookErr != "" || v.block {
		t.Fatalf("allow+updatedInput = %+v, want clean allow with flag", v)
	}
	if !v.ignoredUpdate {
		t.Error("updatedInput must set ignoredUpdate")
	}
	v = parseExit(30, 0, []byte(`{"decision":"block","updated_input":{}}`), nil)
	if !v.block || !v.ignoredUpdate {
		t.Errorf("block+updated_input = %+v, want block with flag", v)
	}
	v = parseExit(30, 0, []byte(`{"hookSpecificOutput":{}}`), nil)
	if v.ignoredUpdate {
		t.Error("no updatedInput must leave the flag clear")
	}
}

// TestUserStyleGuardScript emulates the canonical user hook: a PreToolUse
// guard that reads tool_input from stdin and denies dangerous commands
// while allowing everything else. Written the way a user would write it
// (sh + grep on the raw JSON), not the way the test suite would.
func TestUserStyleGuardScript(t *testing.T) {
	sh := testShell(t)
	guard := `if grep -q 'rm -rf /\|mkfs\|dd .*of=/dev/' ; then echo "refusing destructive command" >&2; exit 2; fi; exit 0`
	r, err := NewRunner(Config{PreToolUse: []Group{{Matcher: "system_exec", Hooks: []Handler{
		{Name: "guard", Command: []string{sh, "-c", guard}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if blocked, _ := r.CheckPreToolUse(ctx, "system_exec", map[string]any{"command": "ls /tmp"}); blocked {
		t.Error("benign command must pass the guard")
	}
	blocked, res := r.CheckPreToolUse(ctx, "system_exec", map[string]any{"command": "rm -rf /"})
	if !blocked {
		t.Fatal("destructive command must be blocked by the guard")
	}
	if s, _ := res["error"].(string); !strings.Contains(s, "refusing destructive") {
		t.Errorf("block reason = %q, want the guard's stderr", s)
	}
	// A different tool family is untouched by the matcher.
	if blocked, _ := r.CheckPreToolUse(ctx, "read_file", map[string]any{"path": "/etc/passwd"}); blocked {
		t.Error("unmatched tool must pass")
	}
}

// TestHookPayloadContent emulates a user hook end to end: the script reads
// the JSON payload from stdin, and the test asserts the contract fields a
// hook author depends on.
func TestHookPayloadContent(t *testing.T) {
	sh := testShell(t)
	dump := filepath.Join(t.TempDir(), "stdin.json")
	r, err := NewRunner(Config{PreToolUse: []Group{{Hooks: []Handler{
		{Command: []string{sh, "-c", "cat > " + dump}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	blocked, _ := r.CheckPreToolUse(context.Background(), "system_exec", map[string]any{"command": "ls"})
	if blocked {
		t.Fatal("dumping hook must allow")
	}
	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("hook did not receive stdin: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("stdin is not JSON: %v\n%s", err, raw)
	}
	if got["hook_event_name"] != "PreToolUse" {
		t.Errorf("hook_event_name = %v", got["hook_event_name"])
	}
	if got["tool_name"] != "system_exec" {
		t.Errorf("tool_name = %v", got["tool_name"])
	}
	input, _ := got["tool_input"].(map[string]any)
	if input["command"] != "ls" {
		t.Errorf("tool_input = %v, want command passthrough", got["tool_input"])
	}
	if cwd, _ := got["cwd"].(string); cwd == "" {
		t.Error("cwd must be non-empty")
	}
	ts, _ := got["timestamp"].(string)
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("timestamp %q is not RFC3339Nano: %v", ts, err)
	}
}

// TestDisabledHandlersNeverExecute pins HK-109: explicit enabled:false
// skips the handler on PreToolUse without spawning it (missing binary
// would fail open loudly if executed — silence proves the skip).
func TestDisabledHandlersNeverExecute(t *testing.T) {
	off := false
	r, err := NewRunner(Config{PreToolUse: []Group{{Hooks: []Handler{
		{Command: []string{"/nonexistent-dir-12345/no-such-hook"}, Enabled: &off},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	var warns []string
	r.SetLog(func(s string) { warns = append(warns, s) })
	if blocked, _ := r.CheckPreToolUse(context.Background(), "t", nil); blocked {
		t.Error("disabled hook must not block")
	}
	for _, w := range warns {
		if strings.Contains(w, "no-such-hook") {
			t.Errorf("disabled hook must not execute (warned: %q)", w)
		}
	}
	if snaps := r.Snapshots(); len(snaps) != 1 || snaps[0].Enabled {
		t.Errorf("disabled hook must still list as disabled: %+v", snaps)
	}
}

// TestEnabledFingerprintUnchanged pins the HK-109 trust property:
// toggling enabled must not change the content fingerprint, so
// disable/enable can never lapse (or smuggle past) trust.
func TestEnabledFingerprintUnchanged(t *testing.T) {
	base := Handler{Command: []string{"/bin/true"}}
	off := false
	on := true
	disabled := Handler{Command: []string{"/bin/true"}, Enabled: &off}
	enabled := Handler{Command: []string{"/bin/true"}, Enabled: &on}
	if fp := disabled.Fingerprint(); fp != base.Fingerprint() {
		t.Errorf("disabled fingerprint %s != base %s", fp, base.Fingerprint())
	}
	if fp := enabled.Fingerprint(); fp != base.Fingerprint() {
		t.Errorf("enabled fingerprint %s != base %s", fp, base.Fingerprint())
	}
	var nilH, offH, onH Handler
	offH.Enabled = &off
	onH.Enabled = &on
	if !nilH.IsEnabled() || offH.IsEnabled() || !onH.IsEnabled() {
		t.Error("IsEnabled must default on, honor explicit false/true")
	}
}

// TestReloadSwapsGroups pins HK-110: Reload replaces the compiled set in
// place (same pointer), the new set fires, and the old set stops.
func TestReloadSwapsGroups(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-spawn tests are unix-only")
	}
	r, err := NewRunner(Config{PreToolUse: []Group{{Hooks: []Handler{
		{Command: []string{"/bin/sh", "-c", "exit 2"}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if blocked, _ := r.CheckPreToolUse(context.Background(), "t", nil); !blocked {
		t.Fatal("old set must block before reload")
	}
	if err := r.Reload(Config{PreToolUse: []Group{{Hooks: []Handler{
		{Command: []string{"/bin/true"}},
	}}}}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if blocked, _ := r.CheckPreToolUse(context.Background(), "t", nil); blocked {
		t.Error("old set must stop firing after reload")
	}
	if !r.Enabled() {
		t.Error("reloaded runner with groups must be enabled")
	}
}

// TestReloadInvalidKeepsOldSet pins the fail-closed edit: a bad config
// fails reload loudly and the previous set keeps serving.
func TestReloadInvalidKeepsOldSet(t *testing.T) {
	r, err := NewRunner(Config{PreToolUse: []Group{{Hooks: []Handler{
		{Command: []string{"/bin/true"}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	bad := Config{PreToolUse: []Group{{Matcher: "[invalid", Hooks: []Handler{
		{Command: []string{"/bin/true"}},
	}}}}
	if err := r.Reload(bad); err == nil {
		t.Fatal("invalid reload must fail")
	}
	if !r.Enabled() || len(r.Snapshots()) != 1 {
		t.Error("old set must stay intact after failed reload")
	}
	if err := r.Reload(Config{}); err != nil {
		t.Fatalf("reload to empty: %v", err)
	}
	if r.Enabled() {
		t.Error("empty reload must disable the runner")
	}
}

// TestReloadConcurrent is the -race pin for HK-110: checks racing a
// reload must never observe a torn set.
func TestReloadConcurrent(t *testing.T) {
	r, err := NewRunner(Config{PreToolUse: []Group{{Hooks: []Handler{
		{Command: []string{"/bin/true"}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				_, _ = r.CheckPreToolUse(context.Background(), "t", nil)
				_ = r.Snapshots()
				_ = r.Enabled()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 10; j++ {
			_ = r.Reload(Config{PostToolUse: []Group{{Hooks: []Handler{
				{Command: []string{"/bin/true"}},
			}}}})
		}
	}()
	wg.Wait()
}

// TestTimeoutKillsBackgroundedGrandchild pins the pipe-holder fix: a hook
// that backgrounds a long sleep and exits must still resolve at the hook
// timeout (plus a small WaitDelay grace), not when the grandchild exits.
// Without group-kill + WaitDelay, Wait blocks on the inherited stdout pipe
// until the grandchild dies.
func TestTimeoutKillsBackgroundedGrandchild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group kill is unix-only")
	}
	start := time.Now()
	_, _, timedOut, _ := runCommand(context.Background(), 2*time.Second,
		[]string{"/bin/sh", "-c", "sleep 30 &"}, nil, "", nil)
	elapsed := time.Since(start)
	if !timedOut {
		t.Error("backgrounded sleep must still trip the hook timeout")
	}
	if elapsed > 15*time.Second {
		t.Errorf("hook run took %v, want bounded by timeout+WaitDelay", elapsed)
	}
}
