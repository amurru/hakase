// tokenize.go - query/document tokenization for knowledge-base relevance
// ranking (Phase 3d-1). Pure stdlib, no external dependencies.
package main

import (
	"strings"
	"unicode"
)

// stopWords is a minimal English stopword set used to damp noise in
// relevance scoring. It is intentionally small: ranking is a soft signal,
// not a hard filter. The result SET is still gated by substring match, so
// dropping a stopword from the token stream can never drop a match - it
// only slightly lowers the score contribution of noise words.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "on": true, "for": true, "with": true, "is": true,
	"are": true, "was": true, "were": true, "be": true, "been": true, "at": true,
	"by": true, "as": true, "it": true, "its": true, "this": true, "that": true,
	"from": true, "how": true, "what": true, "why": true, "when": true, "use": true,
	"using": true, "used": true, "about": true, "into": true, "over": true,
}

// tokenize splits text into lowercase alphanumeric tokens, dropping
// stopwords and single-rune tokens. A query that is entirely stopwords or
// punctuation yields an empty token list (callers fall back to alphabetical
// ordering with zero scores).
func tokenize(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 || stopWords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// tokenCounts returns the frequency of each token in text.
func tokenCounts(text string) map[string]int {
	counts := make(map[string]int)
	for _, t := range tokenize(text) {
		counts[t]++
	}
	return counts
}
