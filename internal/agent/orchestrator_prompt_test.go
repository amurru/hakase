package agent

import (
	"strings"
	"testing"

	"amurru/hakase/internal/config"
)

// TestOrchestratorDoesNotMandatePreAnswerLookups pins the turn-count property.
//
// The orchestrator used to require two knowledge lookups "at the start of a
// session, before planning", plus a list_tasks preamble, before it would
// answer anything. Every tool call is a full model round trip, so a one-line
// question cost several turns of pure overhead. These lookups are now
// reactive - taken when relevant - not mandatory.
func TestOrchestratorDoesNotMandatePreAnswerLookups(t *testing.T) {
	instruction := buildOrchestratorInstruction("", &config.Config{})

	// Phrasings that made a lookup unconditional.
	banned := []string{
		"before planning",
		"at the start of a session, before planning",
		"use 'list_tasks' to review your plan",
		"before answering about a topic you have notes on",
		"BEFORE searching the filesystem",
	}
	for _, phrase := range banned {
		if strings.Contains(instruction, phrase) {
			t.Errorf("instruction still mandates a pre-answer lookup: %q", phrase)
		}
	}

	// The capabilities themselves must survive - we are removing the
	// obligation to call them, not the ability or the guidance.
	required := []string{
		"lessons-learned",
		"search_knowledge",
		"recall_knowledge",
		"list_tasks",
		"ARTIFACT LOCATION",
		"TASK BOARD",
	}
	for _, phrase := range required {
		if !strings.Contains(instruction, phrase) {
			t.Errorf("instruction lost required guidance: %q", phrase)
		}
	}
}

// TestOrchestratorInstructionIsNotBloated guards the prefill cost. The
// instruction is the prefix of every request, so its size is a per-turn tax
// on every single call.
func TestOrchestratorInstructionIsNotBloated(t *testing.T) {
	const maxBytes = 12 << 10 // 12 KiB, excluding the skills block
	instruction := buildOrchestratorInstruction("", &config.Config{})
	if len(instruction) > maxBytes {
		t.Errorf("orchestrator instruction (no skills) is %d bytes, over the %d budget; every turn pays this", len(instruction), maxBytes)
	}
}
