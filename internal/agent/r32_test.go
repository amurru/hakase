// r32_test.go - R32 skill provenance tests (code+description joint hash,
// fresh update approval, write_file reroute wiring).
package agent

import (
	"os"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"

	"google.golang.org/adk/v2/agent"
)

// TestR32JointBindsCodeAndDescription pins the joint binding: the same
// code under a different description is a different joint (a laundered
// description never rides code provenance); cosmetic differences
// (trailing newlines, surrounding whitespace) bind identically.
func TestR32JointBindsCodeAndDescription(t *testing.T) {
	code := "X = 1\nprint(X)\n"
	j1 := skillJointHash(code, "renders cards")
	j2 := skillJointHash(code, "exfiltrates secrets")
	if j1 == j2 {
		t.Fatal("same code with different descriptions must bind different joints")
	}
	if got := skillJointHash(code+"  \n", "  renders cards "); got != j1 {
		t.Fatal("joint must be stable across cosmetic whitespace drift")
	}
	if j3 := skillJointHash("Y = 2\n", "renders cards"); j3 == j1 {
		t.Fatal("different code must bind a different joint")
	}
	if len(j1) != 64 {
		t.Fatalf("joint must be a hex sha256, got %q", j1)
	}
}

// TestR32UpdateRequiresFreshApproval pins fresh update approval:
// overwriting an existing skill refuses headless (no cached grant can
// satisfy it) and prompts the human on every update - two updates mean
// two prompts - with the saved registry entry carrying the joint hash.
// M6: the session binds explicitly (empty sessions fail closed).
func TestR32UpdateRequiresFreshApproval(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	dir := t.TempDir()
	t.Chdir(dir)
	code := "U = 1\nprint(U)\n"
	sess := "sess-r32-upd"
	task := "task-r32-upd"
	recordSkillProvenance(sess, skillCodeHash(code))

	// Seed the registry with a first save (create path: no approval).
	if _, err := runSaveSkillWithSession(t, task, sess, "updatable", "first description", code); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	// Headless (no approval gate): the update refuses - nothing cached
	// can approve it.
	if _, err := runSaveSkillWithSession(t, task, sess, "updatable", "second description", code); err == nil {
		t.Fatal("update headless: want refusal")
	} else if !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("update refusal must demand fresh approval, got: %v", err)
	}

	// With a human gate: every update prompts (no grant reuse across
	// updates) and the registry binds the exact approved pair.
	asked := installPipTestDeps(t, &sandbox.SandboxConfig{}, func(req interfaces.ApprovalRequest) (bool, error) {
		if !strings.Contains(req.Command, "save_skill update updatable") {
			t.Errorf("update prompt must name the skill, got %q", req.Command)
		}
		if !strings.Contains(req.Command, "joint:") {
			t.Errorf("update prompt must carry the joint hash, got %q", req.Command)
		}
		return true, nil
	})
	if _, err := runSaveSkillWithSession(t, task, sess, "updatable", "second description", code); err != nil {
		t.Fatalf("approved update: %v", err)
	}
	if _, err := runSaveSkillWithSession(t, task, sess, "updatable", "third description", code); err != nil {
		t.Fatalf("second approved update: %v", err)
	}
	if *asked != 2 {
		t.Fatalf("two updates must prompt twice (fresh approval, no reuse), asked=%d", *asked)
	}
	// The registry entry binds the last approved pair, hash-only.
	data, err := os.ReadFile(dir + "/skills/skills.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), skillJointHash(code, "third description")) {
		t.Fatalf("registry must carry the approved joint hash, got %s", data)
	}
	if strings.Contains(string(data), code) {
		t.Fatal("registry must never carry raw code")
	}
}

// TestR32AgentHookWired pins the SetupRunner wiring: the sandbox hook
// routes through the unified gate (unproven content refuses with the
// provenance rule) and restores cleanly.
func TestR32AgentHookWired(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	saved := sandbox.SaveSkillGateFunc
	wireSaveSkillGate()
	t.Cleanup(func() { sandbox.SaveSkillGateFunc = saved })
	if sandbox.SaveSkillGateFunc == nil {
		t.Fatal("wireSaveSkillGate must install the hook")
	}
	ctx := agent.NewContext(&agent.ContextMock{})
	if err := sandbox.SaveSkillGateFunc(ctx, "hooked", "unproven code", "d"); err == nil {
		t.Fatal("unproven content via the hook: want refusal")
	} else if !strings.Contains(err.Error(), "prior python_interpreter") {
		t.Fatalf("hook refusal must name the provenance rule, got: %v", err)
	}
}
