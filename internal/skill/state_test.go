package skill

import (
	"os"
	"path/filepath"
	"testing"
)

// setupStateTest redirects HAKASE_HOME to a fresh temp dir so the persisted
// skill state never touches the real user home.
func setupStateTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HAKASE_HOME", home)
	return home
}

func TestSetSkillDisabledRoundTrip(t *testing.T) {
	home := setupStateTest(t)

	if IsSkillDisabled(KindPython, "foo") {
		t.Fatal("fresh state: foo must not be disabled")
	}

	if err := SetSkillDisabled(KindPython, "foo", true); err != nil {
		t.Fatalf("SetSkillDisabled(true): %v", err)
	}
	if !IsSkillDisabled(KindPython, "foo") {
		t.Fatal("after disable, foo must be disabled")
	}
	if IsSkillDisabled(KindMarkdown, "foo") {
		t.Fatal("same name markdown skill must remain enabled")
	}
	if IsSkillDisabled(KindPython, "bar") {
		t.Fatal("bar must remain enabled")
	}

	// Idempotent disable.
	if err := SetSkillDisabled(KindPython, "foo", true); err != nil {
		t.Fatalf("SetSkillDisabled(true) again: %v", err)
	}
	if !IsSkillDisabled(KindPython, "foo") {
		t.Fatal("foo must still be disabled after duplicate disable")
	}

	if err := SetSkillDisabled(KindPython, "foo", false); err != nil {
		t.Fatalf("SetSkillDisabled(false): %v", err)
	}
	if IsSkillDisabled(KindPython, "foo") {
		t.Fatal("after enable, foo must not be disabled")
	}
	if len(DisabledSkillsSet()) != 0 {
		t.Fatalf("expected empty disabled set, got %v", DisabledSkillsSet())
	}

	// The state file must exist and contain a valid registry.
	data, err := os.ReadFile(filepath.Join(home, "skill-state.json"))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("state file must not be empty")
	}
}

func TestDisabledSkillsSetMultiple(t *testing.T) {
	setupStateTest(t)

	keys := []struct{ kind, name string }{
		{KindPython, "a"},
		{KindMarkdown, "b"},
		{KindPython, "c"},
	}
	for _, k := range keys {
		if err := SetSkillDisabled(k.kind, k.name, true); err != nil {
			t.Fatalf("disable %s:%s: %v", k.kind, k.name, err)
		}
	}
	set := DisabledSkillsSet()
	if len(set) != 3 {
		t.Fatalf("expected 3 disabled, got %v", set)
	}
	for _, k := range keys {
		if !set[SkillKey(k.kind, k.name)] {
			t.Errorf("%s:%s missing from disabled set %v", k.kind, k.name, set)
		}
	}

	if err := SetSkillDisabled(KindPython, "b", false); err != nil {
		t.Fatalf("enable python:b: %v", err)
	}
	set = DisabledSkillsSet()
	if set[SkillKey(KindPython, "b")] {
		t.Errorf("python:b must not be disabled after enable: %v", set)
	}
	if !set[SkillKey(KindMarkdown, "b")] {
		t.Errorf("markdown:b must remain disabled (distinct key): %v", set)
	}
	if !set[SkillKey(KindPython, "a")] || !set[SkillKey(KindPython, "c")] {
		t.Errorf("a and c must remain disabled: %v", set)
	}
}

func TestSkillStateMissingFile(t *testing.T) {
	setupStateTest(t)

	if IsSkillDisabled(KindPython, "anything") {
		t.Fatal("missing state file must report not disabled")
	}
	if len(DisabledSkillsSet()) != 0 {
		t.Fatalf("missing state file must yield empty set, got %v", DisabledSkillsSet())
	}
}

func TestSkillStateCorruptFile(t *testing.T) {
	home := setupStateTest(t)

	if err := os.WriteFile(filepath.Join(home, "skill-state.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	// Corrupt state degrades to "nothing disabled", never an error.
	if IsSkillDisabled(KindPython, "x") {
		t.Fatal("corrupt state must report not disabled")
	}
	if len(DisabledSkillsSet()) != 0 {
		t.Fatalf("corrupt state must yield empty set, got %v", DisabledSkillsSet())
	}

	// A write must repair the file.
	if err := SetSkillDisabled(KindPython, "x", true); err != nil {
		t.Fatalf("SetSkillDisabled after corrupt state: %v", err)
	}
	if !IsSkillDisabled(KindPython, "x") {
		t.Fatal("x must be disabled after repair write")
	}
}