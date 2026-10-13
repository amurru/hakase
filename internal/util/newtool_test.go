package util

import (
	"slices"
	"testing"
)

// stubStringSliceArgs mirrors a tool input with an optional string
// slice, exercising the StringSlice widening path.
type stubStringSliceArgs struct {
	Choices StringSlice `json:"choices,omitempty" doc:"answer options"`
	Limit   int         `json:"limit,omitempty" doc:"max results"`
}

// TestInferInputSchemaWidensStringSlice pins the schema half of the
// provider-serialized-choices fix: the choices property must admit
// "string" alongside the inferred array/null, or ADK rejects the
// payload before coercion runs.
func TestInferInputSchemaWidensStringSlice(t *testing.T) {
	schema, err := InferInputSchema[stubStringSliceArgs]()
	if err != nil {
		t.Fatalf("InferInputSchema: %v", err)
	}
	prop := schema.Properties["choices"]
	if prop == nil {
		t.Fatal("schema has no choices property")
	}
	var types []string
	if prop.Type != "" {
		types = []string{prop.Type}
	} else {
		types = prop.Types
	}
	for _, want := range []string{"array", "string"} {
		if !slices.Contains(types, want) {
			t.Errorf("choices types = %v, want it to contain %q", types, want)
		}
	}
	if prop.Description == "" {
		t.Error("choices description lost during widening")
	}
	// Non-StringSlice fields stay untouched.
	if lp := schema.Properties["limit"]; lp == nil {
		t.Fatal("schema has no limit property")
	} else if lp.Description == "" {
		t.Error("limit description missing")
	}

	// Validate payloads the way ADK does (resolved schema over
	// JSON-decoded args): array, stringified array, and omission pass;
	// a number still fails.
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	valid := []map[string]any{
		{"choices": []any{"a", "b"}},
		{"choices": `["a", "b"]`},
		{},
	}
	for i, args := range valid {
		if err := resolved.Validate(args); err != nil {
			t.Errorf("payload %d (%v) rejected: %v", i, args, err)
		}
	}
	if err := resolved.Validate(map[string]any{"choices": 5}); err == nil {
		t.Error("numeric choices accepted, want rejection")
	}
}
