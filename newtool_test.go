// newtool_test.go - verifies newDocTool injects doc:"..." tag text into the
// generated input schema so the LLM actually sees parameter descriptions.
package main

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// declarer matches the internal interface implemented by functionTool that
// exposes the tool declaration (name/description/input schema).
type declarer interface {
	Declaration() *genai.FunctionDeclaration
}

func propDescription(t *testing.T, tl declarer, name string) string {
	t.Helper()
	d := tl.Declaration()
	if d == nil || d.ParametersJsonSchema == nil {
		return ""
	}
	schema, ok := d.ParametersJsonSchema.(*jsonschema.Schema)
	if !ok {
		return ""
	}
	if schema.Properties == nil {
		return ""
	}
	if prop, ok := schema.Properties[name]; ok {
		return prop.Description
	}
	return ""
}

// TestNewDocToolInjectsDescriptions verifies that every parameter documented
// with a doc:"..." tag in the file-ops input structs appears as a property
// description in the tool declaration sent to the model.
func TestNewDocToolInjectsDescriptions(t *testing.T) {
	tools, err := createFileOpsTools(nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		toolName string
		prop     string
		want     string // substring expected in the description
	}{
		{"read_file", "path", "Path to the file"},
		{"read_file", "offset", "1-based line number"},
		{"write_file", "path", "Path of the file"},
		{"write_file", "content", "Full content"},
		{"write_file", "overwrite", "overwriting an existing file"},
		{"patch", "old_string", "must match the file content"},
		{"patch", "new_string", "Replacement text"},
		{"search_files", "pattern", "Regular expression"},
		{"search_files", "output_mode", "files_with_matches"},
	}

	for _, tc := range cases {
		var found toolDeclarer
		for _, tl := range tools {
			if tl.Name() == tc.toolName {
				found = tl.(toolDeclarer)
				break
			}
		}
		if found == nil {
			t.Fatalf("tool %q not found", tc.toolName)
		}
		desc := propDescription(t, found, tc.prop)
		if desc == "" {
			t.Errorf("%s.%s: expected a description, got empty", tc.toolName, tc.prop)
			continue
		}
		if !contains(desc, tc.want) {
			t.Errorf("%s.%s: description %q does not contain %q", tc.toolName, tc.prop, desc, tc.want)
		}
	}
}

// TestNewDocToolPreservesExistingSchema ensures the wrapper does not clobber
// an explicitly provided InputSchema: descriptions from doc tags are only
// injected when the schema is inferred.
func TestNewDocToolPreservesExistingSchema(t *testing.T) {
	// Reuse WriteFileInput with an explicit override that has its own
	// description; the wrapper must leave it untouched.
	tl, err := newDocTool(functiontool.Config{
		Name:        "write_file_custom",
		Description: "custom",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"content": {Type: "string", Description: "custom doc"},
			},
		},
	}, func(ctx agent.Context, input WriteFileInput) (WriteFileOutput, error) {
		return WriteFileOutput{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	desc := propDescription(t, tl.(toolDeclarer), "content")
	if desc != "custom doc" {
		t.Errorf("expected explicit schema description preserved, got %q", desc)
	}
}

// toolDeclarer aliases the declarer interface used in this test package.
type toolDeclarer = declarer

// contains is a small substring helper (avoids importing strings just for one
// call in tests).
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
