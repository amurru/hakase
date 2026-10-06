package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAddRemoveRoundTrip(t *testing.T) {
	p := writeCfg(t, `{"provider":"x","hooks":{"PreToolUse":[]}}`)
	block, err := WriteUserHooks(p, func(c *Config) error {
		return AddUserHook(c, EventPreToolUse, "^system_exec$", Handler{Name: "g", Command: []string{"/bin/true"}})
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(block.PreToolUse) != 1 || len(block.PreToolUse[0].Hooks) != 1 {
		t.Fatalf("block = %+v", block.PreToolUse)
	}
	fp := block.PreToolUse[0].Hooks[0].Fingerprint()
	raw, _ := os.ReadFile(p)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["provider"] != "x" {
		t.Errorf("sibling keys must survive, got %v", m)
	}
	snap, err := func() (Snapshot, error) {
		var out Snapshot
		_, err := WriteUserHooks(p, func(c *Config) error {
			var err error
			out, err = RemoveUserHook(c, fp[:16])
			return err
		})
		return out, err
	}()
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if snap.Name != "g" {
		t.Errorf("removed snapshot = %+v", snap)
	}
}

func TestPrefixResolutionErrors(t *testing.T) {
	var c Config
	if err := AddUserHook(&c, EventPreToolUse, "", Handler{Command: []string{"/bin/true"}}); err != nil {
		t.Fatal(err)
	}
	if err := AddUserHook(&c, EventPreToolUse, "", Handler{Command: []string{"/bin/false"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveUserHook(&c, "sha256:zzz"); err == nil || !strings.Contains(err.Error(), "no user hook") {
		t.Errorf("unknown prefix = %v, want no-match error", err)
	}
	if _, _, err := resolveUserHook(&c, "sha256:"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("shared prefix = %v, want ambiguous error", err)
	}
	if _, _, err := resolveUserHook(&c, ""); err == nil {
		t.Error("empty prefix must error")
	}
}

func TestSetEnabledAndUpdate(t *testing.T) {
	var c Config
	if err := AddUserHook(&c, EventPostToolUse, "", Handler{Name: "o", Command: []string{"/bin/true"}}); err != nil {
		t.Fatal(err)
	}
	fp := c.PostToolUse[0].Hooks[0].Fingerprint()
	snap, err := SetUserHookEnabled(&c, fp[:12], false)
	if err != nil || snap.Enabled {
		t.Fatalf("disable = %+v, %v", snap, err)
	}
	if c.PostToolUse[0].Hooks[0].Fingerprint() != fp {
		t.Error("disabling must not change the fingerprint")
	}
	newName := "renamed"
	newTimeout := 45
	upd := HookUpdate{Name: &newName, Timeout: &newTimeout}
	snap, err = UpdateUserHook(&c, fp[:12], upd)
	if err != nil || snap.Name != "renamed" || c.PostToolUse[0].Hooks[0].Timeout != 45 {
		t.Fatalf("update = %+v, %v", snap, err)
	}
	// Invalid update rolls back: on_failure:block is rejected off PreToolUse.
	bad := "block"
	if _, err := UpdateUserHook(&c, fp[:12], HookUpdate{OnFailure: &bad}); err == nil {
		t.Fatal("fail-closed update off PreToolUse must fail")
	}
	if got := c.PostToolUse[0].Hooks[0].OnFailure; got != OnFailureAllow {
		t.Errorf("rolled-back on_failure = %q", got)
	}
}

func TestAddRejectsBadEvent(t *testing.T) {
	var c Config
	if err := AddUserHook(&c, "Nope", "", Handler{Command: []string{"/bin/true"}}); err == nil {
		t.Error("unknown event must fail loud")
	}
}

func TestWriteRefusesMissingFile(t *testing.T) {
	_, err := WriteUserHooks(filepath.Join(t.TempDir(), "missing.json"), func(c *Config) error { return nil })
	if err == nil {
		t.Error("missing config must error, not create")
	}
}

func TestMasterEnabled(t *testing.T) {
	var c Config
	SetMasterEnabled(&c, false)
	if Enabled(&c) {
		t.Error("master off must disable")
	}
	SetMasterEnabled(&c, true)
	if !Enabled(&c) {
		t.Error("master on must enable")
	}
}

func TestResolveByName(t *testing.T) {
	var c Config
	if err := AddUserHook(&c, EventPreToolUse, "", Handler{Name: "my-guard", Command: []string{"/bin/true"}}); err != nil {
		t.Fatal(err)
	}
	if err := AddUserHook(&c, EventPreToolUse, "", Handler{Name: "other", Command: []string{"/bin/false"}}); err != nil {
		t.Fatal(err)
	}
	// Exact name resolves (same discipline as `hooks test`).
	ref, h, err := resolveUserHook(&c, "my-guard")
	if err != nil || h.Name != "my-guard" || ref.event != EventPreToolUse {
		t.Errorf("name resolve = %+v %+v, %v", ref, h, err)
	}
	// Substring shared by both names is ambiguous.
	if _, _, err := resolveUserHook(&c, "r"); err == nil {
		t.Error("shared substring must be ambiguous")
	}
	// Fingerprint prefix still resolves.
	fp := c.PreToolUse[0].Hooks[0].Fingerprint()
	if _, h, err := resolveUserHook(&c, fp[:16]); err != nil || h.Name != "my-guard" {
		t.Errorf("prefix resolve = %+v, %v", h, err)
	}
}
