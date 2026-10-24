// pip_replace_env_test.go - pip choke replace-env pin (S22 two-layer).
//
// The production pipChokeBuild must pass the scrubbed S20 child env
// (pipEnvMap) as ExecReplaceEnv, so the pip child inherits no ambient
// env and no PYTHONPATH-family key survives the merge scrub.
package agent

import (
	"context"
	"strings"
	"testing"

	"amurru/hakase/internal/sandbox"
)

// TestPipEnvMapScrubbed pins pipEnvMap: PATH present, no
// PYTHONPATH-family keys emitted, so the replace set is clean at rest.
func TestPipEnvMapScrubbed(t *testing.T) {
	t.Setenv("PYTHONPATH", "/evil-ambient")
	m := pipEnvMap("/venv/bin/python")
	if _, ok := m["PATH"]; !ok {
		t.Fatalf("pipEnvMap missing PATH: %v", m)
	}
	for _, bad := range []string{"PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "__PYVENV_LAUNCHER__"} {
		if _, ok := m[bad]; ok {
			t.Errorf("pipEnvMap carries %q, want absent", bad)
		}
	}
}

// TestPipChokeBuildReplaceEnv pins the production builder end to end:
// ambient env (including a poisoned PYTHONPATH) does not reach the pip
// child, and the S20 map keys do.
func TestPipChokeBuildReplaceEnv(t *testing.T) {
	savedGate, savedAudit := sandbox.EvaluateCommandFunc, sandbox.AuditCommandFunc
	sandbox.EvaluateCommandFunc = func(sb *sandbox.SandboxConfig, command string, args []string) sandbox.GateDecision {
		return sandbox.GateDecision{Action: sandbox.ActionAllow, Risk: sandbox.RiskLow}
	}
	sandbox.AuditCommandFunc = func(sandbox.CommandAuditEntry) {}
	t.Cleanup(func() { sandbox.EvaluateCommandFunc, sandbox.AuditCommandFunc = savedGate, savedAudit })

	t.Setenv("S22_TEST_PIP_AMBIENT_LEAK", "leaked")
	t.Setenv("PYTHONPATH", "/evil-ambient")

	env := pipEnvMap("/venv/bin/python")
	cmd, err := pipChokeBuild(context.Background(), "/venv/bin/pip", []string{"install", "pkg"}, t.TempDir(), env)
	if err != nil {
		t.Fatalf("pipChokeBuild: %v", err)
	}
	joined := strings.Join(cmd.Env, "\x00")
	if strings.Contains(joined, "S22_TEST_PIP_AMBIENT_LEAK") {
		t.Errorf("pip child inherited ambient leak: %v", cmd.Env)
	}
	for _, bad := range []string{"PYTHONPATH=", "PYTHONHOME=", "PYTHONSTARTUP=", "__PYVENV_LAUNCHER__="} {
		if strings.Contains(joined, bad) {
			t.Errorf("pip child kept %q, want scrubbed: %v", bad, cmd.Env)
		}
	}
	if !strings.Contains(joined, "PATH=") {
		t.Errorf("pip child missing PATH: %v", cmd.Env)
	}
}
