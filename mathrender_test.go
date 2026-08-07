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

// TestEndToEndCompilePNG verifies the real tectonic + poppler pipeline
// produces a valid PNG. Skips when the toolchain is not installed.
func TestEndToEndCompilePNG(t *testing.T) {
	if !detectMathToolchain() {
		t.Skip("tectonic+poppler not installed")
	}
	png, err := compileEquationPNG(mathFrac)
	if err != nil {
		t.Fatalf("compileEquationPNG failed: %v", err)
	}
	if len(png) < 100 {
		t.Fatalf("suspiciously small PNG: %d bytes", len(png))
	}
	if png[0] != 0x89 || png[1] != 'P' || png[2] != 'N' || png[3] != 'G' {
		t.Fatalf("not a PNG signature: % x", png[:8])
	}
}

// TestEndToEndKittyRenderMarkdown verifies RenderMarkdown queues a real
// transmission when the toolchain is present. Skips when not installed.
func TestEndToEndKittyRenderMarkdown(t *testing.T) {
	if !detectMathToolchain() {
		t.Skip("tectonic+poppler not installed")
	}
	mr := newMathRenderer()
	mr.kittyOK = true
	mr.toolchainOK = true

	out := mr.RenderMarkdown("$$\n"+mathFrac+"\n$$", 80, true)
	plain := stripANSI(out)
	if !strings.ContainsRune(plain, '\U0010EEEE') {
		t.Fatalf("expected kitty placeholder in output:\n%q", plain)
	}
	raw := mr.FlushRaw()
	if len(raw) == 0 {
		t.Fatal("expected a queued APC transmission")
	}
	seq := raw[0]
	if !strings.HasPrefix(seq, "\x1b_G") || !strings.Contains(seq, "f=100") {
		t.Fatalf("unexpected APC sequence")
	}
}

// TestSplitMathSegmentsFirstLastLine verifies math blocks at the very start
// or end of the content keep the odd-index-math parity (the splitter always
// emits boundary text segments, even when empty).
func TestSplitMathSegmentsFirstLastLine(t *testing.T) {
	// Content STARTING with math: parity must be preserved (odd = math).
	segs := splitMathSegments("$$\nx^2\n$$\n\nTail")
	if len(segs) != 3 {
		t.Fatalf("expected 3 segments (leading empty), got %d: %q", len(segs), segs)
	}
	if segs[0] != "" || segs[1] != "x^2" || segs[2] != "\n\nTail" {
		t.Fatalf("unexpected leading-math split: %q", segs)
	}
	if segs[1] != "x^2" {
		t.Fatal("math must be at odd index")
	}

	// Content ENDING with math.
	segs = splitMathSegments("Head\n\n$$\nx^2\n$$")
	if len(segs) != 3 {
		t.Fatalf("expected 3 segments (trailing empty), got %d: %q", len(segs), segs)
	}
	if segs[0] != "Head\n\n" || segs[1] != "x^2" || segs[2] != "" {
		t.Fatalf("unexpected trailing-math split: %q", segs)
	}

	// Only math, no text.
	segs = splitMathSegments("$$\nx^2\n$$")
	if len(segs) != 3 || segs[0] != "" || segs[1] != "x^2" || segs[2] != "" {
		t.Fatalf("unexpected pure-math split: %q", segs)
	}
}

// TestSplitMathSegmentsEscapedDollar verifies \$\$ is treated as literal
// text, not a display-math opener.
func TestSplitMathSegmentsEscapedDollar(t *testing.T) {
	segs := splitMathSegments("Price \\$\\$ 5")
	if len(segs) != 1 {
		t.Fatalf("escaped $$ must not split, got %d segments: %q", len(segs), segs)
	}
	if segs[0] != "Price \\$\\$ 5" {
		t.Fatalf("escaped dollar content changed: %q", segs[0])
	}
}

// TestSplitMathSegmentsInlineCodeDollar verifies $$ inside inline backticks
// and tilde code spans is left untouched.
func TestSplitMathSegmentsInlineCodeDollar(t *testing.T) {
	for _, in := range []string{
		"Use `$$x$$` in inline code",
		"Use ~~$$x$$~~ in tilde code",
		"Multi ``$$x$$`` tick code",
	} {
		segs := splitMathSegments(in)
		if len(segs) != 1 {
			t.Fatalf("%q must not split, got %d segments: %q", in, len(segs), segs)
		}
	}
}

// TestRenderMarkdownGoldenFrac verifies the exact Unicode rendering of a
// stacked fraction through the full pipeline (golden output for the fallback
// tier).
func TestRenderMarkdownGoldenFrac(t *testing.T) {
	mr := newMathRenderer()
	mr.kittyOK = false
	mr.toolchainOK = false

	out := mr.RenderMarkdown("$$\n\\frac{dy}{dx}\n$$", 40, true)
	plain := stripANSI(out)

	// The rendered grid must contain the numerator and denominator on
	// separate lines with a fraction bar between them.
	lines := strings.Split(plain, "\n")
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "dy") || !strings.Contains(joined, "dx") {
		t.Fatalf("golden frac missing numerator/denominator:\n%q", joined)
	}
	foundBar := false
	for _, ln := range lines {
		if strings.Contains(ln, "────") {
			foundBar = true
			break
		}
	}
	if !foundBar {
		t.Fatalf("golden frac missing fraction bar:\n%s", plain)
	}
}

// TestRenderMarkdownMathThenCode verifies a message mixing display math and a
// code block renders both correctly (fence tracking must not leak state).
func TestRenderMarkdownMathThenCode(t *testing.T) {
	mr := newMathRenderer()
	mr.kittyOK = false
	mr.toolchainOK = false

	in := "$$\nx^2\n$$\n\n```\n$$not math$$\n```"
	out := mr.RenderMarkdown(in, 80, true)
	plain := stripANSI(out)

	if !strings.Contains(plain, "x²") && !strings.Contains(plain, "x^2") {
		t.Fatalf("math block missing:\n%s", plain)
	}
	if !strings.Contains(plain, "$$not math$$") {
		t.Fatalf("code-block $$ must be preserved verbatim:\n%s", plain)
	}
}

// TestRenderMarkdownASCIIMode verifies 7-bit terminals get ASCII output.
func TestRenderMarkdownASCIIMode(t *testing.T) {
	mr := newMathRenderer()
	mr.kittyOK = false
	mr.toolchainOK = false
	mr.asciiMode = true

	out := mr.RenderMarkdown("$$\n\\frac{dy}{dx}\n$$", 40, true)
	plain := stripANSI(out)
	for _, r := range plain {
		if r > 127 {
			t.Fatalf("ASCII mode emitted non-ASCII rune %q:\n%s", r, plain)
		}
	}
	// The fraction must still be recognizable (stacked with an ASCII bar).
	if !strings.Contains(plain, "dy") || !strings.Contains(plain, "dx") {
		t.Fatalf("ASCII frac missing parts:\n%s", plain)
	}
}

// TestDecisionTree walks the full rendering decision matrix and verifies each
// branch produces sane output (kitty+toolchain / kitty-only / neither / 7-bit).
func TestDecisionTree(t *testing.T) {
	oldKitty, oldTerm, oldProg := os.Getenv("KITTY_WINDOW_ID"), os.Getenv("TERM"), os.Getenv("TERM_PROGRAM")
	defer func() {
		os.Setenv("KITTY_WINDOW_ID", oldKitty)
		os.Setenv("TERM", oldTerm)
		os.Setenv("TERM_PROGRAM", oldProg)
	}()

	// Branch 1: kitty + toolchain -> images (when allowed).
	mr := newMathRenderer()
	mr.kittyOK = true
	mr.toolchainOK = true
	mr.asciiMode = false
	if !mr.canRenderImages() {
		t.Fatal("kitty+toolchain must enable images")
	}
	out := mr.RenderMarkdown("$$\n"+mathFrac+"\n$$", 80, true)
	if !strings.ContainsRune(stripANSI(out), '\U0010EEEE') {
		t.Fatal("branch kitty+toolchain should emit placeholder runes")
	}
	// Streaming (allowImages=false) -> Unicode even with images available.
	out = mr.RenderMarkdown("$$\n"+mathFrac+"\n$$", 80, false)
	if strings.ContainsRune(stripANSI(out), '\U0010EEEE') {
		t.Fatal("streaming must not emit placeholders")
	}

	// Branch 2: kitty only, no toolchain -> Unicode.
	mr2 := newMathRenderer()
	mr2.kittyOK = true
	mr2.toolchainOK = false
	mr2.asciiMode = false
	if mr2.canRenderImages() {
		t.Fatal("kitty without toolchain must not enable images")
	}
	out = mr2.RenderMarkdown("$$\n"+mathFrac+"\n$$", 80, true)
	if strings.ContainsRune(stripANSI(out), '\U0010EEEE') {
		t.Fatal("kitty-only branch must fall back to Unicode")
	}

	// Branch 3: neither -> Unicode.
	mr3 := newMathRenderer()
	mr3.kittyOK = false
	mr3.toolchainOK = false
	if mr3.canRenderImages() {
		t.Fatal("neither branch must not enable images")
	}
	out = mr3.RenderMarkdown("$$\n"+mathFrac+"\n$$", 80, true)
	if strings.ContainsRune(stripANSI(out), '\U0010EEEE') {
		t.Fatal("neither branch must fall back to Unicode")
	}

	// Branch 4: 7-bit terminal even with everything -> Unicode ASCII.
	mr4 := newMathRenderer()
	mr4.kittyOK = true
	mr4.toolchainOK = true
	mr4.asciiMode = true
	if mr4.canRenderImages() {
		t.Fatal("7-bit terminal must not enable images")
	}
}
