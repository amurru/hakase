// newtool.go - schema enrichment for function tools.
//
// The project documents tool parameters with doc:"..." struct tags, but the
// jsonschema-go library ADK uses to infer tool schemas only reads the
// "jsonschema" tag for descriptions - doc tags are silently dropped. Every
// tool parameter was therefore undocumented to the model, which small models
// cannot reliably call (the root cause of the general_purpose file-write
// failures). newDocTool re-infers the input schema and injects the doc
// descriptions so the model actually sees them.
package main

import (
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// newDocTool is functiontool.New plus doc:"..." description injection. When
// cfg.InputSchema is nil, the input schema is inferred exactly as
// functiontool would, then each property's description is filled from the
// matching struct field's doc tag before construction.
func newDocTool[TArgs, TResults any](cfg functiontool.Config, handler functiontool.Func[TArgs, TResults]) (tool.Tool, error) {
	if cfg.InputSchema == nil {
		schema, err := jsonschema.For[TArgs](nil)
		if err != nil {
			return nil, err
		}
		injectDocDescriptions(schema, reflect.TypeFor[TArgs]())
		cfg.InputSchema = schema
	}
	return functiontool.New(cfg, handler)
}

// injectDocDescriptions copies doc:"..." struct tag text into the matching
// JSON schema property descriptions. Input structs across the codebase are
// flat (scalar/slice fields only), so a single top-level pass covers them.
func injectDocDescriptions(schema *jsonschema.Schema, t reflect.Type) {
	if t.Kind() != reflect.Struct || schema == nil || schema.Properties == nil {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		// Resolve the JSON property name using the same rules as
		// encoding/json: first element of the json tag, "-" skips.
		name := f.Name
		if tag, ok := f.Tag.Lookup("json"); ok {
			if first, _, _ := strings.Cut(tag, ","); first != "" {
				if first == "-" {
					continue
				}
				name = first
			}
		}
		prop, ok := schema.Properties[name]
		if !ok {
			continue
		}
		if doc, ok := f.Tag.Lookup("doc"); ok && doc != "" && prop.Description == "" {
			prop.Description = doc
		}
	}
}
