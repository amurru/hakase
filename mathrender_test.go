package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/doug/termtex"
)

// canonical math expressions used across the tests (the 6 requested probes).
const (
	mathFrac      = `\frac{dy}{dx}`
	mathSum       = `\sum_{i=1}^{n} x_i`
	mathIntegral  = `\int_0^\infty e^{-x^2} dx`
	mathSqrt      = `\sqrt{a^2 + b^2}`
	mathGreek     = `\alpha + \beta = \gamma`
	mathMatrix    = `\begin{pmatrix} a & b \\ c & d \end{pmatrix}`
)

func TestSplitMathSegments(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
	}{
		{
			name: "single display block",
			in:   "Before\n\n$$\n\\frac{1}{2}\n$$\n\nAfter",
			want: []string{"Before\n\n", "\\frac{1}{2}", "\n\nAfter"},
		},
		{
			name: "no math",
			in:   "plain text only",
			want: []string{"plain text only"},
		},
		{
			name: "inline math is not split",
			in:   "Energy is $E=mc^2$ here",
			want: []string{"Energy is $E=mc^2$ here"},
		},
		{
			name: "code fence contains dollar signs",
			in:   "```\n$$not math$$\n```\n\n$$\nx^2\n$$",
			want: []string{"```\n$$not math$$\n```\n\n", "x^2", ""},
		},
		{
			name: "unbalanced opener is literal",
			in:   "costs $$5 and $10",
			want: []string{"costs $$5 and $10"},
		},
		{
			name: "two blocks",
			in:   "$$\na\n$$\nmid\n$$\nb\n$$",
			want: []string{"", "a", "\nmid\n", "b", ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitMathSegments(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d segments (%q), want %d (%q)", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("segment %d = %q, want %q (all: %q)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestMathHashStable(t *testing.T) {
	a := mathHash(mathFrac)
	b := mathHash(mathFrac)
	if a != b {
		t.Fatalf("hash not stable: %s != %s", a, b)
	}
	if len(a) != 16 {
		t.Fatalf("expected 8-byte hex hash, got %d chars: %q", len(a), a)
	}
}

func TestTermtexCanonicalExpressions(t *testing.T) {
	mr := newMathRenderer()
	mr.kittyOK = false // force the Unicode path regardless of environment
	mr.toolchainOK = false

	checks := []struct {
		in     string
		expect string // substring that must appear in the Unicode output
	}{
		{mathFrac, "────"},     // stacked fraction bar
		{mathSum, "∑"},         // big operator
		{mathIntegral, "∫"},    // integral
		{mathSqrt, "√"},        // sqrt radical
		{mathGreek, "α + β = γ"},
		{mathMatrix, "⎛"}, // tall matrix delimiter
	}
	for _, c := range checks {
		out, err := termtex.Render(c.in, mr.termtexStyle())
		if err != nil {
			t.Fatalf("termtex error for %q: %v", c.in, err)
		}
		if !strings.Contains(out, c.expect) {
			t.Fatalf("termtex output for %q missing %q:\n%s", c.in, c.expect, out)
		}
	}
}

func TestRenderMarkdownMathUnicodeFallback(t *testing.T) {
	mr := newMathRenderer()
	mr.kittyOK = false
	mr.toolchainOK = false

	// Display math renders as Unicode when the kitty path is unavailable.
	md := "Equation:\n\n$$\n\\frac{dy}{dx}\n$$\n\nDone."
	out := mr.RenderMarkdown(md, 80, true)
	plain := stripANSI(out)
	if !strings.Contains(plain, "────") {
		t.Fatalf("expected Unicode fraction bar in fallback output:\n%s", plain)
	}
	if strings.Contains(plain, "$$") {
		t.Fatalf("display math delimiters leaked into output:\n%s", plain)
	}
	if !strings.Contains(plain, "Done.") {
		t.Fatalf("trailing text missing:\n%s", plain)
	}
}

func TestRenderMarkdownInlineMath(t *testing.T) {
	mr := newMathRenderer()
	mr.kittyOK = false
	mr.toolchainOK = false

	out := mr.RenderMarkdown("Energy is $E=mc^2$ inline", 80, true)
	plain := stripANSI(out)
	if !strings.Contains(plain, "mc²") {
		t.Fatalf("expected inline math rendered to Unicode superscript:\n%s", plain)
	}
}

func TestRenderMarkdownCodeSpanNotTouched(t *testing.T) {
	mr := newMathRenderer()
	mr.kittyOK = false
	mr.toolchainOK = false

	out := mr.RenderMarkdown("Use `$x$` in code", 80, true)
	plain := stripANSI(out)
	if !strings.Contains(plain, "$x$") {
		t.Fatalf("math-looking code span was modified:\n%s", plain)
	}
}

func TestTermtexBlockFallbackOnError(t *testing.T) {
	mr := newMathRenderer()
	mr.kittyOK = false
	mr.toolchainOK = false

	// An expression termtex cannot parse should degrade to a code block with
	// the raw LaTeX rather than silently wrong output.
	out := termtexBlock(`\begin{unknownenv} x \end{unknownenv}`, mr.termtexStyle())
	if !strings.Contains(out, "```") {
		t.Fatalf("expected code-block fallback, got:\n%s", out)
	}
	if !strings.Contains(out, "unknownenv") {
		t.Fatalf("expected raw latex preserved in fallback:\n%s", out)
	}
}

func TestImageToCellsDefaults(t *testing.T) {
	// With no terminal pixel geometry (test env), the fallback cell size
	// 8x16 is used.
	cols, rows := imageToCells(80, 16, 0)
	if cols != 10 || rows != 1 {
		t.Fatalf("80x16px with 8x16 cells = 10x1, got %dx%d", cols, rows)
	}
	// Capping to 5 cols scales rows proportionally (10 -> 5, rows 1 -> 1).
	cols, rows = imageToCells(80, 16, 5)
	if cols != 5 || rows != 1 {
		t.Fatalf("capped 80x16px = 5x1, got %dx%d", cols, rows)
	}
}

func TestKittyPlaceholderGridStructure(t *testing.T) {
	// Build a tiny 1x1 PNG to exercise the placeholder grid.
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	var buf strings.Builder
	_ = png.Encode(&buf, img)

	grid := kittyPlaceholderGrid(42, []byte(buf.String()), 80)
	// With default 8x16 cells and a 1x1 px image, cols=rows=1.
	if grid == "" {
		t.Fatal("empty placeholder grid")
	}
	if !strings.Contains(grid, "\x1b[38;2;0;0;42m") {
		t.Fatalf("expected image ID 42 encoded in foreground color, got: %q", grid)
	}
	if !strings.ContainsRune(grid, '\U0010EEEE') {
		t.Fatalf("expected kitty placeholder rune in grid: %q", grid)
	}
}

func TestMathRendererCachePreventsRecompile(t *testing.T) {
	mr := newMathRenderer()
	mr.kittyOK = true
	mr.toolchainOK = true
	// Inject a fake PNG into the cache and verify no recompilation happens
	// (renderKittyBlock returns the placeholder from cache without queuing a
	// new transmission).
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	var buf strings.Builder
	_ = png.Encode(&buf, img)
	mr.pngCache[mathHash(mathFrac)] = []byte(buf.String())
	mr.imageIDs[mathHash(mathFrac)] = 7

	rendered, ok := mr.renderKittyBlock(mathFrac, 80)
	if !ok {
		t.Fatal("expected cached block to render")
	}
	if rendered == "" {
		t.Fatal("expected non-empty placeholder from cache")
	}
	if len(mr.pendingRaw) != 0 {
		t.Fatalf("cache hit must not queue a transmission, got %d pending", len(mr.pendingRaw))
	}
	if mr.FlushRaw() != nil {
		t.Fatal("FlushRaw should be empty after cache hit")
	}
}

func TestKittyTransmitSequenceFormat(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf strings.Builder
	_ = png.Encode(&buf, img)

	seq := kittyTransmitSequence(3, []byte(buf.String()), 80)
	if !strings.HasPrefix(seq, "\x1b_G") {
		t.Fatalf("expected APC opener, got: %q", seq[:8])
	}
	if !strings.HasSuffix(seq, "\x1b\\") {
		t.Fatalf("expected APC terminator, got: %q", seq[len(seq)-8:])
	}
	if !strings.Contains(seq, "a=T") {
		t.Fatalf("expected transmit+display action: %q", seq)
	}
	if !strings.Contains(seq, "f=100") {
		t.Fatalf("expected PNG format: %q", seq)
	}
	if !strings.Contains(seq, "i=3") {
		t.Fatalf("expected image ID: %q", seq)
	}
	if !strings.Contains(seq, "U=1") {
		t.Fatalf("expected virtual placement: %q", seq)
	}
}

func TestClearAllSequence(t *testing.T) {
	mr := newMathRenderer()
	if got := mr.ClearAll(); got != "\x1b_Ga=d\x1b\\" {
		t.Fatalf("unexpected clear sequence: %q", got)
	}
}

func TestDetectKittyCapable(t *testing.T) {
	// Env manipulation must restore state to avoid polluting other tests.
	oldKitty, oldTerm, oldProg := os.Getenv("KITTY_WINDOW_ID"), os.Getenv("TERM"), os.Getenv("TERM_PROGRAM")
	defer func() {
		os.Setenv("KITTY_WINDOW_ID", oldKitty)
		os.Setenv("TERM", oldTerm)
		os.Setenv("TERM_PROGRAM", oldProg)
	}()

	os.Unsetenv("KITTY_WINDOW_ID")
	os.Unsetenv("TERM_PROGRAM")

	os.Setenv("TERM", "xterm-kitty")
	if !detectKittyCapable() {
		t.Fatal("TERM=xterm-kitty must be detected")
	}
	os.Setenv("TERM", "xterm-256color")
	if detectKittyCapable() {
		t.Fatal("plain xterm must NOT be detected as kitty")
	}
	os.Setenv("KITTY_WINDOW_ID", "1")
	if !detectKittyCapable() {
		t.Fatal("KITTY_WINDOW_ID must be detected")
	}
	os.Unsetenv("KITTY_WINDOW_ID")
	os.Setenv("TERM_PROGRAM", "WezTerm")
	if !detectKittyCapable() {
		t.Fatal("WezTerm must be detected")
	}
	os.Setenv("TERM_PROGRAM", "iTerm.app")
	if detectKittyCapable() {
		t.Fatal("iTerm must NOT be detected (no kitty protocol)")
	}
}

func TestMathRendererUnicodeWhenToolchainMissing(t *testing.T) {
	// In the test environment tectonic is (likely) absent; verify the
	// renderer degrades to Unicode without erroring even when images are
	// requested.
	mr := newMathRenderer()
	if mr.canRenderImages() {
		t.Skip("tectonic+pdftoppm present; image path active")
	}
	out := mr.RenderMarkdown("$$\n"+mathFrac+"\n$$", 80, true)
	if !strings.Contains(stripANSI(out), "────") {
		t.Fatalf("expected Unicode fallback without toolchain:\n%s", stripANSI(out))
	}
}

// testPNG returns a valid in-memory PNG of the given pixel size.
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf strings.Builder
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return []byte(buf.String())
}

// TestMathRawCmdsFlush verifies mathRawCmds drains the pending queue into
// tea.Raw commands.
func TestMathRawCmdsFlush(t *testing.T) {
	m := newTestModel(t)
	m.math.mu.Lock()
	m.math.pendingRaw = []string{"\x1b_Gseq1\x1b\\", "\x1b_Gseq2\x1b\\"}
	m.math.mu.Unlock()

	cmds := m.mathRawCmds()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 tea.Raw cmds, got %d", len(cmds))
	}
	if m.math.FlushRaw() != nil {
		t.Fatal("pendingRaw must be drained after mathRawCmds")
	}
}

// TestCacheHitDoesNotRetransmit verifies a cached equation renders placeholders
// without queuing a new transmission (the terminal already holds the image).
func TestCacheHitDoesNotRetransmit(t *testing.T) {
	m := newTestModel(t)
	m.math.kittyOK = true
	m.math.toolchainOK = true
	img := testPNG(t, 80, 16)
	hash := mathHash(mathFrac)
	m.math.pngCache[hash] = img
	m.math.imageIDs[hash] = 3

	rendered, ok := m.math.renderKittyBlock(mathFrac, 80)
	if !ok {
		t.Fatal("expected cached render")
	}
	if !strings.ContainsRune(rendered, '\U0010EEEE') {
		t.Fatalf("expected placeholder rune: %q", rendered)
	}
	if m.math.FlushRaw() != nil {
		t.Fatal("cache hit must not queue a transmission")
	}
}

// TestStreamingUsesUnicode verifies that while a message is streaming the
// display-math path stays on Unicode even when the kitty toolchain exists
// (per-chunk recompilation would be wasteful; images upgrade on completion).
func TestStreamingUsesUnicode(t *testing.T) {
	m := newTestModel(t)
	m.math.kittyOK = true
	m.math.toolchainOK = true
	m.mathImages = false

	m.chatHistory = []ChatMessage{{Role: "agent", Content: "$$\n" + mathFrac + "\n$$"}}
	m.rebuildRenderedLines()
	m.renderChatViewport()
	plain := stripANSI(m.chatViewport.View())

	if !strings.Contains(plain, "────") {
		t.Fatalf("streaming must render Unicode math:\n%s", plain)
	}
	if m.math.FlushRaw() != nil {
		t.Fatal("streaming must not queue kitty transmissions")
	}
}
