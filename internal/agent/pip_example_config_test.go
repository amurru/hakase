// pip_example_config_test.go - High 2: pip choke vs allowed_commands.
//
// The shipped config.json.example sets
// allowed_commands=[ls cat grep head tail echo git]. The pip choke build
// goes through the same BuildExecCommandFor gate, so without a
// pre-authorization it was denied outright ("pip is not in the
// allowlist"). This test pins the fix end to end with the real gate
// (no stubbed builder, no stubbed evaluator): the S18-carried
// pre-approval exempts only the allowlist leg and the known-risk
// re-prompt, while hard denies, deny patterns, and the path audit
// still apply.
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
	"amurru/hakase/internal/sandbox"
)

// TestPipChokeBuildExampleConfigRealGate builds a pip install through the
// production choke builder under the example allowed_commands config with
// the real evaluator wired: it must succeed with no second prompt, a
// pre-approved audit entry, and a scrubbed child env.
func TestPipChokeBuildExampleConfigRealGate(t *testing.T) {
	dir := t.TempDir()
	venvBin := filepath.Join(dir, ".venv", "bin")
	if err := os.MkdirAll(venvBin, 0755); err != nil {
		t.Fatalf("mkdir venv fixture: %v", err)
	}
	pipBin := filepath.Join(venvBin, "pip")
	if err := os.WriteFile(pipBin, []byte("# fixture\n"), 0755); err != nil {
		t.Fatalf("write pip fixture: %v", err)
	}

	sb := sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{
		Mode:            "paths",
		WorkspaceRoots:  []string{dir},
		AllowPipInstall: true,
		PipAllowlist:    []string{"requests"},
		// Mirrors config.json.example: pip is deliberately absent.
		AllowedCommands: []string{"ls", "cat", "grep", "head", "tail", "echo", "git"},
	})

	savedWithLabel, savedGate := sandbox.EvaluateCommandWithLabelFunc, sandbox.EvaluateCommandFunc
	// Real gate: the agent evaluator behind the production adapter
	// (cmd/hakase/init.go), converting agent to sandbox decisions.
	sandbox.EvaluateCommandWithLabelFunc = func(sb *sandbox.SandboxConfig, command string, args []string, label *permissions.SessionLabel) sandbox.GateDecision {
		d := EvaluateCommandWithLabel(sb, command, args, label)
		return sandbox.GateDecision{
			Action:     sandbox.GateAction(d.Action),
			Risk:       sandbox.CommandRisk(d.Risk),
			Reason:     d.Reason,
			PolicyRule: d.PolicyRule,
		}
	}
	t.Cleanup(func() {
		sandbox.EvaluateCommandWithLabelFunc = savedWithLabel
		sandbox.EvaluateCommandFunc = savedGate
	})
	var audits []sandbox.CommandAuditEntry
	savedAudit := sandbox.AuditCommandFunc
	sandbox.AuditCommandFunc = func(e sandbox.CommandAuditEntry) { audits = append(audits, e) }
	t.Cleanup(func() { sandbox.AuditCommandFunc = savedAudit })
	prompted := false
	savedApprove := sandbox.ApproveFunc
	sandbox.ApproveFunc = func(ctx context.Context, req interfaces.ApprovalRequest) (bool, error) {
		prompted = true
		return false, nil
	}
	t.Cleanup(func() { sandbox.ApproveFunc = savedApprove })

	t.Setenv("S22_EXAMPLE_AMBIENT_LEAK", "leaked")
	t.Setenv("PYTHONPATH", "/evil-ambient")

	ctx := sandbox.WithConfig(context.Background(), sb)
	cmd, err := pipChokeBuild(ctx, pipBin, []string{"install", "requests"}, dir, pipEnvMap("/venv/bin/python"))
	if err != nil {
		t.Fatalf("pip choke build under example config: %v", err)
	}
	if cmd == nil {
		t.Fatal("pip choke build returned nil cmd")
	}
	if prompted {
		t.Error("pre-approved pip build must not prompt the operator again")
	}
	approved := false
	for _, a := range audits {
		if a.Decision == "approved" && strings.Contains(a.Reason, "pre-approved") {
			approved = true
		}
	}
	if !approved {
		t.Errorf("want a pre-approved audit entry, got %+v", audits)
	}
	joined := strings.Join(cmd.Env, "\x00")
	if strings.Contains(joined, "S22_EXAMPLE_AMBIENT_LEAK") {
		t.Error("pip child inherited ambient leak")
	}
	for _, bad := range []string{"PYTHONPATH=", "PYTHONHOME=", "PYTHONSTARTUP=", "__PYVENV_LAUNCHER__="} {
		if strings.Contains(joined, bad) {
			t.Errorf("pip child kept %q, want scrubbed", bad)
		}
	}
	pathCount := 0
	for _, kv := range strings.Split(joined, "\x00") {
		if k, _, ok := strings.Cut(kv, "="); ok && k == "PATH" {
			pathCount++
		}
	}
	if pathCount != 1 {
		t.Errorf("pip child env must carry PATH exactly once, got %d in %v", pathCount, cmd.Env)
	}
}

// TestPipChokeBuildExampleConfigDeniedWithoutPreApproval is the control
// for the High 2 fix: the same example-config build WITHOUT the carried
// S18 pre-approval is denied at the allowlist leg, proving the real gate
// (not a stub) decides and the exemption is what admits.
func TestPipChokeBuildExampleConfigDeniedWithoutPreApproval(t *testing.T) {
	dir := t.TempDir()
	venvBin := filepath.Join(dir, ".venv", "bin")
	if err := os.MkdirAll(venvBin, 0755); err != nil {
		t.Fatalf("mkdir venv fixture: %v", err)
	}
	pipBin := filepath.Join(venvBin, "pip")
	if err := os.WriteFile(pipBin, []byte("# fixture\n"), 0755); err != nil {
		t.Fatalf("write pip fixture: %v", err)
	}
	sb := sandbox.LoadSandboxConfig(&sandbox.SandboxJSON{
		Mode:            "paths",
		WorkspaceRoots:  []string{dir},
		AllowPipInstall: true,
		PipAllowlist:    []string{"requests"},
		AllowedCommands: []string{"ls", "cat", "grep", "head", "tail", "echo", "git"},
	})
	savedWithLabel := sandbox.EvaluateCommandWithLabelFunc
	sandbox.EvaluateCommandWithLabelFunc = func(sb *sandbox.SandboxConfig, command string, args []string, label *permissions.SessionLabel) sandbox.GateDecision {
		d := EvaluateCommandWithLabel(sb, command, args, label)
		return sandbox.GateDecision{
			Action:     sandbox.GateAction(d.Action),
			Risk:       sandbox.CommandRisk(d.Risk),
			Reason:     d.Reason,
			PolicyRule: d.PolicyRule,
		}
	}
	t.Cleanup(func() { sandbox.EvaluateCommandWithLabelFunc = savedWithLabel })
	savedAudit := sandbox.AuditCommandFunc
	sandbox.AuditCommandFunc = func(sandbox.CommandAuditEntry) {}
	t.Cleanup(func() { sandbox.AuditCommandFunc = savedAudit })

	ctx := sandbox.WithConfig(context.Background(), sb)
	_, err := sandbox.BuildExecCommandFor(ctx, pipBin, []string{"install", "requests"}, dir,
		pipEnvMap("/venv/bin/python"), sandbox.ExecPipInstall(), sandbox.ExecReplaceEnv(pipEnvMap("/venv/bin/python")))
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("unapproved pip under example config must die at the allowlist leg, got: %v", err)
	}
}
