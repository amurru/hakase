// stringslice.go - tolerant string-slice tool input.
//
// Some providers/models serialize a []string tool argument as a single
// JSON-encoded string instead of an array (e.g. clarify's choices
// arriving as "[\"a\", \"b\"]"). ADK validates raw args against the
// inferred schema before the handler runs, so the call fails with a
// schema error like:
//
//	validating /properties/choices: type: ["a", ...] has type "string",
//	want one of "null, array"
//
// StringSlice accepts both forms: a JSON array, or a string holding a
// JSON array, an empty string (nil), or one bare choice. NewDocTool
// widens the inferred schema of StringSlice fields to include "string"
// so validation passes and this coercion runs.
package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// StringSlice is a []string tool-input field that also accepts a JSON
// string: a stringified array decodes element-wise, an empty string
// decodes to nil, and any other string decodes to a single choice.
// Deliberately never comma-splits: legitimate choices contain commas.
type StringSlice []string

// UnmarshalJSON implements json.Unmarshaler.
func (s *StringSlice) UnmarshalJSON(data []byte) error {
	t := bytes.TrimSpace(data)
	if len(t) == 0 || string(t) == "null" {
		*s = nil
		return nil
	}
	switch t[0] {
	case '[':
		raw, err := decodeJSONArray(t)
		if err != nil {
			return err
		}
		*s = stringifyElements(raw)
		return nil
	case '"':
		var str string
		if err := json.Unmarshal(t, &str); err != nil {
			return err
		}
		if strings.TrimSpace(str) == "" {
			*s = nil
			return nil
		}
		// Provider-serialized array: decode element-wise. When the
		// string merely starts with '[' but is not valid JSON, keep
		// it verbatim as one choice rather than failing the call.
		if inner := bytes.TrimSpace([]byte(str)); len(inner) > 0 && inner[0] == '[' {
			if raw, err := decodeJSONArray(inner); err == nil {
				*s = stringifyElements(raw)
				return nil
			}
		}
		*s = StringSlice{str}
		return nil
	default:
		var v any
		if err := json.Unmarshal(t, &v); err != nil {
			return err
		}
		if v == nil {
			*s = nil
			return nil
		}
		*s = StringSlice{jsonScalarToString(v)}
		return nil
	}
}

// decodeJSONArray decodes a JSON array preserving number literals.
func decodeJSONArray(data []byte) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw []any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("stringslice: invalid array: %w", err)
	}
	return raw, nil
}

// stringifyElements converts decoded array elements to strings,
// skipping nulls (a null choice carries no information).
func stringifyElements(raw []any) StringSlice {
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if v == nil {
			continue
		}
		out = append(out, jsonScalarToString(v))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// jsonScalarToString renders one decoded JSON value as a choice string.
func jsonScalarToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}
