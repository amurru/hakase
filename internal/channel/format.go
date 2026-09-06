package channel

import (
	"html"
	"regexp"
	"strings"
)

// MaxMessageLen is Telegram's hard per-message text limit; every transport
// chunks final replies to fit.
const MaxMessageLen = 4096

// chunkLimit leaves headroom below MaxMessageLen for the HTML tags the
// markdown converter adds while converting each chunk.
const chunkLimit = 3500

var (
	linkRe   = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	boldRe   = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	italicRe = regexp.MustCompile(`\*(\S[^*\n]*\S|\S)\*`)
	strikeRe = regexp.MustCompile(`~~([^~\n]+)~~`)
	headerRe = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
)

// ChunkText splits s into pieces of at most limit runes, preferring line
// boundaries; a single line longer than limit is hard-split. Used for final
// replies before HTML conversion (see ChunkReply).
func ChunkText(s string, limit int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if len([]rune(s)) <= limit {
		return []string{s}
	}
	var chunks []string
	for len([]rune(s)) > limit {
		r := []rune(s)
		cut := -1
		if idx := strings.LastIndex(string(r[:limit]), "\n"); idx > limit/2 {
			cut = idx
		}
		if cut <= 0 {
			cut = limit
		}
		chunks = append(chunks, strings.TrimRight(string(r[:cut]), "\n"))
		s = strings.TrimSpace(string(r[cut:]))
	}
	if s != "" {
		chunks = append(chunks, s)
	}
	return chunks
}

// ChunkReply splits markdown into Telegram-ready HTML chunks. The markdown is
// split on raw boundaries and each chunk converted independently; an open
// code fence is closed at the end of a chunk and reopened in the next so no
// chunk contains unbalanced <pre> tags.
func ChunkReply(md string) []string {
	raw := ChunkText(md, chunkLimit)
	if raw == nil {
		return nil
	}
	inFence := false
	out := make([]string, 0, len(raw))
	for _, piece := range raw {
		out = append(out, markdownToTelegramHTMLState(piece, &inFence))
	}
	return out
}

// MarkdownToTelegramHTML converts markdown to the Telegram HTML subset
// (b/i/s/code/pre/a). Everything else is HTML-escaped; input that cannot be
// represented cleanly degrades to escaped text, never to invalid HTML.
func MarkdownToTelegramHTML(md string) string {
	return markdownToTelegramHTMLState(md, new(bool))
}

// markdownToTelegramHTMLState converts one chunk, carrying code-fence state
// across chunks of the same logical message.
func markdownToTelegramHTMLState(md string, inFence *bool) string {
	lines := strings.Split(md, "\n")
	var b strings.Builder
	fenceOpen := *inFence
	if fenceOpen {
		// Reopen the fence left open by the previous chunk of this message.
		b.WriteString("<pre>")
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if fenceOpen {
				b.WriteString("</pre>\n")
				fenceOpen = false
			} else {
				// Language hints after ``` are not rendered by Telegram; the
				// content is plain escaped text inside <pre>.
				b.WriteString("<pre>")
				fenceOpen = true
			}
			continue
		}
		if fenceOpen {
			b.WriteString(html.EscapeString(line) + "\n")
			continue
		}
		b.WriteString(inlineMarkdown(line))
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	if fenceOpen {
		b.WriteString("</pre>")
		// The fence stays open across chunks: the next chunk reopens content
		// without a new fence marker, so flip inFence back to closed only at
		// a real closing fence above.
		*inFence = true
	} else {
		*inFence = false
	}
	return strings.TrimRight(b.String(), "\n")
}

// inlineMarkdown escapes and formats one line of non-code markdown.
func inlineMarkdown(line string) string {
	// Split on backticks first so inline-code content is never transformed.
	parts := strings.Split(line, "`")
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 1 {
			b.WriteString("<code>" + html.EscapeString(part) + "</code>")
			continue
		}
		b.WriteString(inlinePlain(part))
	}
	return b.String()
}

// inlinePlain applies escaping plus the safe subset of inline markdown.
func inlinePlain(s string) string {
	out := html.EscapeString(s)
	out = linkRe.ReplaceAllString(out, `<a href="$2">$1</a>`)
	out = boldRe.ReplaceAllString(out, `<b>$1</b>`)
	out = italicRe.ReplaceAllString(out, `<i>$1</i>`)
	out = strikeRe.ReplaceAllString(out, `<s>$1</s>`)
	out = headerRe.ReplaceAllString(out, `<b>$1</b>`)
	return out
}
