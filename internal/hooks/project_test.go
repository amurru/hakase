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
