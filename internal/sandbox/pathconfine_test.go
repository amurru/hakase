package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustMkdir creates dir and fails the test on error.
func mustMkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

// mustSymlink creates a symlink and fails the test on error.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// mustWriteFile writes a file and fails the test on error.
func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestResolveScopedPathNilSandboxOff verifies that a nil *SandboxConfig
// falls back to filepath.Abs (the legacy resolvePath behavior) with no
// confinement.
func TestResolveScopedPathNilSandboxOff(t *testing.T) {
	sb := (*SandboxConfig)(nil)
	got, err := sb.ResolveScopedPath("some/relative/path", true)
	if err != nil {
		t.Fatalf("nil sandbox: unexpected error: %v", err)
	}
	want, _ := filepath.Abs("some/relative/path")
	if got != want {
		t.Errorf("nil sandbox: got %q, want %q", got, want)
	}
}

// TestResolveScopedPathModeOff verifies that Mode == SandboxModeOff allows
// everything (backward compat).
func TestResolveScopedPathModeOff(t *testing.T) {
	tmp := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModeOff,
		WorkspaceRoots: []string{tmp},
	}
	// Absolute path outside roots should be allowed in off mode.
	got, err := sb.ResolveScopedPath("/etc/passwd", true)
	if err != nil {
		t.Fatalf("off mode: unexpected error: %v", err)
	}
	if want := filepath.Clean("/etc/passwd"); got != want {
		t.Errorf("off mode: got %q, want %q", got, want)
	}
}

// TestResolveScopedPathTable runs the table-driven confinement tests.
func TestResolveScopedPathTable(t *testing.T) {
	tmp := t.TempDir()
	workspace := mustMkdir(t, filepath.Join(tmp, "workspace"))
	readRoot := mustMkdir(t, filepath.Join(tmp, "readonly"))
	outside := mustMkdir(t, filepath.Join(tmp, "outside"))

	// A file inside the workspace.
	insideFile := filepath.Join(workspace, "notes.txt")
	mustWriteFile(t, insideFile, "hi")

	// A file inside the read root.
	readFile := filepath.Join(readRoot, "data.txt")
	mustWriteFile(t, readFile, "data")

	// A file outside both roots.
	outsideFile := filepath.Join(outside, "secret.txt")
	mustWriteFile(t, outsideFile, "secret")

	// Symlink inside workspace pointing outside.
	evilLink := filepath.Join(workspace, "evil")
	mustSymlink(t, outsideFile, evilLink)

	// Deny root: a directory containing a "denied" file.
	denyDir := mustMkdir(t, filepath.Join(tmp, "deny"))
	deniedFile := filepath.Join(denyDir, "key")
	mustWriteFile(t, deniedFile, "key")

	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{workspace},
		ReadRoots:      []string{workspace, readRoot},
		DenyRoots:      []string{denyDir},
		Permissions: map[string]string{
			"system_exec":        "ask",
			"python_interpreter": "allow",
			"write_file":         "allow",
		},
	}

	type tc struct {
		name      string
		path      string
		write     bool
		wantErr   string // substring; "" = no error expected
		wantUnder string // result must be under this dir when no error
	}

	cases := []tc{
		{
			name:      "write inside workspace",
			path:      filepath.Join(workspace, "new.txt"),
			write:     true,
			wantUnder: workspace,
		},
		{
			name:    "write with ../etc/passwd escapes",
			path:    filepath.Join(workspace, "..", "..", "etc", "passwd"),
			write:   true,
			wantErr: "outside approved workspace",
		},
		{
			name:    "write absolute outside roots",
			path:    outsideFile,
			write:   true,
			wantErr: "outside approved workspace",
		},
		{
			name:    "symlink inside root pointing outside",
			path:    evilLink,
			write:   true,
			wantErr: "escapes workspace root after symlink resolution",
		},
		{
			name:    "deny root blocks write",
			path:    deniedFile,
			write:   true,
			wantErr: "denied root",
		},
		{
			name:    "deny root blocks read too",
			path:    deniedFile,
			write:   false,
			wantErr: "denied root",
		},
		{
			name:      "read from read_root allowed",
			path:      readFile,
			write:     false,
			wantUnder: readRoot,
		},
		{
			name:      "read from workspace allowed",
			path:      insideFile,
			write:     false,
			wantUnder: workspace,
		},
		{
			name:    "write to read_root denied (write only workspace)",
			path:    readFile,
			write:   true,
			wantErr: "outside approved workspace",
		},
		{
			name:    "read outside roots denied",
			path:    outsideFile,
			write:   false,
			wantErr: "outside approved read roots",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := sb.ResolveScopedPath(c.path, c.write)
			switch {
			case c.wantErr != "":
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (path=%q result=%q)", c.wantErr, c.path, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("expected error containing %q, got %q", c.wantErr, err.Error())
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if c.wantUnder != "" {
					// Resolve the result via EvalSymlinks when it exists so
					// the containment check matches the runtime's view.
					check := got
					if resolved, rerr := filepath.EvalSymlinks(got); rerr == nil {
						check = resolved
					}
					if !within(c.wantUnder, check) {
						t.Errorf("result %q (checked %q) not under %q", got, check, c.wantUnder)
					}
				}
			}
		})
	}
}

// TestLoadSandboxConfigDefaults verifies the defaulting rules.
func TestLoadSandboxConfigDefaults(t *testing.T) {
	t.Run("nil returns paths-mode default", func(t *testing.T) {
		got := LoadSandboxConfig(nil)
		if got == nil {
			t.Fatal("LoadSandboxConfig(nil) = nil, want non-nil paths config")
		}
		if got.Mode != SandboxModePaths {
			t.Errorf("Mode = %q, want %q", got.Mode, SandboxModePaths)
		}
	})

	t.Run("empty mode defaults to paths", func(t *testing.T) {
		sb := LoadSandboxConfig(&SandboxJSON{})
		if sb == nil {
			t.Fatal("expected non-nil")
		}
		if sb.Mode != SandboxModePaths {
			t.Errorf("Mode = %q, want %q", sb.Mode, SandboxModePaths)
		}
	})

	t.Run("unknown mode defaults to paths", func(t *testing.T) {
		sb := LoadSandboxConfig(&SandboxJSON{Mode: "nonsense"})
		if sb.Mode != SandboxModePaths {
			t.Errorf("Mode = %q, want %q", sb.Mode, SandboxModePaths)
		}
	})

	t.Run("known mode preserved", func(t *testing.T) {
		sb := LoadSandboxConfig(&SandboxJSON{Mode: "paths"})
		if sb.Mode != SandboxModePaths {
			t.Errorf("Mode = %q, want %q", sb.Mode, SandboxModePaths)
		}
	})

	t.Run("empty workspace roots default to dot", func(t *testing.T) {
		sb := LoadSandboxConfig(&SandboxJSON{Mode: "paths"})
		if len(sb.WorkspaceRoots) != 1 {
			t.Fatalf("WorkspaceRoots = %v, want 1 entry", sb.WorkspaceRoots)
		}
		// "." resolves to abs cwd.
		cwd, _ := os.Getwd()
		if sb.WorkspaceRoots[0] != filepath.Clean(cwd) {
			t.Errorf("WorkspaceRoots[0] = %q, want %q", sb.WorkspaceRoots[0], filepath.Clean(cwd))
		}
	})

	t.Run("empty read roots mirror workspace roots", func(t *testing.T) {
		sb := LoadSandboxConfig(&SandboxJSON{
			Mode:           "paths",
			WorkspaceRoots: []string{"."},
		})
		if len(sb.ReadRoots) != len(sb.WorkspaceRoots) {
			t.Fatalf("ReadRoots = %v, want %v", sb.ReadRoots, sb.WorkspaceRoots)
		}
		for i := range sb.ReadRoots {
			if sb.ReadRoots[i] != sb.WorkspaceRoots[i] {
				t.Errorf("ReadRoots[%d] = %q, want %q", i, sb.ReadRoots[i], sb.WorkspaceRoots[i])
			}
		}
	})

	t.Run("nil permissions get defaults", func(t *testing.T) {
		sb := LoadSandboxConfig(&SandboxJSON{Mode: "paths"})
		if sb.Permissions == nil {
			t.Fatal("Permissions nil")
		}
		if got := sb.Permissions["system_exec"]; got != "ask" {
			t.Errorf("Permissions[system_exec] = %q, want ask", got)
		}
		if got := sb.Permissions["python_interpreter"]; got != "allow" {
			t.Errorf("Permissions[python_interpreter] = %q, want allow", got)
		}
		if got := sb.Permissions["write_file"]; got != "allow" {
			t.Errorf("Permissions[write_file] = %q, want allow", got)
		}
	})

	t.Run("explicit permissions preserved", func(t *testing.T) {
		sb := LoadSandboxConfig(&SandboxJSON{
			Mode:        "paths",
			Permissions: map[string]string{"system_exec": "deny"},
		})
		if got := sb.Permissions["system_exec"]; got != "deny" {
			t.Errorf("Permissions[system_exec] = %q, want deny", got)
		}
		// Other defaults not added when map is non-nil.
		if _, ok := sb.Permissions["python_interpreter"]; ok {
			t.Error("expected python_interpreter absent when map was explicit")
		}
	})

	t.Run("new fields default to zero values", func(t *testing.T) {
		sb := LoadSandboxConfig(&SandboxJSON{Mode: "paths"})
		if sb.AllowedCommands != nil {
			t.Errorf("AllowedCommands = %v, want nil", sb.AllowedCommands)
		}
		if sb.DenyPatterns != nil {
			t.Errorf("DenyPatterns = %v, want nil", sb.DenyPatterns)
		}
		if sb.RiskThreshold != "" {
			t.Errorf("RiskThreshold = %q, want empty", sb.RiskThreshold)
		}
		if sb.AllowFallback {
			t.Error("AllowFallback = true, want false")
		}
	})

	t.Run("new fields round-trip through SandboxJSON", func(t *testing.T) {
		input := &SandboxJSON{
			Mode:            "bubblewrap",
			AllowedCommands: []string{"ls", "cat", "git"},
			RiskThreshold:   "high",
			DenyPatterns:    []string{"rm -rf /"},
			AllowFallback:   true,
		}
		sb := LoadSandboxConfig(input)
		if len(sb.AllowedCommands) != 3 || sb.AllowedCommands[0] != "ls" {
			t.Errorf("AllowedCommands = %v, want [ls cat git]", sb.AllowedCommands)
		}
		if sb.RiskThreshold != "high" {
			t.Errorf("RiskThreshold = %q, want high", sb.RiskThreshold)
		}
		if len(sb.DenyPatterns) != 1 || sb.DenyPatterns[0] != "rm -rf /" {
			t.Errorf("DenyPatterns = %v, want [rm -rf /]", sb.DenyPatterns)
		}
		if !sb.AllowFallback {
			t.Error("AllowFallback = false, want true")
		}
	})
}

// TestSandboxConfigWorkspaceRoot verifies workspaceRoot() returns the first
// root and "" when off/nil.
func TestSandboxConfigWorkspaceRoot(t *testing.T) {
	tmp := t.TempDir()
	a := mustMkdir(t, filepath.Join(tmp, "a"))
	b := mustMkdir(t, filepath.Join(tmp, "b"))

	if got := (*SandboxConfig)(nil).WorkspaceRoot(); got != "" {
		t.Errorf("nil workspaceRoot = %q, want empty", got)
	}
	sb := &SandboxConfig{Mode: SandboxModeOff, WorkspaceRoots: []string{a, b}}
	if got := sb.WorkspaceRoot(); got != "" {
		t.Errorf("off workspaceRoot = %q, want empty", got)
	}
	sb.Mode = SandboxModePaths
	if got := sb.WorkspaceRoot(); got != a {
		t.Errorf("workspaceRoot = %q, want %q", got, a)
	}
}

// TestSandboxConfigPermitted verifies permitted() reads the map correctly.
func TestSandboxConfigPermitted(t *testing.T) {
	sb := &SandboxConfig{
		Permissions: map[string]string{"system_exec": "ask"},
	}
	if action, ok := sb.Permitted("system_exec"); !ok || action != "ask" {
		t.Errorf("permitted(system_exec) = (%q, %v), want (ask, true)", action, ok)
	}
	if _, ok := sb.Permitted("missing"); ok {
		t.Error("permitted(missing) should be ok=false")
	}
	if _, ok := (*SandboxConfig)(nil).Permitted("system_exec"); ok {
		t.Error("nil permitted should be ok=false")
	}
}
