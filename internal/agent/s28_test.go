// s28_test.go - S28 deny-first instruction tests.
package agent

import (
	"strings"
	"testing"

	"amurru/hakase/internal/config"
)

// TestS28NoBypassInstruction pins the :2182 close: the orchestrator
// instruction never tells the model to route around a write_file denial
// via system_exec (deny-first). A blocked write stops; the model asks
// the user instead of bypassing.
func TestS28NoBypassInstruction(t *testing.T) {
	if strings.Contains(markdownSkillsInstruction, "if 'write_file' is blocked") {
		t.Fatal("instruction must not invite a write_file bypass")
	}
	if strings.Contains(markdownSkillsInstruction, "Use 'system_exec' to create the files") {
		t.Fatal("instruction must not route writes around through system_exec")
	}
	for _, want := range []string{
		"Never use 'system_exec'",
		"deny-first",
		"stop and ask the user",
	} {
		if !strings.Contains(markdownSkillsInstruction, want) {
			t.Fatalf("instruction must contain %q", want)
		}
	}
	// The composed prompt carries the same wording (no drift between the
	// const and what the model sees).
	if got := buildOrchestratorInstruction("", &config.Config{}); !strings.Contains(got, "deny-first") {
		t.Fatal("composed orchestrator instruction must carry deny-first")
	}
}
