// newtool.go - schema enrichment for function tools.
//
// The project documents tool parameters with doc:"..." struct tags, but the
// jsonschema-go library ADK uses to infer tool schemas only reads the
// "jsonschema" tag for descriptions - doc tags are silently dropped. Every
// tool parameter was therefore undocumented to the model, which small models
// cannot reliably call (the root cause of the general_purpose file-write
// failures). newDocTool re-infers the input schema and injects the doc
// descriptions so the model actually sees them.
package util

import (
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// NewDocTool is functiontool.New plus doc:"..." description injection. When
// cfg.InputSchema is nil, the input schema is inferred exactly as
// functiontool would, then each property's description is filled from the
// matching struct field's doc tag before construction.
// InferInputSchema infers the JSON schema for a tool-input struct exactly
// as NewDocTool would use it: doc:"..." descriptions injected plus
// StringSlice fields widened to admit provider-serialized strings.
// Exported so tests can assert on the schema ADK validates against.
func InferInputSchema[TArgs any]() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[TArgs](nil)
	if err != nil {
		return nil, err
	}
	t := reflect.TypeFor[TArgs]()
	widenStringSliceTypes(schema, t)
	injectDocDescriptions(schema, t)
	return schema, nil
}

func NewDocTool[TArgs, TResults any](cfg functiontool.Config, handler functiontool.Func[TArgs, TResults]) (tool.Tool, error) {
	if cfg.InputSchema == nil {
		schema, err := InferInputSchema[TArgs]()
		if err != nil {
			return nil, err
		}
		cfg.InputSchema = schema
	}
	return functiontool.New(cfg, handler)
}

// stringSliceType marks tool-input fields that accept a JSON array or a
// provider-serialized string (see StringSlice). Their inferred schema
// must admit "string" or ADK rejects the string form before the
// handler's coercion ever runs.
var stringSliceType = reflect.TypeFor[StringSlice]()

// widenStringSliceTypes adds "string" to the allowed types of every
// StringSlice property, preserving the inferred array/null entries.
func widenStringSliceTypes(schema *jsonschema.Schema, t reflect.Type) {
	if t.Kind() != reflect.Struct || schema == nil || schema.Properties == nil {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() || f.Type != stringSliceType {
			continue
		}
		name, ok := jsonPropertyName(f)
		if !ok {
			continue
		}
		prop, ok := schema.Properties[name]
		if !ok || prop == nil {
			continue
		}
		switch {
		case prop.Type != "" && prop.Type != "string":
			prop.Types = []string{prop.Type, "string"}
			prop.Type = ""
		case prop.Types != nil:
			found := false
			for _, typ := range prop.Types {
				if typ == "string" {
					found = true
					break
				}
			}
			if !found {
				prop.Types = append(prop.Types, "string")
			}
		}
	}
}

// jsonPropertyName resolves the JSON property name using the same rules
// as encoding/json: first element of the json tag, "-" skips.
func jsonPropertyName(f reflect.StructField) (string, bool) {
	name := f.Name
	if tag, ok := f.Tag.Lookup("json"); ok {
		if first, _, _ := strings.Cut(tag, ","); first != "" {
			if first == "-" {
				return "", false
			}
			name = first
		}
	}
	return name, true
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
		name, ok := jsonPropertyName(f)
		if !ok {
			continue
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
