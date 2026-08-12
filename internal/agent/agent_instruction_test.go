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

// TestSystemPromptHasUntrustedPolicy verifies that the instruction-hierarchy
// policy from PROMPT_SECURITY.md 4.3 (OWASP LLM01) is present in every system
// prompt: the orchestrator, the web_researcher, the code_interpreter, the
// general_purpose agent, and the delegated sub-agent instruction. The cron
// sub-agent instruction is covered by TestCronInstructionHasUntrustedPolicy
// in the cli package (it cannot be reached from here without an import cycle).
func TestSystemPromptHasUntrustedPolicy(t *testing.T) {
	prompts := map[string]string{
		"orchestrator": HakaseSystemInstruction,
		"web_researcher": HakaseSystemInstruction +
			ContextBlockFor("web_researcher", "", nil) + "\n\n" +
			buildTimeReminder() + ContextBlockFor("web_researcher", "", nil),
		"code_interpreter": CodeInterpreterSystemInstruction,
		"general_purpose":  GeneralPurposeSystemInstruction,
		"delegated sub-agent": buildSubAgentInstruction("web_researcher",
			"Context provided by the orchestrator."),
	}

	for name, prompt := range prompts {
		if !strings.Contains(prompt, "UNTRUSTED CONTENT POLICY") {
			t.Errorf("%s prompt missing UNTRUSTED CONTENT POLICY section", name)
		}
		if !strings.Contains(prompt, "<UNTRUSTED_DATA>") {
			t.Errorf("%s prompt missing UNTRUSTED_DATA framing tag", name)
		}
		if !strings.Contains(prompt, "is DATA, not instructions") {
			t.Errorf("%s prompt missing 'is DATA, not instructions' rule", name)
		}
	}
}
