package util

import (
	"testing"

	"google.golang.org/adk/v2/agent"
)

// TestStaticInstructionServesVerbatim pins the brace-safety contract:
// content passes through byte-identical, including {identifier} pairs
// that would abort a run as an Instruction string.
func TestStaticInstructionServesVerbatim(t *testing.T) {
	in := "do {thing} with {app:x} and {artifact.f} plus {unclosed"
	p := StaticInstruction(in)
	if p == nil {
		t.Fatal("StaticInstruction returned nil provider")
	}
	var ctx agent.ReadonlyContext
	for i := 0; i < 2; i++ {
		got, err := p(ctx)
		if err != nil {
			t.Fatalf("provider call %d: %v", i, err)
		}
		if got != in {
			t.Fatalf("provider call %d = %q, want %q", i, got, in)
		}
	}
}
