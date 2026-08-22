package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
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
