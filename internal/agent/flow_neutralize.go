// flow_neutralize.go - output tag neutralization (S26).
//
// Model- and human-visible flow text (denials, withhold overrides,
// approval previews) must not carry live markup or over-long opaque
// tokens that tool output or rule text could smuggle in:
//
//   - both angle-tag families (open <tag ...> and close </tag>) are
//     neutralized by escaping the brackets, so no live tag reaches the
//     model context or an approval card.
//   - no single emitted tag span exceeds 32 runes: an over-long span is
//     replaced with a length-only marker (fail-closed; the real content
//     hash already rides the audit argsHash, never raw text here).
//
// Order at every site is redact-then-neutralize-then-truncate: secrets
// are removed first (util.RedactSecrets), markup is escaped second, and
// length caps apply last. Pure function, no policy state: with no
// installed v2 policy these renderers never run, so FlowEnabled
// passthrough holds by construction.
package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxFlowTagRun caps one emitted tag span (S26 no->32 rule): a <...> run
// longer than this is withheld, never emitted.
const maxFlowTagRun = 32

// neutralizeFlowTags escapes live angle-tag spans in s. It returns the
// neutralized string and whether anything changed. A span is < up to the
// next > (bounded scan); a span longer than maxFlowTagRun runes becomes
// [tag-withheld:len=N] (length only, no content). Bare < or > with no
// partner still escapes (fail-closed direction: no live bracket emits).
func neutralizeFlowTags(s string) (string, bool) {
	if !strings.ContainsAny(s, "<>") {
		return s, false
	}
	var b strings.Builder
	b.Grow(len(s))
	changed := false
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '<' {
			if c == '>' {
				b.WriteString("\u203a")
				changed = true
			} else {
				b.WriteByte(c)
			}
			i++
			continue
		}
		// Tag span: find the closing bracket with a bounded scan.
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			b.WriteString("\u2039")
			changed = true
			i++
			continue
		}
		span := s[i : i+j+1]
		if utf8.RuneCountInString(span) > maxFlowTagRun {
			fmt.Fprintf(&b, "[tag-withheld:len=%d]", utf8.RuneCountInString(span))
			changed = true
		} else {
			inner := span[1 : len(span)-1]
			// H2: the interior can carry nested brackets (<<x>, <a <b>):
			// escape every < > inside until none survive, so no live
			// bracket reaches the model or an approval card.
			for strings.ContainsAny(inner, "<>") {
				inner = strings.ReplaceAll(inner, "<", "\u2039")
				inner = strings.ReplaceAll(inner, ">", "\u203a")
			}
			b.WriteString("\u2039")
			b.WriteString(inner)
			b.WriteString("\u203a")
			changed = true
		}
		i += j + 1
	}
	return b.String(), changed
}

// neutralizeFlowText is the S26 site helper: redact secrets first, then
// neutralize tags. Truncation stays the caller's job (existing caps
// unchanged).
func neutralizeFlowText(s string) string {
	n, _ := neutralizeFlowTags(s)
	return n
}
