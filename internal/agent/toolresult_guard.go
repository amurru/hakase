package agent

import (
	hctx "amurru/hakase/internal/context"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
)

// ToolResultGuard is a BeforeModelCallback that walks all FunctionResponse
// parts in the request contents and wraps long string values (>32 chars) in
// <UNTRUSTED_DATA> tags for prompt-injection defense. Strings already containing
// the tag are left unchanged (double-wrap prevention). The callback returns
// (nil, nil) to let the LLM call proceed unchanged.
func ToolResultGuard(ctx agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	if req == nil {
		return nil, nil
	}
	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil || part.FunctionResponse == nil {
				continue
			}
			fr := part.FunctionResponse
			if fr.Response == nil {
				continue
			}
			walkAndWrap(fr.Response)
		}
	}
	return nil, nil
}

// walkAndWrap recursively walks the given value (map or slice) and wraps any
// string longer than 32 characters that does not already contain
// <UNTRUSTED_DATA>.
func walkAndWrap(v any) {
	switch val := v.(type) {
	case map[string]any:
		for k, vv := range val {
			if s, ok := vv.(string); ok {
				if len(s) > 32 && !strings.Contains(s, "<UNTRUSTED_DATA>") {
					val[k] = hctx.WrapUntrustedData(s)
				}
			} else {
				walkAndWrap(vv)
			}
		}
	case []any:
		for i, vv := range val {
			if s, ok := vv.(string); ok {
				if len(s) > 32 && !strings.Contains(s, "<UNTRUSTED_DATA>") {
					val[i] = hctx.WrapUntrustedData(s)
				}
			} else {
				walkAndWrap(vv)
			}
		}
	}
}
