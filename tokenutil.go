package main

import "google.golang.org/genai"

// tokenutil provides token estimation and effective-budget helpers used by
// context management for pre-flight checks inside the BeforeModelCallback.
//
// Estimation is deliberately dependency-free (no tiktoken): chars/4 is the
// standard windmill-style ratio and is conservative enough for budget
// purposes. Provider-reported usage (UsageUpdateMsg) remains the source of
// truth for the status bar and session totals; the estimator only decides
// whether history fits before a request is sent.

// EstimateTokens estimates the token count of a text string using a chars/4
// fallback. Empty text is 0; non-empty text is at least 1.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	n := len(text)
	tokens := n / 4
	if n%4 != 0 {
		tokens++
	}
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// EstimateContentTokens estimates the token count of a genai.Content by
// summing per-part estimates. Images and files get a flat estimate (~1200
// tokens) instead of counting raw base64 bytes, which would hugely inflate
// the count. Function calls/responses are counted by name plus a small
// structural overhead.
func EstimateContentTokens(content *genai.Content) int {
	if content == nil {
		return 0
	}
	total := 0
	for _, part := range content.Parts {
		if part == nil {
			continue
		}
		switch {
		case part.Text != "":
			total += EstimateTokens(part.Text)
		case part.InlineData != nil || part.FileData != nil:
			total += 1200
		case part.FunctionCall != nil:
			total += 40 + EstimateTokens(part.FunctionCall.Name)
		case part.FunctionResponse != nil:
			total += 40 + EstimateTokens(part.FunctionResponse.Name)
		case part.ExecutableCode != nil:
			total += 100 + EstimateTokens(part.ExecutableCode.Code)
		default:
			total += 25
		}
	}
	return total
}

// EstimateContentsTokens sums the estimate across a slice of contents.
func EstimateContentsTokens(contents []*genai.Content) int {
	total := 0
	for _, c := range contents {
		total += EstimateContentTokens(c)
	}
	return total
}

// MaxInputTokens returns the effective max input budget for the model:
// min(model.MaxInputTokens, 0.9 * model.ContextWindow). When MaxInputTokens
// is 0 (provider does not report a separate input limit), the budget falls
// back to 0.9 * ContextWindow. Returns 0 when no usable window is known.
func MaxInputTokens(info *ModelInfo) int64 {
	if info == nil || info.ContextWindow <= 0 {
		return 0
	}
	budget := info.ContextWindow * 9 / 10
	if info.MaxInputTokens > 0 && info.MaxInputTokens < budget {
		return info.MaxInputTokens
	}
	return budget
}
