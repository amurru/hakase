package agent

import (
	hctx "amurru/hakase/internal/context"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
)

// ToolResultGuard is a BeforeModelCallback that walks all FunctionResponse
// parts in the request contents and frames untrusted strings in
// <UNTRUSTED_DATA> tags for prompt-injection defense (R30: every
// non-empty string frames - there is no length exemption, so short
// payloads cannot slip through unframed - and only content carrying
// both tags counts as already framed). Already-framed strings are left
// unchanged (idempotent). The callback returns (nil, nil) to let the
// LLM call proceed unchanged.
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

// walkAndWrap recursively walks the given value (map or slice) and frames
// every non-empty string in <UNTRUSTED_DATA> (R30: no length exemption).
// Content already carrying both framing tags is left unchanged
// (idempotent double-wrap prevention); a lone tag never suppresses
// framing and neutralizes on wrap.
func walkAndWrap(v any) {
	switch val := v.(type) {
	case map[string]any:
		for k, vv := range val {
			if s, ok := vv.(string); ok {
				// R30: every non-empty string wraps - no
				// HasUntrustedFraming pre-check. WrapUntrustedData
				// itself owns idempotency (own-frame no-op) and
				// sanitizes + neutralizes forged frames, so a
				// guard-side framing test would skip sanitization
				// for attacker-forged content (M1 fail-closed:
				// wrap, never skip).
				if s != "" {
					val[k] = hctx.WrapUntrustedData(s)
				}
			} else {
				walkAndWrap(vv)
			}
		}
	case []any:
		for i, vv := range val {
			if s, ok := vv.(string); ok {
				// Same no-skip rule as above (M1).
				if s != "" {
					val[i] = hctx.WrapUntrustedData(s)
				}
			} else {
				walkAndWrap(vv)
			}
		}
	}
}
