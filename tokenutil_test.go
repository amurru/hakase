package main

import (
	"testing"

	"google.golang.org/genai"
)

func TestEstimateTokensEmpty(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("EstimateTokens(\"\") = %d, want 0", got)
	}
}

func TestEstimateTokensRatio(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"a", 1},                         // 1 char -> 1 token (min)
		{"abcd", 1},                      // 4 chars -> 1 token
		{"abcdefgh", 2},                  // 8 chars -> 2 tokens
		{"abcdefghijkl", 3},              // 12 chars -> 3 tokens
		{string(make([]byte, 400)), 100}, // 400 chars -> 100 tokens
	}
	for _, c := range cases {
		if got := EstimateTokens(c.text); got != c.want {
			t.Fatalf("EstimateTokens(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

func TestEstimateContentTokensMultipleParts(t *testing.T) {
	content := &genai.Content{
		Role: "user",
		Parts: []*genai.Part{
			genai.NewPartFromText("hello world"),             // 11 chars -> 3 tokens
			{InlineData: &genai.Blob{MIMEType: "image/png"}}, // flat 1200
			{FunctionCall: &genai.FunctionCall{Name: "search"}},
		},
	}
	got := EstimateContentTokens(content)
	want := 3 + 1200 + 40 + EstimateTokens("search")
	if got != want {
		t.Fatalf("EstimateContentTokens = %d, want %d", got, want)
	}
}

func TestEstimateContentTokensNil(t *testing.T) {
	if got := EstimateContentTokens(nil); got != 0 {
		t.Fatalf("EstimateContentTokens(nil) = %d, want 0", got)
	}
}

func TestEstimateContentTokensImageFlatEstimate(t *testing.T) {
	// A big base64 blob should NOT blow up the estimate.
	big := string(make([]byte, 100_000))
	content := &genai.Content{Parts: []*genai.Part{{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte(big)}}}}
	if got := EstimateContentTokens(content); got != 1200 {
		t.Fatalf("image estimate = %d, want 1200 (flat)", got)
	}
}

func TestMaxInputTokens(t *testing.T) {
	// min(maxInput, 0.9*window) when both known.
	info := &ModelInfo{ContextWindow: 100_000, MaxInputTokens: 80_000}
	if got := MaxInputTokens(info); got != 80_000 {
		t.Fatalf("MaxInputTokens = %d, want 80000", got)
	}

	// maxInput larger than 0.9*window -> 0.9*window wins.
	info = &ModelInfo{ContextWindow: 100_000, MaxInputTokens: 99_000}
	if got := MaxInputTokens(info); got != 90_000 {
		t.Fatalf("MaxInputTokens = %d, want 90000", got)
	}

	// maxInput 0 -> falls back to 0.9*window.
	info = &ModelInfo{ContextWindow: 200_000}
	if got := MaxInputTokens(info); got != 180_000 {
		t.Fatalf("MaxInputTokens = %d, want 180000", got)
	}

	// nil / zero window -> 0.
	if got := MaxInputTokens(nil); got != 0 {
		t.Fatalf("MaxInputTokens(nil) = %d, want 0", got)
	}
	if got := MaxInputTokens(&ModelInfo{}); got != 0 {
		t.Fatalf("MaxInputTokens(empty) = %d, want 0", got)
	}
}
