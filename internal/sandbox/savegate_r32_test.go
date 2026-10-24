// savegate_r32_test.go - R32 write_file/patch reroute tests: confined
// skills writes evaluate the unified save_skill gate (flow/provenance
// legs cannot drift from the static suffix check) while write_file/patch
// still never persist executable skills themselves.
package sandbox

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/permissions"
)

// TestR32WriteFileRerouteGateEvaluated pins the reroute: a confined
// write_file evaluates the unified gate (name/code/description legs
// visible to the gate), a gate denial surfaces, and a passing gate
// still refuses with the save_skill alternative (registry + joint
// binding stay owned by save_skill).
func TestR32WriteFileRerouteGateEvaluated(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	dir := t.TempDir()
	t.Chdir(dir)
	tools := fileOpsTools(t)

	sentinel := errors.New("gate says no")
	var calls int
	var gotName, gotCode, gotDesc string
	saved := SaveSkillGateFunc
	SaveSkillGateFunc = func(ctx context.Context, name, code, description string) error {
		calls++
		gotName, gotCode, gotDesc = name, code, description
		return sentinel
	}
	t.Cleanup(func() { SaveSkillGateFunc = saved })

	if _, err := runFileTool(t, tools, "write_file", map[string]any{
		"path": filepath.Join(dir, "skills", "evil.py"), "content": "print(1)\n",
	}); !errors.Is(err, sentinel) {
		t.Fatalf("confined write must surface the gate denial, got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("gate must evaluate exactly once, got %d", calls)
	}
	if gotName != "evil" || gotCode != "print(1)\n" || gotDesc != "" {
		t.Fatalf("gate legs = (%q, %q, %q), want (evil, content, empty desc)", gotName, gotCode, gotDesc)
	}

	// Passing gate: still refused (save_skill owns the domain), save_skill
	// named, nothing written.
	SaveSkillGateFunc = func(ctx context.Context, name, code, description string) error { return nil }
	if _, err := runFileTool(t, tools, "write_file", map[string]any{
		"path": filepath.Join(dir, "skills", "proven.py"), "content": "print(2)\n",
	}); err == nil {
		t.Fatal("confined write with a passing gate: still want refusal")
	} else if !strings.Contains(err.Error(), "save_skill") {
		t.Fatalf("refusal must name save_skill, got: %v", err)
	}
}

// TestR32PatchRerouteGateEvaluated pins the patch leg: the gate
// evaluates before any read (confined missing file surfaces the gate
// denial, not a read error).
func TestR32PatchRerouteGateEvaluated(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	dir := t.TempDir()
	t.Chdir(dir)
	tools := fileOpsTools(t)

	sentinel := errors.New("patch gate says no")
	saved := SaveSkillGateFunc
	SaveSkillGateFunc = func(ctx context.Context, name, code, description string) error { return sentinel }
	t.Cleanup(func() { SaveSkillGateFunc = saved })

	if _, err := runFileTool(t, tools, "patch", map[string]any{
		"path": filepath.Join(dir, "skills", "missing.py"), "old_string": "a", "new_string": "b",
	}); !errors.Is(err, sentinel) {
		t.Fatalf("confined patch must surface the gate denial, got: %v", err)
	}
}

// TestR32NoHookFallbackClosed pins the unwired fallback: with no gate
// installed (agent unwired), confined writes still refuse via the static
// check (fail-closed either way).
func TestR32NoHookFallbackClosed(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	if SaveSkillGateFunc != nil {
		t.Skip("hook installed by another test in this package run")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	tools := fileOpsTools(t)
	if _, err := runFileTool(t, tools, "write_file", map[string]any{
		"path": filepath.Join(dir, "skills", "evil.py"), "content": "print(1)\n",
	}); err == nil {
		t.Fatal("unwired confined write: want refusal")
	} else if !strings.Contains(err.Error(), "save_skill") {
		t.Fatalf("fallback refusal must name save_skill, got: %v", err)
	}
}
