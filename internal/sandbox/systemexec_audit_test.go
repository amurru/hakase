package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAuditSystemCommandPathsRelativeOperands verifies that relative
// path-like operands resolve against the effective working directory (the
// workspace root unless an approved override is supplied) and are audited
// against deny roots and denied sensitive basenames - in both the sh -c
// form and the explicit argv form. Regression guard: auditPathToken used to
// skip every relative token, so "cat services/api/.env" could read a denied
// dotenv nested inside the workspace.
func TestAuditSystemCommandPathsRelativeOperands(t *testing.T) {
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{"/home/user/project"},
		ReadRoots:      []string{"/home/user/project"},
		DenyRoots:      []string{"/home/user/project/secrets"},
		DenyBasenames:  []string{".env"},
	}

	cases := []struct {
		name    string
		command string
		args    []string
		wantErr string // substring; "" = allowed
	}{
		{
			name:    "shell form nested dotenv rejected",
			command: "cat services/api/.env",
			wantErr: "denied sensitive file",
		},
		{
			name:    "explicit argv form nested dotenv rejected",
			command: "cat",
			args:    []string{"services/api/.env"},
			wantErr: "denied sensitive file",
		},
		{
			name:    "bare dotenv basename rejected",
			command: "cat .env",
			wantErr: "denied sensitive file",
		},
		{
			name:    "deny root subdir rejected",
			command: "cat secrets/key",
			wantErr: "denied sandbox root",
		},
		{
			name:    "parent escape into deny root rejected",
			command: "cat ../project/secrets/key",
			wantErr: "denied sandbox root",
		},
		{
			name:    "benign glob operands pass",
			command: "go build ./...",
			wantErr: "",
		},
		{
			name:    "flags and plain words pass",
			command: "ls -la src",
			wantErr: "",
		},
		{
			name:    "urls are not audited as paths",
			command: "curl https://example.com/a.env",
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AuditSystemCommandPaths(sb, tc.command, tc.args, "")
			switch {
			case tc.wantErr == "":
				if err != nil {
					t.Fatalf("expected command to pass, got %v", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
			}
		})
	}
}

// TestAuditSystemCommandPathsExplicitWorkingDir pins that an approved
// working_dir override becomes the resolution base for relative operands:
// the same command is denied from one base and allowed from another.
func TestAuditSystemCommandPathsExplicitWorkingDir(t *testing.T) {
	tmp := t.TempDir()
	workspace := mustMkdir(t, filepath.Join(tmp, "workspace"))
	baseA := mustMkdir(t, filepath.Join(workspace, "a"))
	baseB := mustMkdir(t, filepath.Join(workspace, "b"))
	denied := mustMkdir(t, filepath.Join(baseA, "denied"))

	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: normalizeRoots([]string{workspace}),
		ReadRoots:      normalizeRoots([]string{workspace}),
	}
	sb.DenyRoots = normalizeRoots([]string{denied})

	// From baseA the operand lands inside the denied directory.
	if err := AuditSystemCommandPaths(sb, "cat denied/key", nil, baseA); err == nil || !strings.Contains(err.Error(), "denied sandbox root") {
		t.Fatalf("expected operand under denied working_dir base to be rejected, got %v", err)
	}
	// The identical command from the sibling base stays allowed.
	if err := AuditSystemCommandPaths(sb, "cat denied/key", nil, baseB); err != nil {
		t.Fatalf("same operand from sibling base should pass, got %v", err)
	}
	// A deny root itself can no longer serve as working_dir: the override is
	// resolved through ResolveScopedPath and fails closed.
	if err := AuditSystemCommandPaths(sb, "cat key", nil, denied); err == nil || !strings.Contains(err.Error(), "rejected by sandbox") {
		t.Fatalf("expected denied working_dir to fail closed, got %v", err)
	}

	// Basename denies are path-based like every sandbox deny: ".env" is
	// rejected from any base whether or not that exact file exists.
	sb.DenyBasenames = []string{".env"}
	if err := AuditSystemCommandPaths(sb, "cat .env", nil, ""); err == nil || !strings.Contains(err.Error(), "denied sensitive file") {
		t.Fatalf("expected path-based .env denial, got %v", err)
	}
}

// TestAuditSystemCommandPathsRelativeSymlinkIntoDenyRoot verifies that a
// workspace symlink reached through a relative operand cannot smuggle a read
// of a denied directory.
func TestAuditSystemCommandPathsRelativeSymlinkIntoDenyRoot(t *testing.T) {
	tmp := t.TempDir()
	workspace := mustMkdir(t, filepath.Join(tmp, "workspace"))
	secrets := mustMkdir(t, filepath.Join(workspace, "secrets"))
	mustWriteFile(t, filepath.Join(secrets, "key"), "sk-secret")
	mustSymlink(t, filepath.Join(secrets, "key"), filepath.Join(workspace, "leak.json"))

	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: normalizeRoots([]string{workspace}),
		ReadRoots:      normalizeRoots([]string{workspace}),
	}
	sb.DenyRoots = normalizeRoots([]string{secrets})

	// "./leak.json" carries a separator so it is audited; symlink resolution
	// lands inside the denied directory.
	if err := AuditSystemCommandPaths(sb, "cat ./leak.json", nil, ""); err == nil || !strings.Contains(err.Error(), "denied sandbox root") {
		t.Fatalf("expected relative symlink into deny root to be rejected, got %v", err)
	}
}

// TestAuditSystemCommandPathsRelativeWorkingDirResolved is a regression test:
// the workingDir override is resolved through ResolveScopedPath before it
// becomes the audit base. A raw relative override ("sub") used to leave the
// audit comparing absolute deny roots against relative candidates, so
// "cat secrets/key" passed while the process ran in workspace/sub.
func TestAuditSystemCommandPathsRelativeWorkingDirResolved(t *testing.T) {
	tmp := t.TempDir()
	workspace := mustMkdir(t, filepath.Join(tmp, "workspace"))
	secrets := mustMkdir(t, filepath.Join(workspace, "sub", "secrets"))
	mustWriteFile(t, filepath.Join(secrets, "key"), "sk-secret")

	t.Chdir(workspace)

	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: normalizeRoots([]string{workspace}),
		ReadRoots:      normalizeRoots([]string{workspace}),
	}
	sb.DenyRoots = normalizeRoots([]string{secrets})

	// workingDir "sub" resolves to workspace/sub; the operand then lands in
	// the denied directory and must be rejected.
	if err := AuditSystemCommandPaths(sb, "cat secrets/key", nil, "sub"); err == nil || !strings.Contains(err.Error(), "denied sandbox root") {
		t.Fatalf("expected denied read via relative workingDir to be rejected, got %v", err)
	}
	// A rejected override fails closed instead of auditing against garbage.
	if _, err := sb.ResolveScopedPath("../outside", false); err == nil {
		t.Skip("test premise requires ../outside to be outside approved roots")
	}
	if err := AuditSystemCommandPaths(sb, "cat x", nil, "../outside"); err == nil || !strings.Contains(err.Error(), "rejected by sandbox") {
		t.Fatalf("expected unresolvable working_dir to fail closed, got %v", err)
	}
}

// TestAuditSystemCommandPathsGlobExpansion verifies that shell glob
// metacharacters cannot mask denied targets: patterns are pre-expanded and
// every concrete match is audited against deny rules, for both relative and
// absolute operands.
func TestAuditSystemCommandPathsGlobExpansion(t *testing.T) {
	tmp := t.TempDir()
	workspace := mustMkdir(t, filepath.Join(tmp, "workspace"))
	deniedA := mustMkdir(t, filepath.Join(workspace, "a", "secrets"))
	allowedB := mustMkdir(t, filepath.Join(workspace, "b", "secrets"))
	mustWriteFile(t, filepath.Join(deniedA, "key"), "sk-a")
	mustWriteFile(t, filepath.Join(allowedB, "key"), "sk-b")
	mustWriteFile(t, filepath.Join(workspace, "main.go"), "package main")
	mustMkdir(t, filepath.Join(workspace, "sub", "secrets"))
	mustWriteFile(t, filepath.Join(workspace, "sub", "secrets", ".env"), "TOKEN=x")

	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: normalizeRoots([]string{workspace}),
		ReadRoots:      normalizeRoots([]string{workspace}),
		DenyBasenames:  []string{".env"},
	}
	sb.DenyRoots = normalizeRoots([]string{deniedA})

	cases := []struct {
		name    string
		command string
		args    []string
		wantErr string // substring; "" = allowed
	}{
		{
			name:    "relative glob reaching into deny root rejected",
			command: "cat */secrets/key",
			wantErr: "denied sandbox root",
		},
		{
			name:    "absolute glob reaching into deny root rejected",
			command: "cat " + workspace + "/*/secrets/key",
			wantErr: "denied sandbox root",
		},
		{
			name:    "suffix glob on dotenv basename rejected",
			command: "cat sub/secrets/*.env",
			args:    nil,
			wantErr: "denied sensitive file",
		},
		{
			name:    "benign code glob passes",
			command: "ls *.go",
			wantErr: "",
		},
		{
			name:    "glob matching only allowed paths passes",
			command: "cat b/secrets/*",
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AuditSystemCommandPaths(sb, tc.command, tc.args, "")
			switch {
			case tc.wantErr == "":
				if err != nil {
					t.Fatalf("expected command to pass, got %v", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
			}
		})
	}
}

// TestAuditSystemCommandPathsRelativeEscapeRejected verifies that relative
// operands resolving outside every read root are rejected even when they
// dodge the deny rules: the relative branch enforces read-root confinement
// on literal, symlink-resolved, and glob-expanded candidates. Regression
// guard for "../other-workspace/private-key" style escapes.
func TestAuditSystemCommandPathsRelativeEscapeRejected(t *testing.T) {
	tmp := t.TempDir()
	workspace := mustMkdir(t, filepath.Join(tmp, "workspace"))
	outside := mustMkdir(t, filepath.Join(tmp, "other-workspace"))
	mustWriteFile(t, filepath.Join(outside, "private-key"), "sk")
	mustWriteFile(t, filepath.Join(outside, "a.pem"), "pem")

	// t.TempDir() lives under /tmp, which trustedExecDirs allows by default;
	// drop the trusted dirs for this test so the /tmp trust cannot mask the
	// escape being exercised.
	origTrusted := trustedExecDirs
	trustedExecDirs = nil
	t.Cleanup(func() { trustedExecDirs = origTrusted })

	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: normalizeRoots([]string{workspace}),
		ReadRoots:      normalizeRoots([]string{workspace}),
	}

	cases := []struct {
		name    string
		command string
		args    []string
	}{
		{"shell form escape", "cat ../other-workspace/private-key", nil},
		{"explicit argv form escape", "cat", []string{"../other-workspace/private-key"}},
		{"glob escape", "cat ../other-workspace/*.pem", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AuditSystemCommandPaths(sb, tc.command, tc.args, "")
			if err == nil || !strings.Contains(err.Error(), "outside the sandbox") {
				t.Fatalf("expected outside-sandbox rejection, got %v", err)
			}
		})
	}

	// The same shape of operand inside the workspace stays allowed.
	mustWriteFile(t, filepath.Join(workspace, "notes.txt"), "hi")
	if err := AuditSystemCommandPaths(sb, "cat notes.txt", nil, ""); err != nil {
		t.Fatalf("in-workspace read should pass, got %v", err)
	}
}

// TestAuditSystemCommandPathsGlobOverflowRejected verifies that a glob
// expanding beyond maxGlobMatches is rejected outright instead of being
// audited only up to the cap - sh expands every match, so truncation would
// leave denied or unconfined paths past the limit unaudited.
func TestAuditSystemCommandPathsGlobOverflowRejected(t *testing.T) {
	workspace := t.TempDir()
	overflow := mustMkdir(t, filepath.Join(workspace, "overflow"))
	for i := 0; i <= maxGlobMatches; i++ {
		mustWriteFile(t, filepath.Join(overflow, fmt.Sprintf("f%04d.txt", i)), "x")
	}

	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: normalizeRoots([]string{workspace}),
		ReadRoots:      normalizeRoots([]string{workspace}),
	}

	// 1001 matches: even though every match sits inside the workspace, the
	// audit refuses patterns it cannot fully verify.
	if err := AuditSystemCommandPaths(sb, "cat overflow/*.txt", nil, ""); err == nil || !strings.Contains(err.Error(), "exceeding") {
		t.Fatalf("expected glob overflow rejection, got %v", err)
	}
	// A narrower pattern within the cap stays allowed.
	if err := AuditSystemCommandPaths(sb, "cat overflow/f000*.txt", nil, ""); err != nil {
		t.Fatalf("in-cap glob should pass, got %v", err)
	}
}

// TestGitWorkdirConfigAskReasonLinkedWorktree builds the faithful
// linked-worktree layout git worktree add creates: the worktree .git file
// points at <main>/.git/worktrees/<name>, which holds a commondir file
// (usually "../..") instead of its own config. The audit must follow
// commondir to the shared config; the old Join(gitdir,"config") read a
// file that never exists and stayed silent on an evil main config.
func TestGitWorkdirConfigAskReasonLinkedWorktree(t *testing.T) {
	tmp := t.TempDir()
	main := mustMkdir(t, filepath.Join(tmp, "main"))
	mainGit := mustMkdir(t, filepath.Join(main, ".git"))
	mustWriteFile(t, filepath.Join(mainGit, "config"), "[core]\n\tpager = evil-pager\n")
	wtGitDir := mustMkdir(t, filepath.Join(mainGit, "worktrees", "wt1"))
	mustWriteFile(t, filepath.Join(wtGitDir, "commondir"), "../..\n")
	mustWriteFile(t, filepath.Join(wtGitDir, "gitdir"), filepath.Join(tmp, "wt1", ".git")+"\n")

	wt := mustMkdir(t, filepath.Join(tmp, "wt1"))
	mustWriteFile(t, filepath.Join(wt, ".git"), "gitdir: "+wtGitDir+"\n")

	if reason := gitWorkdirConfigAskReason(wt); !strings.Contains(reason, "requires user approval") {
		t.Fatalf("linked worktree with evil main config should ask, got %q", reason)
	}

	// Clean shared config stays green through the same pointer.
	mustWriteFile(t, filepath.Join(mainGit, "config"), "[core]\n\trepositoryformatversion = 0\n")
	if reason := gitWorkdirConfigAskReason(wt); reason != "" {
		t.Fatalf("linked worktree with clean main config must stay silent, got %q", reason)
	}

	// Missing commondir falls back to stripping /worktrees/<name>.
	mustWriteFile(t, filepath.Join(mainGit, "config"), "[core]\n\tpager = evil-pager\n")
	if err := os.Remove(filepath.Join(wtGitDir, "commondir")); err != nil {
		t.Fatal(err)
	}
	if reason := gitWorkdirConfigAskReason(wt); !strings.Contains(reason, "requires user approval") {
		t.Fatalf("linked worktree without commondir should ask via fallback, got %q", reason)
	}
}

// TestGitConfigTextHasExecHookExtended pins the on-disk exec-hook set:
// filter/diff drivers, include paths, ! credential helpers, gpg.program,
// and core.gitProxy all ask, while benign lookalikes stay green.
func TestGitConfigTextHasExecHookExtended(t *testing.T) {
	asks := []struct {
		name string
		text string
	}{
		{"filter clean", "[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n"},
		{"filter smudge", "[filter \"lfs\"]\n\tsmudge = git-lfs smudge -- %f\n"},
		{"filter process", "[filter \"lfs\"]\n\tprocess = git-lfs filter-process\n"},
		{"diff command", "[diff \"x\"]\n\tcommand = mydiff\n"},
		{"diff textconv", "[diff \"x\"]\n\ttextconv = cat\n"},
		{"include path", "[include]\n\tpath = /tmp/evil.inc\n"},
		{"includeIf path", "[includeIf \"gitdir:/tmp/\"]\n\tpath = /tmp/evil.inc\n"},
		{"credential bang helper", "[credential]\n\thelper = !evil-helper\n"},
		{"credential quoted bang helper", "[credential]\n\thelper = \"!evil-helper\"\n"},
		{"gpg program", "[gpg]\n\tprogram = evil-gpg\n"},
		{"core gitproxy", "[core]\n\tgitProxy = evil-proxy\n"},
		{"merge driver", "[merge \"x\"]\n\tdriver = evil-merge %O %A %B\n"},
		{"bare merge driver", "[merge]\n\tdriver = evil-merge\n"},
		{"legacy dotted merge header", "[merge.foo]\n\tdriver = evil-merge\n"},
		{"submodule bang update", "[submodule \"foo\"]\n\tupdate = !evil-update\n"},
		{"submodule quoted bang update", "[submodule \"foo\"]\n\tupdate = \"!evil-update\"\n"},
		{"legacy dotted submodule header", "[submodule.foo]\n\tupdate = !evil-update\n"},
		{"alias bang", "[alias]\n\tco = !evil-co\n"},
		{"alias double-quoted bang", "[alias]\n\tco = \"!evil-co\"\n"},
		{"alias single-quoted bang", "[alias]\n\tco = '!evil-co'\n"},
		{"pager section", "[pager]\n\tlog = diff-highlight | less\n"},
		{"uploadpack objects hook", "[uploadpack]\n\tpackObjectsHook = evil-hook\n"},
		{"remote uploadpack", "[remote \"origin\"]\n\tuploadpack = evil-upload-pack\n"},
		{"remote receivepack", "[remote \"origin\"]\n\treceivepack = evil-receive-pack\n"},
		{"remote vcs", "[remote \"origin\"]\n\tvcs = evil-vcs\n"},
		{"gpg ssh defaultKeyCommand", "[gpg \"ssh\"]\n\tdefaultKeyCommand = evil-key-cmd\n"},
		{"credential absolute-path helper", "[credential]\n\thelper = /usr/bin/evil-helper\n"},
	}
	for _, tc := range asks {
		t.Run(tc.name, func(t *testing.T) {
			if !gitConfigTextHasExecHook(tc.text) {
				t.Errorf("expected exec hook for %q", tc.text)
			}
		})
	}

	green := []struct {
		name string
		text string
	}{
		{"plain repo", "[core]\n\trepositoryformatversion = 0\n"},
		{"alias plain", "[alias]\n\tco = checkout\n"},
		{"credential store helper", "[credential]\n\thelper = store\n"},
		{"credential osxkeychain", "[credential]\n\thelper = osxkeychain\n"},
		{"unrelated path key", "[core]\n\tuntrackedcache = true\n"},
		{"submodule checkout update", "[submodule \"foo\"]\n\tupdate = checkout\n"},
		{"non-merge driver key", "[core]\n\tdriver = foo\n"},
		{"pack memory key", "[pack]\n\twindowMemory = 10m\n"},
		{"pack objects hook ignored", "[pack]\n\tpackObjectsHook = evil-hook\n"},
		{"non-remote uploadpack key", "[core]\n\tuploadpack = foo\n"},
		{"non-gpg defaultKeyCommand key", "[core]\n\tdefaultKeyCommand = foo\n"},
	}
	for _, tc := range green {
		t.Run(tc.name, func(t *testing.T) {
			if gitConfigTextHasExecHook(tc.text) {
				t.Errorf("expected green for %q", tc.text)
			}
		})
	}
}

// TestGitWorkdirConfigOversizeAsks pins the fail-closed audit cap: a
// config past 64KiB hiding an exec hook at the tail (where truncation
// used to cut the scan off) must ask, and an oversize clean config asks
// too - the audit cannot clear hooks it cannot read.
func TestGitWorkdirConfigOversizeAsks(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("[core]\n\trepositoryformatversion = 0\n")
	// Pad past the 64KiB audit cap with comments, then hide an exec
	// hook at the tail where the old truncation cut the scan off.
	for b.Len() < 64*1024+1024 {
		b.WriteString("# padding to push the hook past the audit cap\n")
	}
	b.WriteString("[core]\n\tpager = evil-pager\n")
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if reason := gitWorkdirConfigAskReason(repo); !strings.Contains(reason, "requires user approval") {
		t.Fatalf("oversize config with tail hook should ask, got %q", reason)
	}

	// Oversize but hook-free still asks: too large to audit.
	var c strings.Builder
	c.WriteString("[core]\n\trepositoryformatversion = 0\n")
	for c.Len() < 64*1024+16 {
		c.WriteString("# benign padding past the audit cap\n")
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(c.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if reason := gitWorkdirConfigAskReason(repo); !strings.Contains(reason, "requires user approval") {
		t.Fatalf("oversize clean config should ask (too large to audit), got %q", reason)
	}
}

// TestGitConfigStockLfsAsks pins the lfs scope decision: a stock
// git-lfs filter block asks like any other filter driver - there is
// deliberately no trusted-filter-binary allowlist, because the filter
// path still executes a program on checkout/smudge. Approval clears a
// benign stock-lfs repo after inspection.
func TestGitConfigStockLfsAsks(t *testing.T) {
	stock := "[filter \"lfs\"]\n" +
		"\tclean = git-lfs clean -- %f\n" +
		"\tsmudge = git-lfs smudge -- %f\n" +
		"\tprocess = git-lfs filter-process\n" +
		"\trequired = true\n"
	if !gitConfigTextHasExecHook(stock) {
		t.Errorf("stock git-lfs filter block should ask, stayed green")
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(repo, ".git", "config"), "[core]\n\trepositoryformatversion = 0\n"+stock)
	if reason := gitWorkdirConfigAskReason(repo); !strings.Contains(reason, "requires user approval") {
		t.Fatalf("stock-lfs repo config should ask, got %q", reason)
	}
}

// TestGitPreGateEnvTrampoline pins the F1 env unwrap: env-trampolined
// git (bare env, /usr/bin/env path form, -S splices with assignments
// preserved, and backslash-escaped \git) receives the same pre-gate
// enforcement as plain git. Assignments are evaluated as the env overlay
// before the strip: an exec-hook assignment denies, any other GIT_*
// assignment asks, and a clean trampoline stays silent exactly like
// plain git status.
func TestGitPreGateEnvTrampoline(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	replace := map[string]string{"PATH": "/usr/bin:/bin"}

	silent := []struct {
		name    string
		command string
		args    []string
	}{
		{"shell env git status", `env git status`, nil},
		{"shell absolute env git status", `/usr/bin/env git status`, nil},
		{"shell escaped git status", `\git status`, nil},
		{"shell env PATH assignment", `env PATH=/usr/bin:/bin git status`, nil},
		{"shell env clean -S payload", `env -S 'PATH=/bin git status'`, nil},
		{"explicit env git status", "env", []string{"git", "status"}},
		{"explicit env PATH assignment", "env", []string{"PATH=/usr/bin:/bin", "git", "status"}},
	}
	for _, tc := range silent {
		t.Run(tc.name, func(t *testing.T) {
			seen = nil
			wd := t.TempDir()
			if _, err := buildExecCommand(context.Background(), nil, "sess", tc.command, tc.args, wd, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
				t.Errorf("clean trampoline %q should build silently, got %v", tc.command, err)
			}
			if len(seen) != 0 {
				t.Errorf("clean trampoline %q must not prompt, got %v", tc.command, seen)
			}
		})
	}

	deny := []struct {
		name    string
		command string
		args    []string
	}{
		{"shell env overlay hook", `env GIT_PAGER=x git status`, nil},
		{"shell env -S hook splice", `env -S 'GIT_EXTERNAL_DIFF=id git status'`, nil},
		{"shell env -S pager splice", `env -S 'GIT_PAGER=x git status'`, nil},
		{"shell env -S config triple", `env -S 'GIT_CONFIG_PARAMETERS=core.pager=id git status'`, nil},
		{"shell absolute env hook", `/usr/bin/env GIT_PAGER=x git status`, nil},
		{"shell sudo env -S hook", `sudo env -S 'GIT_PAGER=x git status'`, nil},
		{"explicit env overlay hook", "env", []string{"GIT_EXTERNAL_DIFF=x", "git", "status"}},
		{"explicit env -S hook", "env", []string{"-S", "GIT_PAGER=x git status"}},
	}
	for _, tc := range deny {
		t.Run(tc.name, func(t *testing.T) {
			seen = nil
			wd := t.TempDir()
			if _, err := buildExecCommand(context.Background(), nil, "sess", tc.command, tc.args, wd, map[string]string{}, ExecReplaceEnv(replace)); err == nil || !strings.Contains(err.Error(), "denied by protection policy") {
				t.Errorf("trampoline %q: want protection-policy denial, got %v", tc.command, err)
			}
			if len(seen) != 0 {
				t.Errorf("deny path must not prompt for approval, got %d prompts", len(seen))
			}
		})
	}

	// A non-hook -S assignment asks like the plain overlay form, and
	// approval carries the build through.
	seen = nil
	wd := t.TempDir()
	if _, err := buildExecCommand(context.Background(), nil, "sess", `env -S 'GIT_TRACE=1 git status'`, nil, wd, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
		t.Errorf("approved -S GIT_TRACE trampoline should build, got %v", err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "GIT_TRACE") {
		t.Errorf("want exactly 1 GIT_TRACE approval prompt, got %v", seen)
	}
}

// TestGitPreGateEnvSplitStringChdir pins the env -C/--chdir parity: a
// -C/--chdir smuggled inside -S/--split-string steers the utility's
// working directory exactly like a literal -C, so the pre-gate audits
// the steered repository (Ask naming the hook) instead of the base
// directory. A smuggled -C on a non-git-led build still asks via the
// directory-override leg, like a literal `env -C /hook ls`.
func TestGitPreGateEnvSplitStringChdir(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	replace := map[string]string{"PATH": "/usr/bin:/bin"}

	tmp := t.TempDir()
	evil := mustMkdir(t, filepath.Join(tmp, "evil", ".git", "hooks"))
	mustWriteFile(t, filepath.Join(tmp, "evil", ".git", "config"), "[core]\n\trepositoryformatversion = 0\n")
	mustWriteFile(t, filepath.Join(evil, "pre-commit"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(evil, "pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Smuggled and literal -C steer into the hooked repo: exactly one
	// prompt naming the hook (the steered directory is audited, not the
	// clean base).
	ask := []struct {
		name    string
		command string
		args    []string
	}{
		{"shell split-string chdir into hook", `env -S '-C ` + filepath.Join(tmp, "evil") + ` git status'`, nil},
		{"shell split-string long chdir into hook", `env -S '--chdir=` + filepath.Join(tmp, "evil") + ` git status'`, nil},
		{"explicit split-string chdir into hook", "env", []string{"-S", "-C " + filepath.Join(tmp, "evil") + " git status"}},
		{"explicit literal chdir into hook", "env", []string{"-C", filepath.Join(tmp, "evil"), "git", "status"}},
		{"SE-1 shell assign before nested env chdir into hook", `env -S 'X=1 env -C ` + filepath.Join(tmp, "evil") + ` git status'`, nil},
		{"SE-1 explicit assign before nested env chdir into hook", "env", []string{"-S", "X=1 env -C " + filepath.Join(tmp, "evil") + " git status"}},
		{"SE-1 explicit assigns before nested env chdir into hook", "env", []string{"X=1", "env", "-C", filepath.Join(tmp, "evil"), "git", "status"}},
	}
	for _, tc := range ask {
		t.Run(tc.name, func(t *testing.T) {
			seen = nil
			if _, err := buildExecCommand(context.Background(), nil, "sess", tc.command, tc.args, tmp, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
				t.Fatalf("approved steered build should proceed, got %v", err)
			}
			if len(seen) != 1 || !strings.Contains(seen[0], "pre-commit") {
				t.Fatalf("steered build must prompt naming the hook (want 1, got %d): %v", len(seen), seen)
			}
		})
	}

	// Non-git-led smuggled -C still asks via the directory-override leg.
	seen = nil
	if _, err := buildExecCommand(context.Background(), nil, "sess", `env -S '-C /tmp ls'`, nil, tmp, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
		t.Fatalf("approved non-git-led steered build should proceed, got %v", err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "env -C") {
		t.Fatalf("non-git-led smuggled -C must prompt via the directory override (want 1, got %d): %v", len(seen), seen)
	}
}

// TestGitPreGateCdSegmentWorkdir pins the HIGH-1 segment-aware workdir
// audit: `cd <evil> && git status` audits the cd target's repository,
// not just the process working directory. A clean cd target stays
// silent; quoting, semicolon chaining, and relative targets resolve the
// same way the spawned shell would see them.
func TestGitPreGateCdSegmentWorkdir(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	replace := map[string]string{"PATH": "/usr/bin:/bin"}

	tmp := t.TempDir()
	evil := mustMkdir(t, filepath.Join(tmp, "evil"))
	mustMkdir(t, filepath.Join(evil, ".git"))
	mustWriteFile(t, filepath.Join(evil, ".git", "config"), "[core]\n\tpager = evil-pager\n")
	clean := mustMkdir(t, filepath.Join(tmp, "clean"))
	mustMkdir(t, filepath.Join(clean, ".git"))
	mustWriteFile(t, filepath.Join(clean, ".git", "config"), "[core]\n\trepositoryformatversion = 0\n")

	ask := []struct {
		name    string
		command string
	}{
		{"cd evil and-chain", "cd " + evil + " && git status"},
		{"cd evil semicolon-chain", "cd " + evil + "; git status"},
		{"cd evil quoted", "cd \"" + evil + "\" && git status"},
		{"relative cd evil", "cd evil && git status"},
	}
	for _, tc := range ask {
		t.Run(tc.name, func(t *testing.T) {
			seen = nil
			if _, err := buildExecCommand(context.Background(), nil, "sess", tc.command, nil, tmp, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
				t.Fatalf("approved cd trampoline should build, got %v", err)
			}
			if len(seen) != 1 {
				t.Fatalf("cd into evil repo must prompt (want 1, got %d): %v", len(seen), seen)
			}
		})
	}

	// Clean target and no-cd control stay silent.
	for _, cmd := range []string{
		"cd " + clean + " && git status",
		"git status",
	} {
		seen = nil
		wd := tmp
		if cmd == "git status" {
			wd = clean
		}
		if _, err := buildExecCommand(context.Background(), nil, "sess", cmd, nil, wd, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
			t.Errorf("clean command %q should build silently, got %v", cmd, err)
		}
		if len(seen) != 0 {
			t.Errorf("clean command %q must not prompt, got %v", cmd, seen)
		}
	}
}

// TestGitPreGateDirRedirectHoist pins the HIGH-4 hoist extension:
// repository redirects (GIT_DIR/WORK_TREE/COMMON_DIR) and config
// injectors in the overlay always ask, even for non-git-led builds and
// even on the git-led path below the deny line.
func TestGitPreGateDirRedirectHoist(t *testing.T) {
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	replace := map[string]string{"PATH": "/usr/bin:/bin"}

	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"} {
		// Non-git-led trampoline shape still asks.
		seen = nil
		wd := t.TempDir()
		if _, err := buildExecCommand(context.Background(), nil, "sess", "sh", []string{"-c", "git x"}, wd, map[string]string{key: "/tmp/x"}); err != nil {
			t.Errorf("non-git-led overlay %s should build after approval, got %v", key, err)
		}
		if len(seen) != 1 || !strings.Contains(seen[0], key) {
			t.Errorf("non-git-led overlay %s must prompt naming the key, got %v", key, seen)
		}

		// Git-led path asks too (never silent, never denied).
		seen = nil
		wd = t.TempDir()
		if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, wd, map[string]string{key: "/tmp/x"}, ExecReplaceEnv(replace)); err != nil {
			t.Errorf("git-led overlay %s should build after approval, got %v", key, err)
		}
		if len(seen) != 1 || !strings.Contains(seen[0], key) {
			t.Errorf("git-led overlay %s must prompt naming the key, got %v", key, seen)
		}
	}

	// A config injector (GIT_CONFIG_COUNT) hoists on the non-git-led
	// path but still denies on the git-led path: arbitrary config
	// injection is never an Ask-level influence.
	seen = nil
	wd := t.TempDir()
	if _, err := buildExecCommand(context.Background(), nil, "sess", "sh", []string{"-c", "git x"}, wd, map[string]string{"GIT_CONFIG_COUNT": "1"}); err != nil {
		t.Errorf("non-git-led GIT_CONFIG_COUNT should build after approval, got %v", err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "GIT_CONFIG_COUNT") {
		t.Errorf("non-git-led GIT_CONFIG_COUNT must prompt naming the key, got %v", seen)
	}
	seen = nil
	wd = t.TempDir()
	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, wd, map[string]string{"GIT_CONFIG_COUNT": "1"}, ExecReplaceEnv(replace)); err == nil || !strings.Contains(err.Error(), "denied by protection policy") {
		t.Errorf("git-led GIT_CONFIG_COUNT: want protection-policy denial, got %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("deny path must not prompt for approval, got %d prompts", len(seen))
	}
}

// TestGitConfigTextExtTransport pins the HIGH-2 ext:: legs: any url
// using the ext:: transport and any insteadOf rewrite pointing at ext::
// ask, as does a protocol.ext.allow enablement (always/user, quoted or
// bare, via `[protocol "ext"]` or the bare `[protocol]` default). The
// `never` value and other protocols stay green, exactly like ordinary
// https URLs.
func TestGitConfigTextExtTransport(t *testing.T) {
	asks := []struct {
		name string
		text string
	}{
		{"remote ext url", "[remote \"origin\"]\n\turl = ext::ssh -i key host repo\n"},
		{"remote quoted ext url", "[remote \"origin\"]\n\turl = \"ext::ssh host repo\"\n"},
		{"submodule ext url", "[submodule \"foo\"]\n\turl = ext::git host repo\n"},
		{"url base insteadOf ext", "[url \"https://example.com/\"]\n\tinsteadOf = ext::ssh host\n"},
		{"url base quoted insteadOf ext", "[url \"https://example.com/\"]\n\tinsteadOf = \"ext::ssh host\"\n"},
		{"protocol ext allow always", "[protocol \"ext\"]\n\tallow = always\n"},
		{"protocol ext allow user", "[protocol \"ext\"]\n\tallow = user\n"},
		{"protocol ext dotted allow", "[protocol.ext]\n\tallow = always\n"},
		{"protocol bare allow always", "[protocol]\n\tallow = always\n"},
	}
	for _, tc := range asks {
		t.Run(tc.name, func(t *testing.T) {
			if !gitConfigTextHasExecHook(tc.text) {
				t.Errorf("expected exec hook for %q", tc.text)
			}
		})
	}

	green := []struct {
		name string
		text string
	}{
		{"remote https url", "[remote \"origin\"]\n\turl = https://example.com/r.git\n"},
		{"submodule ssh url", "[submodule \"foo\"]\n\turl = ssh://git@example.com/r.git\n"},
		{"url base insteadOf https", "[url \"https://example.com/\"]\n\tinsteadOf = https://mirror.example.com/\n"},
		{"protocol ext allow never", "[protocol \"ext\"]\n\tallow = never\n"},
		{"protocol https allow always", "[protocol \"https\"]\n\tallow = always\n"},
		{"protocol bare allow never", "[protocol]\n\tallow = never\n"},
	}
	for _, tc := range green {
		t.Run(tc.name, func(t *testing.T) {
			if gitConfigTextHasExecHook(tc.text) {
				t.Errorf("expected green for %q", tc.text)
			}
		})
	}
}

// TestGitConfigCredentialHelperSafeSet pins the HIGH-3 credential-helper
// rule: green only for stock safe helpers (bare, with optional
// arguments, quote-tolerant, empty clears). A ! shell helper, an
// absolute or relative path helper (even one naming a safe program),
// and any bare unknown name all ask.
func TestGitConfigCredentialHelperSafeSet(t *testing.T) {
	asks := []struct {
		name string
		text string
	}{
		{"bare unknown helper", "[credential]\n\thelper = myhelper\n"},
		{"relative helper", "[credential]\n\thelper = ./helper\n"},
		{"relative subdir helper", "[credential]\n\thelper = sub/dir/helper\n"},
		{"absolute safe-name helper", "[credential]\n\thelper = /usr/bin/store\n"},
		{"bang helper with args", "[credential]\n\thelper = !evil --flag\n"},
		{"quoted bare unknown", "[credential]\n\thelper = \"myhelper\"\n"},
	}
	for _, tc := range asks {
		t.Run(tc.name, func(t *testing.T) {
			if !gitConfigTextHasExecHook(tc.text) {
				t.Errorf("expected exec hook for %q", tc.text)
			}
		})
	}

	green := []struct {
		name string
		text string
	}{
		{"store", "[credential]\n\thelper = store\n"},
		{"cache with timeout args", "[credential]\n\thelper = cache --timeout=3600\n"},
		{"quoted store", "[credential]\n\thelper = \"store\"\n"},
		{"empty clears helpers", "[credential]\n\thelper =\n"},
		{"wincred platform helper", "[credential]\n\thelper = manager-core\n"},
	}
	for _, tc := range green {
		t.Run(tc.name, func(t *testing.T) {
			if gitConfigTextHasExecHook(tc.text) {
				t.Errorf("expected green for %q", tc.text)
			}
		})
	}
}

// TestGitWorkdirConfigWorktree pins the F2 config.worktree sibling: an
// exec hook in .git/config.worktree asks exactly like the main config,
// an oversize sibling asks, and a clean sibling stays silent.
func TestGitWorkdirConfigWorktree(t *testing.T) {
	// Evil sibling behind a clean main config.
	repo := t.TempDir()
	mustMkdir(t, filepath.Join(repo, ".git"))
	mustWriteFile(t, filepath.Join(repo, ".git", "config"), "[core]\n\trepositoryformatversion = 0\n")
	mustWriteFile(t, filepath.Join(repo, ".git", "config.worktree"), "[core]\n\tpager = evil-pager\n")
	if reason := gitWorkdirConfigAskReason(repo); !strings.Contains(reason, "config.worktree") {
		t.Errorf("evil config.worktree sibling should ask naming the file, got %q", reason)
	}

	// Clean sibling stays green.
	mustWriteFile(t, filepath.Join(repo, ".git", "config.worktree"), "[core]\n\tbare = false\n")
	if reason := gitWorkdirConfigAskReason(repo); reason != "" {
		t.Errorf("clean config.worktree sibling must stay silent, got %q", reason)
	}

	// Missing sibling stays green.
	repo2 := t.TempDir()
	mustMkdir(t, filepath.Join(repo2, ".git"))
	mustWriteFile(t, filepath.Join(repo2, ".git", "config"), "[core]\n\trepositoryformatversion = 0\n")
	if reason := gitWorkdirConfigAskReason(repo2); reason != "" {
		t.Errorf("missing config.worktree sibling must stay silent, got %q", reason)
	}

	// Oversize sibling fails closed.
	var b strings.Builder
	b.WriteString("[core]\n\trepositoryformatversion = 0\n")
	for b.Len() < 64*1024+16 {
		b.WriteString("# benign padding past the audit cap\n")
	}
	mustWriteFile(t, filepath.Join(repo2, ".git", "config.worktree"), b.String())
	if reason := gitWorkdirConfigAskReason(repo2); !strings.Contains(reason, "config.worktree") {
		t.Errorf("oversize config.worktree sibling should ask, got %q", reason)
	}

	// End to end: plain git status in the repo asks through the pre-gate.
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	replace := map[string]string{"PATH": "/usr/bin:/bin"}
	mustWriteFile(t, filepath.Join(repo, ".git", "config.worktree"), "[core]\n\tpager = evil-pager\n")
	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, repo, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
		t.Fatalf("approved config.worktree build should proceed, got %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("evil config.worktree must prompt (want 1, got %d): %v", len(seen), seen)
	}
}

// TestGitWorkdirHooksAsk pins the F3 hooks audit: an executable
// non-sample hook under .git/hooks asks (end to end through the
// pre-gate for plain git status), while sample templates,
// non-executable files, and a missing hooks directory stay green.
func TestGitWorkdirHooksAsk(t *testing.T) {
	mkRepo := func(t *testing.T, withHook func(hooks string)) string {
		t.Helper()
		repo := t.TempDir()
		hooks := mustMkdir(t, filepath.Join(repo, ".git", "hooks"))
		mustWriteFile(t, filepath.Join(repo, ".git", "config"), "[core]\n\trepositoryformatversion = 0\n")
		if withHook != nil {
			withHook(hooks)
		}
		return repo
	}

	// Executable hook asks.
	repo := mkRepo(t, func(hooks string) {
		mustWriteFile(t, filepath.Join(hooks, "pre-commit"), "#!/bin/sh\nexit 0\n")
		if err := os.Chmod(filepath.Join(hooks, "pre-commit"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, filepath.Join(hooks, "pre-commit.sample"), "#!/bin/sh\nexit 0\n")
	})
	if reason := gitWorkdirHooksAskReason(repo); !strings.Contains(reason, "requires user approval") {
		t.Fatalf("executable hook should ask, got %q", reason)
	}

	// Samples and non-executable files stay green.
	repo2 := mkRepo(t, func(hooks string) {
		mustWriteFile(t, filepath.Join(hooks, "pre-commit.sample"), "#!/bin/sh\nexit 0\n")
		mustWriteFile(t, filepath.Join(hooks, "pre-commit"), "#!/bin/sh\nexit 0\n")
	})
	if reason := gitWorkdirHooksAskReason(repo2); reason != "" {
		t.Fatalf("non-executable hook must stay silent, got %q", reason)
	}

	// Missing hooks directory stays green.
	repo3 := t.TempDir()
	mustMkdir(t, filepath.Join(repo3, ".git"))
	mustWriteFile(t, filepath.Join(repo3, ".git", "config"), "[core]\n\trepositoryformatversion = 0\n")
	if reason := gitWorkdirHooksAskReason(repo3); reason != "" {
		t.Fatalf("missing hooks dir must stay silent, got %q", reason)
	}

	// End to end: plain git status in the hooked repo asks once, naming
	// the hook; the sample-only repo stays silent.
	var seen []string
	gitPreGateTestStubs(t, func() (bool, error) { return true, nil }, &seen)
	replace := map[string]string{"PATH": "/usr/bin:/bin"}
	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, repo, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
		t.Fatalf("approved hooked build should proceed, got %v", err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "pre-commit") {
		t.Fatalf("hooked repo must prompt naming the hook (want 1, got %d): %v", len(seen), seen)
	}
	seen = nil
	if _, err := buildExecCommand(context.Background(), nil, "sess", "git", []string{"status"}, repo2, map[string]string{}, ExecReplaceEnv(replace)); err != nil {
		t.Fatalf("sample-only build should proceed, got %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("sample-only repo must not prompt, got %v", seen)
	}
}

// TestAuditProseFreeTextGreen pins SE-2 scope: quoted free-text operands
// (commit messages, grep patterns, echo text) are never re-split, so
// benign prose carrying spaces or slashes stays green. Only -S values
// are unwrapped via unwrapEnvLayers (see TestAuditQuotedSplitPayloadPaths
// for the deny side). A blanket whitespace re-split denied these.
func TestAuditProseFreeTextGreen(t *testing.T) {
	workspace := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{workspace},
		ReadRoots:      []string{workspace},
	}
	cases := []struct {
		name    string
		command string
		args    []string
	}{
		{name: "shell commit message prose", command: `git commit -m "escape ../config"`},
		{name: "explicit commit message prose", command: "git", args: []string{"commit", "-m", "escape ../config"}},
		{name: "shell grep pattern prose", command: `grep "TODO ../outside"`},
		{name: "explicit grep pattern prose", command: "grep", args: []string{"TODO ../outside"}},
		{name: "shell echo prose with absolute", command: `echo "see /root/secret"`},
		{name: "explicit echo prose with absolute", command: "echo", args: []string{"see /root/secret"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := AuditSystemCommandPaths(sb, tc.command, tc.args, workspace); err != nil {
				t.Fatalf("prose must stay green, got error: %v", err)
			}
		})
	}
}

// TestAuditProseOperatorPieces pins the split rule for operator-bearing
// tokens: a whitespace-free token is a real attached-redirect shape and
// audits as a path (read-root confinement included), while a
// whitespace-carrying token is quoted free text, so its pieces still get
// deny-root/denied-basename checks but skip the read-root confinement. That
// keeps a benign commit message like "fix a && /opt + echo" green without
// weakening a denied path or an attached redirect.
func TestAuditProseOperatorPieces(t *testing.T) {
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{"/home/user/project"},
		ReadRoots:      []string{"/home/user/project"},
		DenyRoots:      []string{"/home/user/.ssh"},
	}
	green := []struct {
		name    string
		command string
		args    []string
	}{
		{name: "shell commit prose", command: `git commit -m "fix a && /opt + echo"`},
		{name: "explicit commit prose", command: "git", args: []string{"commit", "-m", "fix a && /opt + echo"}},
		{name: "shell prose with redirect word", command: `git commit -m "see /opt > notes"`},
	}
	for _, tc := range green {
		t.Run(tc.name, func(t *testing.T) {
			if err := AuditSystemCommandPaths(sb, tc.command, tc.args, ""); err != nil {
				t.Fatalf("prose pieces must stay green, got %v", err)
			}
		})
	}

	deny := []struct {
		name    string
		command string
	}{
		// Prose pieces still hit the deny root even without scope math.
		{name: "prose denied root", command: `git commit -m "fix a && /home/user/.ssh/id_rsa"`},
		// Whitespace-free attached redirects keep full path auditing.
		{name: "attached denied root", command: `cat</home/user/.ssh/id_rsa`},
		{name: "attached outside scope", command: `cat</opt/outside`},
	}
	for _, tc := range deny {
		t.Run(tc.name, func(t *testing.T) {
			if err := AuditSystemCommandPaths(sb, tc.command, nil, ""); err == nil {
				t.Fatalf("attached/prose denied path must be rejected, got nil")
			}
		})
	}
}

// TestSpliceSplitParityMirror pins the cross-layer contract from the
// sandbox side: spliceEnvLayer splices the same -S/--chdir shapes the
// gate expandEnvSplitString splices (see TestExpandSpliceOptionsTable in
// internal/agent). Keep the two tables in sync by construction.
func TestSpliceSplitParityMirror(t *testing.T) {
	cases := []struct {
		name string
		rest []string
		want []string
	}{
		{name: "split-string splices", rest: []string{"-S", "FOO=bar ls"}, want: []string{"FOO=bar", "ls"}},
		{name: "long split-string splices", rest: []string{"--split-string", "FOO=bar ls"}, want: []string{"FOO=bar", "ls"}},
		{name: "long abbrev split-string splices", rest: []string{"--sp", "FOO=bar ls"}, want: []string{"FOO=bar", "ls"}},
		{name: "nested split assign before env", rest: []string{"-S", "X=1 env -C /evil git status"}, want: []string{"X=1", "git", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, unk := spliceEnvLayer(tc.rest)
			if unk {
				t.Fatalf("spliceEnvLayer(%q) = unknown, want %q", tc.rest, tc.want)
			}
			if len(out) != len(tc.want) {
				t.Fatalf("spliceEnvLayer(%q) = %q, want %q", tc.rest, out, tc.want)
			}
			for i := range out {
				if out[i] != tc.want[i] {
					t.Fatalf("spliceEnvLayer(%q) = %q, want %q", tc.rest, out, tc.want)
				}
			}
		})
	}
}

// TestAuditQuotedSplitPayloadPaths pins SE-2: a quoted -S payload hides
// absolute operands inside one multi-word token (`env -S 'cat
// /home/u/.ssh/id_rsa'` tokenizes the payload as a single "cat /home/..."
// token). The audit unwraps -S values via unwrapEnvLayers so each
// spliced operand is confined on its own; a denied absolute path
// inside the payload is rejected in both the shell and explicit-argv
// forms. Quoted free-text elsewhere is never re-split (see
// TestAuditProseFreeTextGreen).
func TestAuditQuotedSplitPayloadPaths(t *testing.T) {
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{"/home/user/project"},
		ReadRoots:      []string{"/home/user/project"},
		DenyRoots:      []string{"/home/user/.ssh"},
	}
	cases := []struct {
		name    string
		command string
		args    []string
	}{
		{name: "shell quoted payload", command: `env -S 'cat /home/user/.ssh/id_rsa'`},
		{name: "explicit argv quoted payload", command: "env", args: []string{"-S", "cat /home/user/.ssh/id_rsa"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := AuditSystemCommandPaths(sb, tc.command, tc.args, ""); err == nil || !strings.Contains(err.Error(), "denied sandbox root") {
				t.Fatalf("quoted -S payload absolute path must be rejected, got %v", err)
			}
		})
	}
}

// TestAuditQuotedSplitPayloadEscapedQuoteFailClosed pins F1: a backslash
// escapes the next rune under shlex rules (outside quotes and inside
// double quotes; literal inside single quotes), so `env -S 'cat /abs \'`
// splices to ["cat" "/abs" ...] and denies exactly like the control
// without the backslash (whose unbalanced quote fails closed in the SE-2
// leg instead of continuing silent).
func TestAuditQuotedSplitPayloadEscapedQuoteFailClosed(t *testing.T) {
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{"/home/user/project"},
		ReadRoots:      []string{"/home/user/project"},
		DenyRoots:      []string{"/home/user/.ssh"},
	}
	cases := []struct {
		name    string
		command string
		args    []string
	}{
		{name: "shell escaped quote payload", command: `env -S 'cat /home/user/.ssh/id_rsa \''`},
		{name: "explicit escaped quote payload", command: "env", args: []string{"-S", `cat /home/user/.ssh/id_rsa \''`}},
		{name: "shell unbalanced control", command: `env -S 'cat /home/user/.ssh/id_rsa '`},
		{name: "explicit unbalanced control", command: "env", args: []string{"-S", `cat /home/user/.ssh/id_rsa '`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := AuditSystemCommandPaths(sb, tc.command, tc.args, ""); err == nil {
				t.Fatalf("escaped/unbalanced -S payload must be rejected, got nil")
			}
		})
	}
}

// TestAuditAttachedRedirectDenyRoot pins F-A: the path audit sees the
// target of an attached shell redirect the way the shell does, so a denied
// root cannot be read or written by gluing the operator to the command
// (`cat</deny`), prefixing an FD (`0</deny`, `2>>/deny`), doubling slashes,
// or escaping characters inside the target (`id_r\sa` opens id_rsa).
// Redirect-aware auditPathToken is the single helper, so the SE-2 quoted
// -S payload leg audits spliced operands exactly like the raw tokens (and
// the explicit-argv form matches the shell form). A backslash-escaped
// operator is a literal word, not a redirect, so that shape stays green
// together with quoted prose - the escape must not widen the deny.
func TestAuditAttachedRedirectDenyRoot(t *testing.T) {
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{"/home/user/project"},
		ReadRoots:      []string{"/home/user/project"},
		DenyRoots:      []string{"/home/user/.ssh"},
	}
	deny := []struct {
		name    string
		command string
		args    []string
	}{
		{name: "attached stdin", command: `cat</home/user/.ssh/id_rsa`},
		{name: "attached stdout", command: `echo hi >/home/user/.ssh/out`},
		{name: "attached read-write", command: `cat<>/home/user/.ssh/id_rsa`},
		{name: "attached append", command: `echo hi 2>>/home/user/.ssh/out`},
		{name: "fd prefixed stdin", command: `cat0</home/user/.ssh/id_rsa`},
		{name: "amp redirect", command: `cat&>/home/user/.ssh/out`},
		{name: "doubled slash target", command: `cat<//home/user/.ssh/id_rsa`},
		{name: "escaped target character", command: `cat</home/user/.ssh/id_r\sa`},
		{name: "dot segments in target", command: `cat</home/user/.ssh/../.ssh/id_rsa`},
		{name: "explicit argv attached stdin", command: "cat", args: []string{"</home/user/.ssh/id_rsa"}},
		{name: "SE-2 payload attached stdin", command: `env -S 'cat</home/user/.ssh/id_rsa'`},
		{name: "SE-2 payload separated stdin", command: `env -S 'cat </home/user/.ssh/id_rsa'`},
		{name: "SE-2 explicit payload", command: "env", args: []string{"-S", "cat</home/user/.ssh/id_rsa"}},
	}
	for _, tc := range deny {
		t.Run(tc.name, func(t *testing.T) {
			err := AuditSystemCommandPaths(sb, tc.command, tc.args, "")
			if err == nil || !strings.Contains(err.Error(), "denied sandbox root") {
				t.Fatalf("attached redirect target must be rejected, got %v", err)
			}
		})
	}
	// Escaped operators are literal words (the shell never opens the path),
	// and quoted prose pieces are bare words: neither may deny.
	green := []string{
		`cat\</home/user/.ssh/id_rsa`,
		`echo fix > bug`,
	}
	for _, cmd := range green {
		t.Run("green "+cmd, func(t *testing.T) {
			if err := AuditSystemCommandPaths(sb, cmd, nil, ""); err != nil {
				t.Fatalf("literal/prose token must stay green, got %v", err)
			}
		})
	}
}

// TestFullBuildDeepEnvNestingLinear pins F-B: the full build (git pre-gate
// plus path audit) unwraps a bare env chain once per invocation instead of
// re-unwrapping every suffix. Before the coveredUntil linearization the
// pre-gate alone took ~245ms at 1000 layers (quadratic, ~4x per doubling).
// The interleaved `env x` shape is the harder case: x is the utility, so the
// every-position scan must stop at the first non-wrapper operand instead of
// re-unwrapping the remaining tail per env token (it took 0.9s/3.6s/14.3s at
// 400/800/1600 pairs before the stop). Both shapes are benign, so the build
// proceeds rather than failing closed. The 5s bound absorbs slow CI hosts
// while still catching a quadratic regression (x2 cost per doubling).
func TestFullBuildDeepEnvNestingLinear(t *testing.T) {
	allowGate(t)
	clearAmbientGitEnv(t)
	wd := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{wd},
		ReadRoots:      []string{wd},
	}
	const n = 4000
	cmd := strings.TrimSpace(strings.Repeat("env ", n) + "git status")
	start := time.Now()
	if _, err := buildExecCommand(context.Background(), sb, "", cmd, nil, wd, nil); err != nil {
		t.Fatalf("benign %d-layer env build must proceed, got %v", n, err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("full build of %d nested env layers took %v, want <5s (quadratic regression)", n, elapsed)
	}

	const interleaved = 1600
	cmd = strings.TrimSpace(strings.Repeat("env x ", interleaved) + "git status")
	start = time.Now()
	if _, err := buildExecCommand(context.Background(), sb, "", cmd, nil, wd, nil); err != nil {
		t.Fatalf("benign %d-pair env x build must proceed, got %v", interleaved, err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("full build of %d interleaved env x pairs took %v, want <5s (quadratic regression)", interleaved, elapsed)
	}
}

// TestAuditSubstGitLedAsk pins the Low: a substitution-led git token
// (`$(git -C /evil status)`) still executes git, so the pre-gate treats
// it as git-led and audits the working directory's repository (hooks and
// config) instead of missing it entirely. With a hooked repository in
// scope the pre-gate asks; the directory-override Ask itself rides the
// gate leg, so this leg pins the repository audit firing through the
// substitution opener.
func TestAuditSubstGitLedAsk(t *testing.T) {
	workspace := t.TempDir()
	hooks := filepath.Join(workspace, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookPath := filepath.Join(hooks, "pre-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{workspace},
		ReadRoots:      []string{workspace},
	}
	deny, ask := auditGitPreGate(`$(git -C /evil status)`, nil, workspace, nil, &execOptions{}, sb)
	if deny != "" {
		t.Fatalf("substitution-led git must not deny, got deny %q", deny)
	}
	if ask == "" {
		t.Fatalf("substitution-led git in hooked repo must ask, got empty")
	}
	// Bare substitution-led status in the same hooked repo asks too.
	deny, ask = auditGitPreGate(`$(git status)`, nil, workspace, nil, &execOptions{}, sb)
	if deny != "" || ask == "" {
		t.Fatalf("$(git status) in hooked repo must ask, got deny=%q ask=%q", deny, ask)
	}
}
