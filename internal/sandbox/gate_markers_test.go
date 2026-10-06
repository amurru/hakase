// gate_markers_test.go - pins durable-resume Phase 4: the tools whose
// handlers can block on an approval gate report IsLongRunning so the
// engine marks their call events with LongRunningToolIDs (post-restart
// resume matching). Read-only companions stay unmarked.
package sandbox

import (
	"testing"
)

func TestGateHostingToolsAreLongRunning(t *testing.T) {
	tools, err := CreateSystemExecTools(nil, nil, "")
	if err != nil {
		t.Fatalf("CreateSystemExecTools: %v", err)
	}
	byName := make(map[string]bool)
	for _, tl := range tools {
		byName[tl.Name()] = tl.IsLongRunning()
	}
	for _, name := range []string{"system_exec", "system_exec_start"} {
		v, ok := byName[name]
		if !ok {
			t.Errorf("tool %q not found", name)
			continue
		}
		if !v {
			t.Errorf("tool %q should be long-running (resume marker)", name)
		}
	}
	for _, name := range []string{"system_exec_status", "system_exec_kill", "system_exec_list"} {
		if v, ok := byName[name]; ok && v {
			t.Errorf("tool %q should NOT be long-running (never gates)", name)
		}
	}
}
