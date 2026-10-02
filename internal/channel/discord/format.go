// format.go - Discord message shaping (DC-009).
//
// Discord renders a markdown-ish subset: code fences, inline code, bold,
// italic, and links all pass through; Telegram-style HTML does not exist
// here, so any HTML-isms are stripped to their text. Messages cap at
// MaxMessageLen chars (Discord's 2000); longer texts split on rune
// boundaries, preferring newline breaks.
package discord

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxMessageLen is Discord's message character limit.
const MaxMessageLen = 2000

var (
	htmlTagRe  = regexp.MustCompile(`<[^>]+>`)
	htmlEntRe  = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&", "&quot;", `"`)
	multiBlank = regexp.MustCompile(`\n{3,}`)
)

// MarkdownToDiscord converts orchestrator markdown (which may carry
// Telegram HTML-isms from shared render paths) into Discord-safe text:
// HTML tags stripped, entities unescaped, runs of blank lines collapsed.
func MarkdownToDiscord(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "")
	s = htmlEntRe.Replace(s)
	s = multiBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// ChunkText splits text into ≤MaxMessageLen-rune chunks, preferring to
// break at the last newline before the limit so code fences and lists
// usually survive intact. A single over-long line is hard-split.
func ChunkText(s string) []string {
	if utf8.RuneCountInString(s) <= MaxMessageLen {
		return []string{s}
	}
	var out []string
	runes := []rune(s)
	for len(runes) > 0 {
		if len(runes) <= MaxMessageLen {
			out = append(out, string(runes))
			break
		}
		cut := MaxMessageLen
		for i := MaxMessageLen - 1; i > MaxMessageLen/2; i-- {
			if runes[i] == '\n' {
				cut = i + 1
				break
			}
		}
		out = append(out, strings.TrimRight(string(runes[:cut]), "\n"))
		runes = runes[cut:]
	}
	return out
}
