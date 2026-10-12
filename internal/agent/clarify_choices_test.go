// clarify_choices_test.go - regression tests for provider-serialized
// clarify choices.
//
// Some providers serialize the choices array as a JSON string instead
// of an array. ADK validates raw args against the inferred schema
// before the handler runs, so the call used to fail with:
//
//	validating /properties/choices: ... has type "string",
//	want one of "null, array"
//
// ClarifyInput.Choices is a util.StringSlice (array-or-string) with a
// widened schema, so the string form decodes and the question proceeds.
// These tests drive the real tool: raw arg maps through Run with a
// stub gate, asserting the gate sees decoded choices and the answer
// flows back.
package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"google.golang.org/genai"

	adkagent "google.golang.org/adk/v2/agent"

	"amurru/hakase/internal/interfaces"
)

// runnableTool mirrors ADK's unexported runnable-tool surface
// (Declaration + Run) so tests can drive the real validation path.
type runnableTool interface {
	Declaration() *genai.FunctionDeclaration
	Run(ctx adkagent.Context, args any) (result map[string]any, err error)
}

// runClarifyTool invokes the registered clarify tool with raw args and
// a stub gate answering ans, returning the gate's request and the
// tool's result payload.
func runClarifyTool(t *testing.T, args map[string]any, ans []string) (interfaces.ClarifyRequest, map[string]any) {
	t.Helper()
	saved := rt
	rt = &Runtime{}
	var captured interfaces.ClarifyRequest
	rt.SetClarifyGate(&mockClarifyGate{
		askFunc: func(req interfaces.ClarifyRequest) (interfaces.ClarifyResponse, error) {
			captured = req
			return interfaces.ClarifyResponse{Answer: ans}, nil
		},
	})
	t.Cleanup(func() { rt = saved })

	clarifyT, err := registerClarifyTool()
	if err != nil {
		t.Fatalf("registerClarifyTool: %v", err)
	}
	res, err := clarifyT.(runnableTool).Run(&stubContext{}, args)
	if err != nil {
		t.Fatalf("Run(%v): %v", args, err)
	}
	return captured, res
}

// TestClarifyToolAcceptsStringifiedChoices reproduces the reported
// failure: 8 choices as one JSON-encoded string must decode and reach
// the gate instead of failing schema validation.
func TestClarifyToolAcceptsStringifiedChoices(t *testing.T) {
	choices := []string{
		"ODE (ordinary) — initial value problem (e.g., physics simulation, population dynamics)",
		"ODE — boundary value problem",
		"PDE (partial) — time-dependent (heat, wave, fluid dynamics)",
		"Stiff system (widely varying timescales)",
		"Differential-algebraic equations (DAEs)",
		"Parameter estimation / inverse problem (fitting ODE params to data)",
		"Real-time / embedded (fixed-step, limited compute)",
		"Symbolic/analytical solution (closed form)",
	}
	raw, _ := json.Marshal(choices)
	req, res := runClarifyTool(t,
		map[string]any{"question": "Which solver?", "choices": string(raw)},
		[]string{choices[0]})
	if !reflect.DeepEqual(req.Choices, choices) {
		t.Fatalf("gate choices = %q, want %q", req.Choices, choices)
	}
	if got, _ := res["user_response"].([]any); len(got) != 1 || got[0] != choices[0] {
		t.Fatalf("user_response = %v, want [%q]", res["user_response"], choices[0])
	}
}

// TestClarifyToolChoicesForms pins the remaining shapes: a real array
// still works, omission stays open-ended, and a bare string becomes
// one choice (never comma-split).
func TestClarifyToolChoicesForms(t *testing.T) {
	t.Run("array", func(t *testing.T) {
		req, _ := runClarifyTool(t,
			map[string]any{"question": "Pick?", "choices": []any{"A", "B"}},
			[]string{"A"})
		if !reflect.DeepEqual(req.Choices, []string{"A", "B"}) {
			t.Fatalf("gate choices = %q, want [A B]", req.Choices)
		}
	})
	t.Run("omitted", func(t *testing.T) {
		req, _ := runClarifyTool(t, map[string]any{"question": "Say?"}, []string{"hi"})
		if len(req.Choices) != 0 {
			t.Fatalf("gate choices = %q, want empty", req.Choices)
		}
	})
	t.Run("bare string", func(t *testing.T) {
		req, _ := runClarifyTool(t,
			map[string]any{"question": "Pick?", "choices": "yes, no"},
			[]string{"yes, no"})
		if !reflect.DeepEqual(req.Choices, []string{"yes, no"}) {
			t.Fatalf("gate choices = %q, want single choice", req.Choices)
		}
	})
}
