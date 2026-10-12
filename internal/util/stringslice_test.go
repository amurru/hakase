package util

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestStringSliceUnmarshal pins the coercion contract: arrays pass
// through, provider-serialized strings decode, and degenerate forms
// degrade to nil or a single choice instead of failing the tool call.
func TestStringSliceUnmarshal(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		want  StringSlice
		isNil bool
	}{
		{"array", `["a","b"]`, StringSlice{"a", "b"}, false},
		{"empty array", `[]`, nil, true},
		{"null", `null`, nil, true},
		// The reported clarify failure: model serialized choices.
		{"stringified array", `"[\"ODE (ordinary) — init\", \"PDE (partial)\"]"`,
			StringSlice{"ODE (ordinary) — init", "PDE (partial)"}, false},
		{"stringified empty array", `"[]"`, nil, true},
		{"plain string", `"just one"`, StringSlice{"just one"}, false},
		{"blank string", `"  "`, nil, true},
		// No comma-splitting: choices legitimately contain commas.
		{"commas kept", `"a, b, c"`, StringSlice{"a, b, c"}, false},
		{"bracket prefix kept verbatim", `"[not json"`, StringSlice{"[not json"}, false},
		{"numbers", `[1, 2.5, true]`, StringSlice{"1", "2.5", "true"}, false},
		{"nulls skipped", `["a", null, "b"]`, StringSlice{"a", "b"}, false},
		{"top number", `5`, StringSlice{"5"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got StringSlice
			if err := json.Unmarshal([]byte(c.raw), &got); err != nil {
				t.Fatalf("Unmarshal(%q): %v", c.raw, err)
			}
			if c.isNil {
				if got != nil {
					t.Fatalf("Unmarshal(%q) = %q, want nil", c.raw, got)
				}
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Unmarshal(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// TestStringSliceInvalidArrayErrors documents the boundary: a raw
// (non-string) malformed array is still an error - only the string
// form degrades gracefully.
func TestStringSliceInvalidArrayErrors(t *testing.T) {
	var got StringSlice
	if err := json.Unmarshal([]byte(`["a",`), &got); err == nil {
		t.Fatal("malformed array: want error, got nil")
	}
}
