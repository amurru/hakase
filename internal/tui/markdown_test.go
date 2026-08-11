package tui

import (
	"regexp"
	"strings"
	"testing"
)

var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

// lipgloss fuses SGR params into one sequence (e.g. \x1b[38;5;252;1m), so
// match the bold/italic params as a trailing ;N segment or standalone code.
var (
	boldEscape   = regexp.MustCompile(`\x1b\[(1m|[0-9;]*;1m)`)
	italicEscape = regexp.MustCompile(`\x1b\[(3m|[0-9;]*;3m)`)
)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func TestMarkdownHeadingLevelsHaveDistinctStyles(t *testing.T) {
	out := renderMarkdown("# Alpha\n\n## Beta\n\n### Gamma\n\n#### Delta\n\n##### Epsilon\n\n###### Zeta", 80)

	lines := strings.Split(out, "\n")
	findLine := func(label string) string {
		for _, ln := range lines {
			if strings.Contains(stripANSI(ln), label) {
				return ln
			}
		}
		t.Fatalf("heading %q not found in rendered output:\n%s", label, stripANSI(out))
		return ""
	}

	h1, h2, h3, h4, h5, h6 := findLine("Alpha"), findLine("Beta"), findLine("Gamma"), findLine("Delta"), findLine("Epsilon"), findLine("Zeta")

	seen := map[string]bool{h1: true}
	for _, h := range []string{h2, h3, h4, h5, h6} {
		if seen[h] {
			t.Fatalf("heading levels must be styled differently, duplicate style:\nh1=%q\nh2=%q\nh3=%q\nh4=%q\nh5=%q\nh6=%q", h1, h2, h3, h4, h5, h6)
		}
		seen[h] = true
	}

	if !boldEscape.MatchString(h1) && !boldEscape.MatchString(h2) && !boldEscape.MatchString(h3) {
		t.Fatalf("expected bold styling on headings:\nh1=%q\nh2=%q\nh3=%q", h1, h2, h3)
	}
}

func TestMarkdownInlineFormatting(t *testing.T) {
	out := renderMarkdown("**bold** and *italic* and ~~strike~~ and `code`", 80)

	if !boldEscape.MatchString(out) {
		t.Fatalf("expected bold escape sequence in output: %q", out)
	}
	if !italicEscape.MatchString(out) {
		t.Fatalf("expected italic escape sequence in output: %q", out)
	}

	plain := stripANSI(out)
	for _, leaked := range []string{"**bold**", "*italic*", "~~strike~~", "`code`"} {
		if strings.Contains(plain, leaked) {
			t.Fatalf("markdown markers leaked into plain text (%q): %q", leaked, plain)
		}
	}
	if !strings.Contains(plain, "code") {
		t.Fatalf("inline code text missing: %q", plain)
	}
}

func TestMarkdownCodeBlockRendersBody(t *testing.T) {
	out := renderMarkdown("```go\nfunc main() {}\n```", 80)
	plain := stripANSI(out)

	if !strings.Contains(plain, "func main() {}") {
		t.Fatalf("expected code body in output: %q", plain)
	}
	if strings.Contains(plain, "```") {
		t.Fatalf("code fence markers leaked into plain text: %q", plain)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected ANSI styling on code block")
	}
}

func TestMarkdownFallbackOnEmpty(t *testing.T) {
	out := renderMarkdown("", 80)
	if out != "" {
		t.Fatalf("expected empty output for empty input, got %q", out)
	}
}

func TestAgentMessageRendersMarkdownWithLabel(t *testing.T) {
	m := newTestModel(t)
	m.chatHistory = []ChatMessage{
		{Role: "agent", Content: "# Title\n\nBody with **bold**"},
	}
	m.rebuildRenderedLines()
	m.renderChatViewport()

	content := m.chatViewport.View()
	plain := stripANSI(content)

	if !strings.Contains(plain, "🤖 Agent:") {
		t.Fatalf("expected agent label in viewport:\n%s", plain)
	}
	if !strings.Contains(content, "\x1b[") {
		t.Fatalf("expected styled markdown in viewport")
	}
	if !strings.Contains(plain, "Title") {
		t.Fatalf("expected heading text in viewport:\n%s", plain)
	}
}

func TestUserMessageStaysLiteral(t *testing.T) {
	m := newTestModel(t)
	m.chatHistory = []ChatMessage{
		{Role: "user", Content: "please show **literal** asterisks"},
	}
	m.rebuildRenderedLines()
	m.renderChatViewport()

	plain := stripANSI(m.chatViewport.View())
	if !strings.Contains(plain, "**literal**") {
		t.Fatalf("user message should render literal markdown markers:\n%s", plain)
	}
}
