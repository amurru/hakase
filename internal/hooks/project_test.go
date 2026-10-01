package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProjectFile(t *testing.T, root, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".hakase")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadProjectFileValid(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, `{"PreToolUse":[{"matcher":"^x$","hooks":[{"name":"p","command":["/bin/true"]}]}],"SessionStart":[{"hooks":[{"command":["/bin/true"]}]}]}`)
	f, err := LoadProjectFile(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if f == nil || len(f.PreToolUse) != 1 || len(f.SessionStart) != 1 {
		t.Fatalf("file = %+v, want 1 pre + 1 session group", f)
	}
	if h := f.PreToolUse[0].Hooks[0]; h.Timeout != DefaultTimeoutSeconds || h.OnFailure != DefaultOnFailure {
		t.Errorf("defaults not applied: %+v", h)
	}
}

func TestLoadProjectFileMissingIsNil(t *testing.T) {
	if f, err := LoadProjectFile(t.TempDir()); err != nil || f != nil {
		t.Errorf("missing file = (%v, %v), want (nil, nil)", f, err)
	}
	if f, err := LoadProjectFile(""); err != nil || f != nil {
		t.Errorf("empty root = (%v, %v), want (nil, nil)", f, err)
	}
}

func TestLoadProjectFileRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"unknown key", `{"PreToolUse":[]}`, "want one of"}, // valid actually; replaced below
		{"enabled rejected", `{"enabled":true}`, "enabled"},
		{"bogus key", `{"Elicitation":[]}`, "unknown key"},
		{"bad handler", `{"PreToolUse":[{"hooks":[{"command":[]}]}]}`, "command"},
		{"fail-closed post", `{"PostToolUse":[{"hooks":[{"command":["/bin/true"],"on_failure":"block"}]}]}`, "PreToolUse"},
		{"session matcher", `{"SessionStart":[{"matcher":"x","hooks":[{"command":["/bin/true"]}]}]}`, "unconditionally"},
		{"not json", `{oops`, "invalid project hooks"},
	}
	cases[0] = struct {
		name string
		body string
		want string
	}{"empty groups ok", `{"PreToolUse":[]}`, ""}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFile(t, root, tc.body)
			f, err := LoadProjectFile(root)
			if tc.want == "" {
				if err != nil || f == nil {
					t.Fatalf("load = (%v, %v), want success", f, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load = (%v, %v), want error containing %q", f, err, tc.want)
			}
		})
	}
}

func TestLoadProjectFileSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "evil.json")
	if err := os.WriteFile(target, []byte(`{"PreToolUse":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".hakase")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "hooks.json")); err != nil {
		t.Skip("symlinks unavailable")
	}
	_, err := LoadProjectFile(root)
	if err == nil || !strings.Contains(err.Error(), "escapes the project root") {
		t.Fatalf("load = %v, want symlink-escape error", err)
	}
}

func TestLoadProjectFileRelativeResolution(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, `{"PreToolUse":[{"hooks":[{"command":["scripts/guard.sh","--strict"]}]}]}`)
	f, err := LoadProjectFile(root)
	if err != nil {
		t.Fatal(err)
	}
	got := f.PreToolUse[0].Hooks[0].Command[0]
	if got != filepath.Join(root, "scripts/guard.sh") {
		t.Errorf("relative command = %q, want rooted at %q", got, root)
	}
	if fp1, fp2 := f.PreToolUse[0].Hooks[0].Fingerprint(), f.PreToolUse[0].Hooks[0].Fingerprint(); fp1 != fp2 {
		t.Error("fingerprint must be deterministic")
	}
}

func TestProjectHooksPath(t *testing.T) {
	if got := ProjectHooksPath(""); got != "" {
		t.Errorf("empty root = %q, want empty", got)
	}
	if got := ProjectHooksPath("/r"); got != filepath.Join("/r", ".hakase", "hooks.json") {
		t.Errorf("path = %q", got)
	}
}

// TestLoadProjectFileAliasedRoot pins the escape-guard fix: when the root
// itself arrives through an alias (symlinked dir, Windows 8.3 short name),
// a hooks file inside it must still load. Comparing a resolved file
// against an unresolved root false-positived on Windows CI (RUNNER~1 vs
// runneradmin) and broke every project-layer test there.
func TestLoadProjectFileAliasedRoot(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, `{"PreToolUse":[{"hooks":[{"command":["/bin/true"]}]}]}`)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f, err := LoadProjectFile(alias)
	if err != nil {
		t.Fatalf("aliased root must load: %v", err)
	}
	if f == nil || len(f.PreToolUse) != 1 {
		t.Fatalf("file = %+v, want 1 pre group", f)
	}
}

// TestInterpreterFormScriptCovered pins the trust property: a hook that
// runs a script through an interpreter (["/bin/sh", "guard.sh"]) must
// change fingerprint when the SCRIPT body changes, even though argv[0]
// and the argv strings do not.
func TestInterpreterFormScriptCovered(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "guard.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := Handler{Command: []string{"/bin/sh", script}}
	before := h.Fingerprint()
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if after := h.Fingerprint(); after == before {
		t.Error("rewriting the interpreter-form script body must change the fingerprint")
	}
}

// TestBareCommandsStayOnPath pins the PATH behavior: bare argv[0] names
// must NOT be joined onto the project root (that manufactures a
// non-existent path and the hook fails open instead of running), while
// relative path-like elements still anchor at the root.
func TestBareCommandsStayOnPath(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, `{"PreToolUse":[{"hooks":[
		{"name":"bare","command":["python3","hook.py"]},
		{"name":"pathed","command":["scripts/guard.sh"]}
	]}]}`)
	f, err := LoadProjectFile(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := f.PreToolUse[0].Hooks[0].Command[0]; got != "python3" {
		t.Errorf("bare command = %q, want PATH lookup untouched", got)
	}
	// hook.py has no separator and names no file under root: untouched.
	if got := f.PreToolUse[0].Hooks[0].Command[1]; got != "hook.py" {
		t.Errorf("bare argv element = %q, want untouched", got)
	}
	want := filepath.Join(root, "scripts", "guard.sh")
	if got := f.PreToolUse[0].Hooks[1].Command[0]; got != want {
		t.Errorf("pathed command = %q, want %q", got, want)
	}
}
