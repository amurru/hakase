package main

import (
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
)

// glamour v2 helper types: StyleConfig/StyleBlock fields use pointer values.

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func uintPtr(u uint) *uint    { return &u }

// markdownStyle builds the style config used for rendering agent responses.
// It starts from glamour's built-in dark style (complete coverage for lists,
// tables, code blocks, links, etc.) and overrides the heading levels so each
// level has its own color, plus a few readability tweaks for a dark TUI.
func markdownStyle() ansi.StyleConfig {
	s := styles.DarkStyleConfig

	// Heading levels: distinct colors so the hierarchy is obvious at a glance.
	s.Heading = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color:       strPtr("#00D7FF"),
			Bold:        boolPtr(true),
			BlockSuffix: "\n",
		},
	}
	// H1 renders as a filled banner chip so the most important heading stands out.
	s.H1 = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix:          " ",
			Suffix:          " ",
			Color:           strPtr("#F8F8F2"),
			BackgroundColor: strPtr("#005FD7"),
			Bold:            boolPtr(true),
		},
	}
	s.H2 = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "## ",
			Color:  strPtr("#00D7FF"),
			Bold:   boolPtr(true),
		},
	}
	s.H3 = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "### ",
			Color:  strPtr("#00FF87"),
			Bold:   boolPtr(true),
		},
	}
	s.H4 = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "#### ",
			Color:  strPtr("#FFD75F"),
			Bold:   boolPtr(true),
		},
	}
	s.H5 = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "##### ",
			Color:  strPtr("#FF5FAF"),
			Bold:   boolPtr(true),
		},
	}
	s.H6 = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "###### ",
			Color:  strPtr("#AF87FF"),
			Italic: boolPtr(true),
		},
	}

	// Block quotes: muted italic with a vertical bar gutter.
	s.BlockQuote = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color:  strPtr("#9AA5B1"),
			Italic: boolPtr(true),
		},
		Indent:      uintPtr(1),
		IndentToken: strPtr("│ "),
	}

	// Links: readable blue with underline; text stays bold.
	s.Link = ansi.StylePrimitive{
		Color:     strPtr("#5FAFFF"),
		Underline: boolPtr(true),
	}

	// Horizontal rule: faint dashes instead of the loud default.
	s.HorizontalRule = ansi.StylePrimitive{
		Color:  strPtr("240"),
		Format: "\n ─────────\n",
	}

	// List markers in the accent color so nested structure is easy to follow.
	s.Item = ansi.StylePrimitive{
		BlockPrefix: "• ",
		Color:       strPtr("#00D7FF"),
	}
	s.Enumeration = ansi.StylePrimitive{
		BlockPrefix: ". ",
		Color:       strPtr("#00D7FF"),
	}

	// Inline code: warm tone on a subtle dark background.
	s.Code = ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix:          " ",
			Suffix:          " ",
			Color:           strPtr("#FF9E64"),
			BackgroundColor: strPtr("#3A3A3A"),
		},
	}

	return s
}

// cachedMarkdownRenderer and cachedMarkdownWidth memoize the renderer across
// calls. The renderer bakes in the word-wrap width, so it is rebuilt only when
// the width changes (window resize). All rendering happens inside the Bubble
// Tea update loop (single goroutine), so no locking is required.
var (
	cachedMarkdownRenderer *glamour.TermRenderer
	cachedMarkdownWidth    int
)

func markdownRenderer(width int) *glamour.TermRenderer {
	if cachedMarkdownRenderer != nil && cachedMarkdownWidth == width {
		return cachedMarkdownRenderer
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle()),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		// Practically unreachable: the style config is static and valid. Fall
		// back to a bare renderer rather than crashing the TUI.
		r, _ = glamour.NewTermRenderer(glamour.WithWordWrap(width))
	}
	cachedMarkdownRenderer = r
	cachedMarkdownWidth = width
	return r
}

// renderMarkdown converts markdown content into ANSI-styled text wrapped to
// the given width. Leading/trailing blank lines are trimmed so the caller can
// control spacing between chat messages. On any rendering error the raw
// content is returned unchanged so the chat stream never breaks.
func renderMarkdown(content string, width int) string {
	if width <= 0 {
		width = 80
	}
	out, err := markdownRenderer(width).Render(content)
	if err != nil {
		return content
	}
	return strings.Trim(out, "\n")
}
