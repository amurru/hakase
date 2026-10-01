package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchSelectorOrder(t *testing.T) {
	snaps := []Snapshot{
		{Event: EventPreToolUse, Name: "guard", Fingerprint: "sha256:aaa111"},
		{Event: EventPreToolUse, Name: "guard-strict", Fingerprint: "sha256:bbb222"},
		{Event: EventPostToolUse, Name: "other", Fingerprint: "sha256:ccc333"},
	}
	if got := MatchSelector(snaps, "guard"); len(got) != 1 || got[0].Name != "guard" {
		t.Errorf("exact name must win: %+v", got)
	}
	// "ard" is a substring of both guard names; fingerprint fallback must
	// not leak in when names match.
	if got := MatchSelector(snaps, "ard-str"); len(got) != 1 || got[0].Name != "guard-strict" {
		t.Errorf("substring must win over fingerprint: %+v", got)
	}
	if got := MatchSelector(snaps, "bbb2"); len(got) != 1 {
		t.Errorf("fingerprint prefix must match: %+v", got)
	}
	if got := MatchSelector(snaps, "SHA256:CCC3"); len(got) != 1 {
		t.Errorf("fingerprint match must be case-insensitive: %+v", got)
	}
	if got := MatchSelector(snaps, ""); len(got) != 0 {
		t.Errorf("empty selector must match nothing: %+v", got)
	}
	if got := MatchSelector(snaps, "zzz"); len(got) != 0 {
		t.Errorf("unknown selector must match nothing: %+v", got)
	}
}

func TestMatchFingerprintsAllOrNothing(t *testing.T) {
	snaps := []Snapshot{
		{Name: "a", Fingerprint: "sha256:aaa111"},
		{Name: "b", Fingerprint: "sha256:bbb222"},
	}
	if _, err := MatchFingerprints(snaps, []string{"sha256:zzz"}); err == nil {
		t.Error("unknown prefix must fail")
	}
	if _, err := MatchFingerprints(snaps, []string{"sha256:"}); err == nil {
		t.Error("ambiguous prefix must fail")
	}
	got, err := MatchFingerprints(snaps, []string{"sha256:aaa1"})
	if err != nil || len(got) != 1 || got[0].Name != "a" {
		t.Errorf("unique prefix = %+v, %v", got, err)
	}
}

func TestParseAddArgs(t *testing.T) {
	a, err := ParseAddArgs([]string{"PreToolUse", "--matcher", "^x$", "--name", "n", "--timeout", "10", "--on-failure", "block", "--", "/bin/echo", "hi"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.Event != "PreToolUse" || a.Matcher != "^x$" || a.Name != "n" || a.Timeout != 10 || a.OnFailure != "block" {
		t.Errorf("fields = %+v", a)
	}
	if len(a.Argv) != 2 || a.Argv[0] != "/bin/echo" {
		t.Errorf("argv = %v", a.Argv)
	}
	for _, bad := range [][]string{
		{},
		{"PreToolUse", "/bin/true"},
		{"PreToolUse", "--"},
		{"PreToolUse", "--bogus", "--", "/bin/true"},
		{"PreToolUse", "--timeout", "x", "--", "/bin/true"},
		{"PreToolUse", "--timeout", "--", "/bin/true"},
	} {
		if _, err := ParseAddArgs(bad); err == nil {
			t.Errorf("ParseAddArgs(%v) must fail", bad)
		}
	}
	// Argv starting with a dash needs no escaping after `--`.
	a, err = ParseAddArgs([]string{"PreToolUse", "--", "/bin/echo", "-n"})
	if err != nil || len(a.Argv) != 2 {
		t.Errorf("dash argv = %+v, %v", a.Argv, err)
	}
}

func TestParseUpdateArgs(t *testing.T) {
	prefix, upd, err := ParseUpdateArgs([]string{"abc123", "--name", "n2", "--disable"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if prefix != "abc123" || upd.Name == nil || *upd.Name != "n2" || upd.Enabled == nil || *upd.Enabled {
		t.Errorf("update = %q %+v", prefix, upd)
	}
	if _, _, err := ParseUpdateArgs(nil); err == nil {
		t.Error("empty update must fail")
	}
	if _, _, err := ParseUpdateArgs([]string{"abc", "--bogus"}); err == nil {
		t.Error("unknown flag must fail")
	}
	if _, _, err := ParseUpdateArgs([]string{"abc", "--"}); err == nil {
		t.Error("bare -- with no argv must fail")
	}
	_, upd, err = ParseUpdateArgs([]string{"abc", "--", "/bin/true"})
	if err != nil || len(upd.Command) != 1 {
		t.Errorf("argv replace = %+v, %v", upd.Command, err)
	}
}

func TestScriptPreview(t *testing.T) {
	if got := ScriptPreview(nil); got != "" {
		t.Errorf("empty argv = %q, want empty", got)
	}
	if got := ScriptPreview([]string{"/nonexistent-12345/x.sh"}); got != "" {
		t.Errorf("missing file = %q, want empty", got)
	}
	dir := t.TempDir()
	sh := filepath.Join(dir, "h.sh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\necho hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ScriptPreview([]string{sh}); !strings.Contains(got, "echo hi") {
		t.Errorf("preview must show script body: %q", got)
	}
	bin := filepath.Join(dir, "b")
	if err := os.WriteFile(bin, []byte{0x7f, 'E', 'L', 'F', 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ScriptPreview([]string{bin}); !strings.Contains(got, "binary") {
		t.Errorf("binary must be suppressed: %q", got)
	}
}

func TestLookupUserHandler(t *testing.T) {
	var c Config
	if err := AddUserHook(&c, EventPreToolUse, "", Handler{Name: "g", Command: []string{"/bin/true"}}); err != nil {
		t.Fatal(err)
	}
	fp := c.PreToolUse[0].Hooks[0].Fingerprint()
	h, ok := LookupUserHandler(&c, EventPreToolUse, fp)
	if !ok || h.Name != "g" {
		t.Errorf("lookup = %+v, %v", h, ok)
	}
	if _, ok := LookupUserHandler(&c, EventPostToolUse, fp); ok {
		t.Error("wrong event must miss")
	}
	if _, ok := LookupUserHandler(&c, "Nope", fp); ok {
		t.Error("bad event must miss")
	}
}
