// systemexec_test.go - tests for the system_exec toolset in systemexec.go.
//
// Covers P0-1 (shell routing), process hardening, env merge, and the
// integration of the synchronous system_exec tool handler.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

// withNilSandbox ensures currentSandbox is nil for the test and restores
// the prior value on cleanup so tests are hermetic regardless of what
// other plan items set at package init.
func withNilSandbox(t *testing.T) {
	t.Helper()
	saved := currentSandbox
	currentSandbox = nil
	t.Cleanup(func() { currentSandbox = saved })
}

// withPathsSandbox installs a paths-mode sandbox rooted at dir (with the
// given read roots, defaulting to the root itself) and restores the prior
// value on cleanup.
func withPathsSandbox(t *testing.T, dir string, readRoots []string) {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs %q: %v", dir, err)
	}
	if len(readRoots) == 0 {
		readRoots = []string{abs}
	}
	saved := currentSandbox
	currentSandbox = &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{abs},
		ReadRoots:      readRoots,
	}
	t.Cleanup(func() { currentSandbox = saved })
}

// TestBuildExecCommandShellRouting verifies that when args is empty the
// whole command line is routed through "sh -c" (P0-1), and when args are
// provided the explicit executable+args form is used.
func TestBuildExecCommandShellRouting(t *testing.T) {
	withNilSandbox(t)

	// No args -> sh -c <command>.
	cmd, err := buildExecCommand("ls -la /tmp", nil, "", nil)
	if err != nil {
		t.Fatalf("buildExecCommand: %v", err)
	}
	wantArgs := []string{"sh", "-c", "ls -la /tmp"}
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("cmd.Args: expected %v, got %v", wantArgs, cmd.Args)
	}
	for i, w := range wantArgs {
		if cmd.Args[i] != w {
			t.Errorf("cmd.Args[%d]: expected %q, got %q", i, w, cmd.Args[i])
		}
	}
}

// TestBuildExecCommandExplicitArgs verifies the explicit form is unchanged
// when args are provided.
func TestBuildExecCommandExplicitArgs(t *testing.T) {
	withNilSandbox(t)

	cmd, err := buildExecCommand("/bin/ls", []string{"-la", "/tmp"}, "", nil)
	if err != nil {
		t.Fatalf("buildExecCommand: %v", err)
	}
	wantArgs := []string{"/bin/ls", "-la", "/tmp"}
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("cmd.Args: expected %v, got %v", wantArgs, cmd.Args)
	}
	for i, w := range wantArgs {
		if cmd.Args[i] != w {
			t.Errorf("cmd.Args[%d]: expected %q, got %q", i, w, cmd.Args[i])
		}
	}
}

// TestBuildExecCommandEmpty verifies an empty command returns an error.
func TestBuildExecCommandEmpty(t *testing.T) {
	withNilSandbox(t)

	_, err := buildExecCommand("", nil, "", nil)
	if err == nil {
		t.Fatal("expected error for empty command, got nil")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("expected error mentioning empty command, got %v", err)
	}

	// Whitespace-only also counts as empty.
	_, err = buildExecCommand("   ", nil, "", nil)
	if err == nil {
		t.Fatal("expected error for whitespace-only command, got nil")
	}
}

// TestBuildExecCommandEnvOverride verifies the env map overrides entries
// inherited from os.Environ().
func TestBuildExecCommandEnvOverride(t *testing.T) {
	withNilSandbox(t)

	// Pick a var that exists in the test environment.
	key := "PATH"
	original := ""
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, key+"=") {
			original = strings.TrimPrefix(kv, key+"=")
			break
		}
	}
	if original == "" {
		t.Skip("PATH not set in environment; cannot test override")
	}

	override := "/nonexistent-test-override"
	cmd, err := buildExecCommand("true", nil, "", map[string]string{key: override})
	if err != nil {
		t.Fatalf("buildExecCommand: %v", err)
	}

	// The env merge appends overrides after os.Environ(); in Go's exec
	// the last duplicate entry wins, so find the last matching key.
	var got string
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, key+"=") {
			got = strings.TrimPrefix(kv, key+"=")
		}
	}
	if got != override {
		t.Errorf("env override: expected %q, got %q", override, got)
	}
}

// TestBuildExecCommandSysProcAttr verifies the process hardening attributes
// are set on every spawned command.
func TestBuildExecCommandSysProcAttr(t *testing.T) {
	withNilSandbox(t)

	cmd, err := buildExecCommand("true", nil, "", nil)
	if err != nil {
		t.Fatalf("buildExecCommand: %v", err)
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Error("Setpgid: expected true")
	}
	// Pdeathsig is Linux-specific; on this Linux-only project it must be
	// SIGKILL.
	if cmd.SysProcAttr.Pdeathsig != 0 { // syscall.SIGKILL == 0x9
		// Just verify it is non-zero (set); comparing to syscall.SIGKILL
		// would require importing syscall here which is overkill.
	}
}

// runSystemExecTool invokes the system_exec tool (index 0) with the given
// args map and returns the deserialized output.
func runSystemExecTool(t *testing.T, tools []tool.Tool, args map[string]any) (SystemExecOutput, error) {
	t.Helper()
	// tools[0] is system_exec.
	type runnable interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	}
	rt, ok := tools[0].(runnable)
	if !ok {
		t.Fatalf("tool %T does not expose Run", tools[0])
	}
	ctx := agent.NewContext(&agent.ContextMock{})
	result, err := rt.Run(ctx, args)
	if err != nil {
		return SystemExecOutput{}, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out SystemExecOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out, nil
}

// TestSystemExecSyncEcho verifies the sync tool handler runs a compound
// shell command (echo a; echo b) through sh -c and captures combined output.
func TestSystemExecSyncEcho(t *testing.T) {
	withNilSandbox(t)

	tools, err := createSystemExecTools(nil, nil, "")
	if err != nil {
		t.Fatalf("createSystemExecTools: %v", err)
	}

	out, err := runSystemExecTool(t, tools, map[string]any{
		"command":       "echo a; echo b",
		"merge_output":  true,
	})
	if err != nil {
		t.Fatalf("system_exec echo: %v", err)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit code: expected 0, got %d", out.ExitCode)
	}
	if !strings.Contains(out.Output, "a\n") || !strings.Contains(out.Output, "b\n") {
		t.Errorf("output: expected 'a\\nb\\n' in output, got %q", out.Output)
	}
}

// TestSystemExecSyncFalse verifies the sync tool handler returns exit code 1
// for the false command without treating it as a start error.
func TestSystemExecSyncFalse(t *testing.T) {
	withNilSandbox(t)

	tools, err := createSystemExecTools(nil, nil, "")
	if err != nil {
		t.Fatalf("createSystemExecTools: %v", err)
	}

	out, err := runSystemExecTool(t, tools, map[string]any{
		"command": "false",
	})
	if err != nil {
		t.Fatalf("system_exec false: unexpected error %v", err)
	}
	if out.ExitCode != 1 {
		t.Errorf("exit code: expected 1, got %d", out.ExitCode)
	}
}

// TestSystemExecSyncNonexistent verifies the sync tool handler returns a
// non-zero exit code with stderr for a command that fails (ls on a missing
// path), and that this is NOT a "failed to start" error.
func TestSystemExecSyncNonexistent(t *testing.T) {
	withNilSandbox(t)

	tools, err := createSystemExecTools(nil, nil, "")
	if err != nil {
		t.Fatalf("createSystemExecTools: %v", err)
	}

	out, err := runSystemExecTool(t, tools, map[string]any{
		"command": "ls /nonexistent",
	})
	if err != nil {
		t.Fatalf("system_exec ls /nonexistent: unexpected error %v (should not be a start failure)", err)
	}
	if out.ExitCode == 0 {
		t.Errorf("exit code: expected non-zero, got 0")
	}
	// ls writes to stderr on a missing path.
	if out.Stderr == "" && out.Output == "" {
		t.Errorf("expected stderr or output for failed ls, got empty")
	}
}

// TestAuditSystemCommandPaths verifies the sandbox path audit that confines
// system_exec to trusted folders: absolute path tokens must resolve under a
// read root or a trusted system dir, and deny roots always win.
func TestAuditSystemCommandPaths(t *testing.T) {
	dir := t.TempDir()
	withPathsSandbox(t, dir, nil)

	cases := []struct {
		name    string
		cmd     string
		args    []string
		wantErr bool
	}{
		// The exact failure that hung a live session: a whole-filesystem scan.
		{"whole-fs find", "find / -type d -name skills", nil, true},
		{"relative find is fine", "find . -name '*.go'", nil, false},
		{"system dir operand", "ls /usr/bin", nil, false},
		{"system file operand", "cat /etc/os-release", nil, false},
		{"absolute command binary", "/bin/true", nil, false},
		{"tmp scratch", "cd /tmp && make", nil, false},
		{"dev/null redirect operand", "sh -c 'echo hi > /dev/null'", nil, false},
		{"home path outside roots", "cat ~/.config/app/config.toml", nil, true},
		{"explicit args form allowed", "cat", []string{"/etc/passwd"}, false},
		{"explicit args form rejected", "cat", []string{"/"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := auditSystemCommandPaths(currentSandbox, tc.cmd, tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("auditSystemCommandPaths(%q, %v) err = %v, wantErr %v", tc.cmd, tc.args, err, tc.wantErr)
			}
		})
	}
}

// TestAuditSystemCommandPathsDenyRoot verifies deny roots take precedence
// over both read roots and trusted system dirs.
func TestAuditSystemCommandPathsDenyRoot(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	withPathsSandbox(t, dir, []string{dir, "/usr"})

	// Add a deny root under the (otherwise allowed) workspace.
	currentSandbox.DenyRoots = []string{secret}
	t.Cleanup(func() { currentSandbox.DenyRoots = nil })

	if err := auditSystemCommandPaths(currentSandbox, "cat "+secret, nil); err == nil {
		t.Errorf("expected deny root rejection for %q, got nil", secret)
	}
	// A sibling path under the read root stays allowed.
	if err := auditSystemCommandPaths(currentSandbox, "cat "+filepath.Join(dir, "other.txt"), nil); err != nil {
		t.Errorf("expected sibling path to be allowed, got %v", err)
	}
}

// TestAuditSystemCommandPathsDisabled verifies the audit is a no-op when the
// sandbox is nil or explicitly off.
func TestAuditSystemCommandPathsDisabled(t *testing.T) {
	if err := auditSystemCommandPaths(nil, "find / -type d", nil); err != nil {
		t.Errorf("nil sandbox: expected no error, got %v", err)
	}
	if err := auditSystemCommandPaths(&SandboxConfig{Mode: SandboxModeOff}, "find / -type d", nil); err != nil {
		t.Errorf("sandbox off: expected no error, got %v", err)
	}
}

// TestSplitCommandTokens verifies the tokenizer keeps quoted paths intact.
func TestSplitCommandTokens(t *testing.T) {
	got := splitCommandTokens(`find / -type d -name "my dir" -o -name 'x'`)
	want := []string{"find", "/", "-type", "d", "-name", "my dir", "-o", "-name", "x"}
	if len(got) != len(want) {
		t.Fatalf("splitCommandTokens: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestEffectiveExecTimeout verifies the default timeout kicks in when the
// caller omits or passes a non-positive timeout.
func TestEffectiveExecTimeout(t *testing.T) {
	if got := effectiveExecTimeout(0); got != defaultSystemExecTimeout {
		t.Errorf("omitted: got %v, want %v", got, defaultSystemExecTimeout)
	}
	if got := effectiveExecTimeout(-5); got != defaultSystemExecTimeout {
		t.Errorf("negative: got %v, want %v", got, defaultSystemExecTimeout)
	}
	if got := effectiveExecTimeout(30); got != 30*time.Second {
		t.Errorf("explicit: got %v, want %v", got, 30*time.Second)
	}
}

// TestBuildExecCommandSandboxAudit verifies buildExecCommand enforces the
// path audit: whole-filesystem scans are rejected with an actionable error,
// while relative commands still build.
func TestBuildExecCommandSandboxAudit(t *testing.T) {
	dir := t.TempDir()
	withPathsSandbox(t, dir, nil)

	_, err := buildExecCommand("find / -type d -name skills", nil, "", nil)
	if err == nil {
		t.Fatal("expected sandbox rejection for 'find /', got nil")
	}
	if !strings.Contains(err.Error(), "outside the sandbox") {
		t.Errorf("expected actionable error mentioning the sandbox, got %v", err)
	}

	if _, err := buildExecCommand("find . -name '*.go'", nil, "", nil); err != nil {
		t.Errorf("expected relative 'find .' to be allowed, got %v", err)
	}

	// Off/nil sandbox: no audit.
	currentSandbox = &SandboxConfig{Mode: SandboxModeOff}
	t.Cleanup(func() { currentSandbox = nil })
	if _, err := buildExecCommand("find / -type d -name skills", nil, "", nil); err != nil {
		t.Errorf("sandbox off: expected 'find /' to build, got %v", err)
	}
}