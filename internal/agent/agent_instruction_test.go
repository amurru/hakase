// agent_instruction_test.go - assertion tests for the P1-5, P1-6, and P2-4
// instruction rewrites in agent.go. These verify that the orchestrator and
// researcher system prompts contain the required guidance text and no longer
// contain the ambiguous "Use 'web_researcher'" phrasing that caused the model
// to call sub-agent names as tools.
package agent

import (
	"strings"
	"testing"
)

// TestOrchestratorInstructionContainsSubAgentGuidance verifies that
// buildOrchestratorInstruction explicitly tells the model that
// web_researcher/code_interpreter/general_purpose are sub-agents (not tools)
// and that it must use delegate_task/transfer_to_agent to invoke them.
func TestOrchestratorInstructionContainsSubAgentGuidance(t *testing.T) {
	instruction := buildOrchestratorInstruction("")

	for _, want := range []string{
		"delegate_task",
		"agent_name",
		"transfer_to_agent",
		"ARTIFACT LOCATION",
	} {
		if !strings.Contains(instruction, want) {
			t.Errorf("orchestrator instruction missing %q\ninstruction:\n%s", want, instruction)
		}
	}

	// The old ambiguous phrasing "Use 'web_researcher'" must be gone.
	if strings.Contains(instruction, "Use 'web_researcher'") {
		t.Errorf("orchestrator instruction still contains the ambiguous \"Use 'web_researcher'\" phrasing\ninstruction:\n%s", instruction)
	}
}

// TestHakaseSystemInstructionContainsResearchQuality verifies that the
// researcher system instruction includes the RESEARCH QUALITY section
// added by P2-4.
func TestHakaseSystemInstructionContainsResearchQuality(t *testing.T) {
	for _, want := range []string{
		"RESEARCH QUALITY",
		"retrieval date",
	} {
		if !strings.Contains(HakaseSystemInstruction, want) {
			t.Errorf("HakaseSystemInstruction missing %q", want)
		}
	}
}
