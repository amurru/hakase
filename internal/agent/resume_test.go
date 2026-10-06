package agent

import (
	"amurru/hakase/internal/config"
	"testing"
	"time"
)

func TestResumeMaxAgeDefault(t *testing.T) {
	saved := deps
	deps = nil
	t.Cleanup(func() { deps = saved })
	if got := ResumeMaxAge(); got != 30*time.Minute {
		t.Errorf("ResumeMaxAge nil deps = %v, want 30m", got)
	}
}

func TestResumeMaxAgeConfigured(t *testing.T) {
	saved := deps
	deps = &Deps{Config: &config.Config{}}
	deps.Config.DurableResume.MaxResumeAgeMinutes = 10
	t.Cleanup(func() { deps = saved })
	if got := ResumeMaxAge(); got != 10*time.Minute {
		t.Errorf("ResumeMaxAge = %v, want 10m", got)
	}
}

// TestGateToolsAreLongRunning pins Phase 4: the gate-hosting tools
// report IsLongRunning so the engine marks their call events with
// LongRunningToolIDs (post-restart resume matching). Behavior is
// otherwise synchronous and unchanged.
func TestGateToolsAreLongRunning(t *testing.T) {
	clarifyT, err := registerClarifyTool()
	if err != nil {
		t.Fatalf("registerClarifyTool: %v", err)
	}
	if !clarifyT.IsLongRunning() {
		t.Error("clarify tool should be long-running (resume marker)")
	}
	pyT, err := createPythonTool(nil)
	if err != nil {
		t.Fatalf("createPythonTool: %v", err)
	}
	if !pyT.IsLongRunning() {
		t.Error("python_interpreter tool should be long-running (resume marker)")
	}
}
