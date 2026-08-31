//go:build windows

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWithinCaseInsensitiveWindows pins that within() treats C:\Foo and
// c:\foo as identical and contains paths under the volume root.
func TestWithinCaseInsensitiveWindows(t *testing.T) {
	if !within(`C:\Foo`, `c:\foo\bar`) {
		t.Error("within: C:\\Foo must contain c:\\foo\\bar")
	}
	if !within(`c:\foo`, `C:\FOO`) {
		t.Error("within: c:\\foo must contain C:\\FOO")
	}
	if within(`C:\Foo`, `c:\foobar`) {
		t.Error("within: prefix collision must not count as containment")
	}
	if !within(`C:\`, `C:\Windows\System32`) {
		t.Error("within: volume root must contain paths under it")
	}
	if within(`C:\`, `D:\Windows`) {
		t.Error("within: volume root must not contain other volumes")
	}
}

// TestCheckPathAliasAliases unit-tests the reject rules; each error must
// name the alias class.
func TestCheckPathAliasAliases(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`C:\ws\config.json.`, "trailing dot"},
		{`C:\ws\config.json `, "trailing dot or space"},
		{`C:\ws\config.json:ads`, "alternate data stream"},
		{`\\?\C:\ws\config.json`, "device-namespace"},
		{`\\.\C:\ws\config.json`, "device-namespace"},
		{`C:relative`, "drive-relative"},
	}
	for _, tc := range cases {
		err := checkPathAlias(tc.in)
		if err == nil {
			t.Errorf("checkPathAlias(%q): expected rejection, got nil", tc.in)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("checkPathAlias(%q): error %q must name alias class %q", tc.in, err, tc.want)
		}
	}
	// Benign forms pass.
	for _, ok := range []string{`C:\ws\config.json`, `config.json`, `src\main.go`, `..\.env`} {
		if err := checkPathAlias(ok); err != nil {
			t.Errorf("checkPathAlias(%q): unexpected rejection: %v", ok, err)
		}
	}
}

// withTestSandbox installs sb as CurrentSandbox for the test.
func withTestSandbox(t *testing.T, sb *SandboxConfig) {
	t.Helper()
	saved := CurrentSandbox
	CurrentSandbox = sb
	t.Cleanup(func() { CurrentSandbox = saved })
}

// TestResolveScopedPathAliasRegression runs the deny pipeline against the
// alias forms of a denied sensitive file, read and write, asserting each
// rejection names the alias class.
func TestResolveScopedPathAliasRegression(t *testing.T) {
	ws := t.TempDir()
	deniedFile := filepath.Join(ws, "config.json")
	if err := writeFileForTest(deniedFile, []byte(`{"api_key":"x"}`)); err != nil {
		t.Fatal(err)
	}
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{ws},
		ReadRoots:      []string{ws},
		DenyRoots:      []string{deniedFile},
		DenyBasenames:  []string{".env"},
	}
	withTestSandbox(t, sb)

	cases := []struct {
		in   string
		want string
	}{
		{"config.json.", "trailing dot"},
		{"config.json ", "trailing dot or space"},
		{ws + string(filepath.Separator) + "config.json:ads", "alternate data stream"},
		{`C:relative-escape`, "drive-relative"},
	}
	for _, tc := range cases {
		for _, write := range []bool{false, true} {
			_, err := sb.ResolveScopedPath(tc.in, write)
			if err == nil {
				t.Errorf("ResolveScopedPath(%q, write=%v): expected rejection, got nil", tc.in, write)
				continue
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ResolveScopedPath(%q, write=%v): error %q must name alias class %q", tc.in, write, err, tc.want)
			}
		}
	}

	// A benign workspace file still resolves.
	if _, err := sb.ResolveScopedPath(filepath.Join(ws, "notes.md"), false); err != nil {
		t.Errorf("benign read rejected: %v", err)
	}
}

// TestResolveScopedPathShortNameAlias checks that an 8.3 short-name alias of
// a denied file is folded by canonicalization and rejected. Skips with a
// reason on volumes where 8.3 name generation is disabled.
func TestResolveScopedPathShortNameAlias(t *testing.T) {
	ws := t.TempDir()
	long := "longfilename-deny-target.md"
	longPath := filepath.Join(ws, long)
	if err := writeFileForTest(longPath, []byte("secret")); err != nil {
		t.Fatal(err)
	}

	lp, err := windows.UTF16PtrFromString(longPath)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(lp, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Skipf("GetShortPathName unavailable: %v", err)
	}
	short := windows.UTF16ToString(buf[:n])
	if short == longPath || filepath.Base(short) == long {
		t.Skipf("8.3 name generation is disabled on this volume (short=%q)", short)
	}

	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{ws},
		ReadRoots:      []string{ws},
		DenyRoots:      []string{longPath},
	}
	withTestSandbox(t, sb)

	// The short name must resolve to the same final path and be denied.
	if _, err := sb.ResolveScopedPath(short, false); err == nil {
		t.Fatalf("8.3 alias %q of denied file was not rejected", short)
	} else if !strings.Contains(err.Error(), "denied root") {
		t.Errorf("8.3 alias rejection should name the denied root, got: %v", err)
	}
}

// TestCanonicalizePathFoldsCase verifies the handle-based resolution returns
// the on-disk casing (and junction folding happens implicitly through
// GetFinalPathNameByHandle).
func TestCanonicalizePathFoldsCase(t *testing.T) {
	ws := t.TempDir()
	real := filepath.Join(ws, "Config.JSON")
	if err := writeFileForTest(real, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	lower := filepath.Join(strings.ToLower(ws), "config.json")
	got, err := canonicalizePath(lower, false)
	if err != nil {
		t.Fatalf("canonicalizePath: %v", err)
	}
	if !strings.EqualFold(got, real) {
		t.Errorf("canonical form %q does not match real path %q", got, real)
	}
	if got != real {
		t.Errorf("canonical form %q should carry on-disk casing %q", got, real)
	}

	// Not-yet-existing path: canonicalizes the existing parent and appends.
	missing := filepath.Join(strings.ToLower(ws), "sub", "new.txt")
	got, err = canonicalizePath(missing, true)
	if err != nil {
		t.Fatalf("canonicalizePath(missing): %v", err)
	}
	if !strings.EqualFold(got, missing) {
		t.Errorf("missing-path canonical form %q does not match %q", got, missing)
	}
}

// TestBuildExecCommandCoercesBubblewrapOnWindows pins the defensive mode
// coercion: a bubblewrap-mode sandbox constructed directly (bypassing
// LoadSandboxConfig) is coerced to paths at BuildExecCommand and the audit
// entry records the effective mode.
func TestBuildExecCommandCoercesBubblewrapOnWindows(t *testing.T) {
	ws := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModeBubblewrap,
		WorkspaceRoots: []string{ws},
		ReadRoots:      []string{ws},
		DenyRoots:      []string{filepath.Join(ws, "config.json")},
	}
	withTestSandbox(t, sb)

	var auditMode string
	savedAudit := AuditCommandFunc
	savedGate := EvaluateCommandFunc
	AuditCommandFunc = func(entry CommandAuditEntry) { auditMode = entry.SandboxMode }
	EvaluateCommandFunc = func(sb *SandboxConfig, command string, args []string) GateDecision {
		return GateDecision{Action: ActionAllow, Risk: RiskLow}
	}
	t.Cleanup(func() {
		AuditCommandFunc = savedAudit
		EvaluateCommandFunc = savedGate
	})

	cmd, err := BuildExecCommand("echo hi", nil, "", nil)
	if err != nil {
		t.Fatalf("BuildExecCommand under bubblewrap config: %v", err)
	}
	if CurrentSandbox.Mode != SandboxModePaths {
		t.Errorf("mode not coerced: got %q", CurrentSandbox.Mode)
	}
	if cmd.Args[0] == "bwrap" {
		t.Error("command was wrapped in bwrap on Windows")
	}
	if auditMode != string(SandboxModePaths) {
		t.Errorf("audit SandboxMode: expected %q, got %q", SandboxModePaths, auditMode)
	}
}

// TestLoadSandboxConfigCoercesOnWindows pins the central coercion point.
func TestLoadSandboxConfigCoercesOnWindows(t *testing.T) {
	for _, mode := range []string{"bubblewrap", "landlock"} {
		sb := LoadSandboxConfig(&SandboxJSON{Mode: mode})
		if sb.Mode != SandboxModePaths {
			t.Errorf("LoadSandboxConfig(%q): expected coercion to paths, got %q", mode, sb.Mode)
		}
	}
}

// TestAuditRejectsShellExpansionTokens verifies %VAR% and !VAR! operands are
// rejected by the audit BEFORE cmd.exe runs, so an approved-looking command
// like `type %USERPROFILE%\.hakase\credentials.json` cannot reach the denied
// credential file.
func TestAuditRejectsShellExpansionTokens(t *testing.T) {
	ws := t.TempDir()
	sb := &SandboxConfig{
		Mode:           SandboxModePaths,
		WorkspaceRoots: []string{ws},
		ReadRoots:      []string{ws},
	}
	withTestSandbox(t, sb)

	for _, command := range []string{
		`type %USERPROFILE%\.hakase\credentials.json`,
		`type !USERPROFILE!\.hakase\credentials.json`,
		`type !X Y!\secret.txt`,
		`type %X Y%\secret.txt`,
	} {
		_, err := BuildExecCommand(command, nil, "", nil)
		if err == nil {
			t.Errorf("BuildExecCommand(%q): expected expansion-token rejection, got nil", command)
			continue
		}
		if !strings.Contains(err.Error(), "expansion") {
			t.Errorf("BuildExecCommand(%q): error should name shell expansion, got: %v", command, err)
		}
	}
}

func writeFileForTest(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}
