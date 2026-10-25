// s16_test.go - save_skill code-source allowlist tests (S16).
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"

	"google.golang.org/adk/v2/agent"
)

// runSaveSkill drives the save_skill tool like the ADK would, returning the
// decoded output. The mock invocation context carries no task session, so
// provenance resolves under the empty session ("") - which fails closed
// (M6): use runSaveSkillWithSession for allow paths.
func runSaveSkill(t *testing.T, name, desc, code string) (SaveSkillOutput, error) {
	t.Helper()
	return runSaveSkillWithCtx(t, agent.NewContext(&agent.ContextMock{}), name, desc, code)
}

// runSaveSkillWithSession drives save_skill under a session-bound ctx
// (taskID -> sessionID). M6: allow paths must bind a non-empty session;
// the empty ctx above always fails provenance. Reuses the flowWiringCtx
// pattern (ADK session ID = taskID, registered to the hakase session)
// so SessionIDFromCtx resolves inside the tool handler.
func runSaveSkillWithSession(t *testing.T, taskID, sessionID, name, desc, code string) (SaveSkillOutput, error) {
	t.Helper()
	interfaces.RegisterTaskSession(taskID, sessionID)
	t.Cleanup(func() { interfaces.UnregisterTask(taskID) })
	ctx := agent.NewContext(&flowWiringCtx{sess: flowWiringSession{id: taskID}})
	return runSaveSkillWithCtx(t, ctx, name, desc, code)
}

func runSaveSkillWithCtx(t *testing.T, ctx agent.Context, name, desc, code string) (SaveSkillOutput, error) {
	t.Helper()
	tool, err := createSaveSkillTool()
	if err != nil {
		t.Fatalf("createSaveSkillTool: %v", err)
	}
	runnable, ok := tool.(interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("tool %T does not expose Run", tool)
	}
	result, err := runnable.Run(ctx, map[string]any{
		"name": name, "description": desc, "code": code,
	})
	if err != nil {
		return SaveSkillOutput{}, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out SaveSkillOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out, nil
}

// TestS16SaveSkillRequiresProvenance pins the code-source gate: saving code
// with no prior python execution record in the session is refused (before
// any file or registry mutation), and saving with a recorded hash writes
// the file.
func TestS16SaveSkillRequiresProvenance(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	dir := t.TempDir()
	t.Chdir(dir)
	code := "X = 1\nprint(X)\n"

	if _, err := runSaveSkill(t, "proven_skill", "d", code); err == nil {
		t.Fatal("save without provenance: want refusal")
	} else if !strings.Contains(err.Error(), "prior python_interpreter") {
		t.Fatalf("refusal must name the provenance rule, got: %v", err)
	}
	// ADR condition 9: the refusal leaves no file and no registry delta.
	if _, serr := os.Stat(filepath.Join(dir, "skills", "proven_skill.py")); !os.IsNotExist(serr) {
		t.Fatal("refused save must not write the skill file")
	}
	if _, serr := os.Stat(filepath.Join(dir, "skills", "skills.json")); !os.IsNotExist(serr) {
		t.Fatal("refused save must not create a registry")
	}

	recordSkillProvenance("sess-s16-ok", skillCodeHash(code))
	out, err := runSaveSkillWithSession(t, "task-s16-ok", "sess-s16-ok", "proven_skill", "d", code)
	if err != nil {
		t.Fatalf("save with recorded hash: %v", err)
	}
	if out.FilePath == "" {
		t.Fatal("save with recorded hash must return a file path")
	}
	if _, serr := os.Stat(filepath.Join(dir, "skills", "proven_skill.py")); serr != nil {
		t.Fatalf("recorded save must write the file: %v", serr)
	}
}

// TestS16SaveSkillCrossSessionDenied pins session scoping: provenance
// recorded in session A never authorizes session B.
func TestS16SaveSkillCrossSessionDenied(t *testing.T) {
	permissions.Install(nil)
	t.Cleanup(func() { permissions.Install(nil) })
	code := "Y = 2\n"
	recordSkillProvenance("sess-s16-A", skillCodeHash(code))
	ctxB := flowSessionCtx(t, "task-s16-B", "sess-s16-B")
	if err := checkSaveSkillAllowed(ctxB, "cross_skill", code, "d"); err == nil {
		t.Fatal("cross-session save: want refusal")
	} else if !strings.Contains(err.Error(), "prior python_interpreter") {
		t.Fatalf("cross-session refusal must name the provenance rule, got: %v", err)
	}
}

// TestS16SaveSkillDenyWins pins deny-wins (ADR condition 4): a flow Deny
// for save_skill refuses even with valid provenance.
func TestS16SaveSkillDenyWins(t *testing.T) {
	installFlowPolicy(t, permissions.Policy{Version: 2, Default: "allow", Rules: []permissions.Rule{
		{Action: "save_skill", Resource: "*", Effect: permissions.EffectDeny},
	}})
	code := "Z = 3\n"
	sess := "sess-s16-deny"
	recordSkillProvenance(sess, skillCodeHash(code))
	ctx := flowSessionCtx(t, "task-s16-deny", sess)
	if err := checkSaveSkillAllowed(ctx, "deny_skill", code, "d"); err == nil {
		t.Fatal("flow Deny with valid provenance: want refusal")
	} else if !strings.Contains(err.Error(), "blocked by flow guard") {
		t.Fatalf("deny-wins refusal must name the flow guard, got: %v", err)
	}
}
