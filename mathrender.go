package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/png" // register PNG decoder for image.DecodeConfig
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/doug/termtex"
)

// MathRenderer renders LaTeX math in the TUI chat output.
//
// Two rendering tiers, decided per display-math block:
//
//   - Kitty graphics protocol (high quality): the equation is compiled to a
//     transparent PNG via tectonic + pdftoppm, transmitted to the terminal
//     with the kitty graphics protocol (virtual placement), and the markdown
//     block is replaced by U+10EEEE placeholder cells that the terminal
//     swaps for the rendered image. The placeholders scroll with the buffer
//     and survive window resizes.
//
//   - Unicode fallback (github.com/doug/termtex): a pure-Go recursive
//     descent parser renders the LaTeX to a character grid using Unicode
//     math glyphs (stacked fractions, matrix delimiters, limits). Zero
//     dependencies, works in every terminal. Used when the terminal lacks
//     kitty protocol support, the toolchain is missing, compilation fails,
//     or the block is inline math ($...$) which never becomes an image.
//
// The renderer is safe for concurrent use: capability detection is cached at
// construction, the PNG cache is mutex-guarded, and raw kitty sequences are
// queued for the caller to flush (via tea.Raw) after rendering.
type MathRenderer struct {
	mu sync.Mutex

	kittyOK     bool // terminal supports the kitty graphics protocol
	toolchainOK bool // tectonic + pdftoppm binaries are available
	asciiMode   bool // terminal needs 7-bit ASCII output

	// nextImageID is the next kitty image ID (monotonic per process).
	nextImageID int

	// imageIDs maps a math source hash to the kitty image ID assigned when it
	// was first transmitted, so placeholder re-emission references the same
	// image without re-transmitting.
	imageIDs map[string]int

	// pendingRaw holds kitty APC sequences produced during rendering that
	// must be flushed to the terminal (via tea.Raw) after the render pass.
	pendingRaw []string

	// pngCache maps a math source hash to its compiled PNG bytes so repeated
	// renderings of the same equation do not re-run the toolchain.
	pngCache map[string][]byte
}

// newMathRenderer constructs a renderer, probing terminal capability and the
// tectonic/poppler toolchain once. Detection is never fatal - all failures
// degrade to the Unicode fallback.
func newMathRenderer() *MathRenderer {
	mr := &MathRenderer{
		kittyOK:     detectKittyCapable(),
		toolchainOK: detectMathToolchain(),
		asciiMode:   os.Getenv("TERM") == "dumb" || os.Getenv("TERM") == "",
		pngCache:    make(map[string][]byte),
		imageIDs:    make(map[string]int),
	}
	return mr
}

// detectKittyCapable reports whether the current terminal understands the
// kitty graphics protocol. Uses the same env allowlist as restish, superfile
// and rasterm: KITTY_WINDOW_ID, TERM=xterm-kitty, or TERM_PROGRAM matching a
// known kitty-protocol terminal (WezTerm and ghostty implement it natively).
func detectKittyCapable() bool {
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		return true
	}
	if os.Getenv("TERM") == "xterm-kitty" {
		return true
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "WezTerm", "ghostty", "kitty":
		return true
	}
	return false
}

// detectMathToolchain reports whether both tectonic (LaTeX engine) and
// pdftoppm (poppler-utils PDF-to-PNG) are available on PATH.
func detectMathToolchain() bool {
	if _, err := exec.LookPath("tectonic"); err != nil {
		return false
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return false
	}
	return true
}

// canRenderImages reports whether the kitty PNG path is available.
func (mr *MathRenderer) canRenderImages() bool {
	return mr.kittyOK && mr.toolchainOK && !mr.asciiMode
}

// RenderMarkdown renders markdown content with math support to ANSI-styled,
// width-wrapped text.
//
// Display math blocks ($$...$$) are extracted before glamour: kitty-capable
// blocks are compiled to PNGs and replaced with U+10EEEE placeholder cells
// (transmitted via the queued APC sequences); everything else becomes a
// Unicode character grid inside a fenced code block that glamour styles.
// Inline math ($...$) is expanded by termtex inside the text segments.
// The whole document is glamour-rendered once so list/heading structure is
// preserved, then kitty placeholders are substituted back in.
//
// allowImages gates the kitty PNG path: it should be false while a message is
// still streaming (the equation is incomplete and recompiling per chunk is
// wasteful) and true once the message is complete.
func (mr *MathRenderer) RenderMarkdown(content string, width int, allowImages bool) string {
	if width <= 0 {
		width = 80
	}
	// Fast path: no display math. termtex.Expand handles inline $...$
	// (code-span aware) and glamour styles the rest.
	if !strings.Contains(content, "$$") {
		return renderMarkdown(termtex.Expand(content, mr.termtexStyle()), width)
	}

	type kittyReplacement struct {
		token string
		grid  string
	}
	var kittyRepls []kittyReplacement

	segments := splitMathSegments(content)
	var sb strings.Builder
	for i, seg := range segments {
		if i%2 == 1 {
			// Display math block.
			if allowImages && mr.canRenderImages() {
				if grid, ok := mr.renderKittyBlock(seg, width); ok {
					token := fmt.Sprintf("\x1fM%d\x1f", len(kittyRepls))
					sb.WriteString(token)
					kittyRepls = append(kittyRepls, kittyReplacement{token: token, grid: grid})
					continue
				}
			}
			// Unicode fallback: a fenced code block glamour renders as a
			// monospace grid (termtex.Expand skips fences, so inline math in
			// the text segments is unaffected).
			sb.WriteString(termtexBlock(seg, mr.termtexStyle()))
			continue
		}
		// Text segment: kept verbatim (inline $...$ expanded below).
		sb.WriteString(seg)
	}

	// Expand inline math, glamour-render the whole document once, then
	// substitute the transmitted kitty placeholders into the styled output.
	assembled := termtex.Expand(sb.String(), mr.termtexStyle())
	out := renderMarkdown(assembled, width)
	for _, r := range kittyRepls {
		out = strings.ReplaceAll(out, r.token, r.grid)
	}
	return strings.Trim(out, "\n")
}

// termtexStyle returns the termtex style for the current terminal.
func (mr *MathRenderer) termtexStyle() termtex.Style {
	return termtex.Style{ASCII: mr.asciiMode}
}

// termtexBlock renders a display-math LaTeX string to a Unicode character
// grid. On parse failure it falls back to showing the raw LaTeX in a code
// block rather than silently wrong output.
func termtexBlock(latex string, style termtex.Style) string {
	out, err := termtex.Render(latex, style)
	if err != nil || strings.TrimSpace(out) == "" {
		return "```\n" + latex + "\n```"
	}
	return "```\n" + out + "\n```"
}

// renderKittyBlock compiles a display-math block to a transparent PNG and
// returns kitty placeholder cells (or a raw transmitted image fallback). The
// APC sequences needed to transmit the image are queued into mr.pendingRaw.
// ok=false means the block could not be rendered as an image (compile error
// or missing toolchain) and the caller should use the Unicode fallback.
// width caps the placeholder width so images never overflow the chat pane.
func (mr *MathRenderer) renderKittyBlock(latex string, width int) (string, bool) {
	hash := mathHash(latex)

	mr.mu.Lock()
	if png, ok := mr.pngCache[hash]; ok {
		mr.mu.Unlock()
		// Already transmitted in a previous render pass; the terminal still
		// holds the image, so only re-emit the placeholder cells.
		return kittyPlaceholderGrid(mr.imageIDFor(hash), png, width), true
	}
	mr.mu.Unlock()

	png, err := compileEquationPNG(latex)
	if err != nil {
		// Compile failed - do not cache the failure (the equation might be
		// mid-stream); let the Unicode fallback handle it this pass.
		return "", false
	}

	mr.mu.Lock()
	if cached, ok := mr.pngCache[hash]; ok {
		mr.mu.Unlock()
		return kittyPlaceholderGrid(mr.imageIDFor(hash), cached, width), true
	}
	mr.nextImageID++
	id := mr.nextImageID
	mr.pngCache[hash] = png
	mr.pendingRaw = append(mr.pendingRaw, kittyTransmitSequence(id, png, width))
	mr.imageIDs[hash] = id
	mr.mu.Unlock()

	return kittyPlaceholderGrid(id, png, width), true
}

// mathHash returns a stable hex hash of a math source string.
func mathHash(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:8])
}

// compileEquationPNG compiles a LaTeX math expression to a transparent PNG.
//
// Pipeline: standalone .tex -> tectonic (PDF) -> pdftoppm (transparent PNG).
// The work runs in a temp dir that is always cleaned up. Errors are returned
// wrapped so the caller can log the underlying toolchain output.
func compileEquationPNG(latex string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "hakase-math-*")
	if err != nil {
		return nil, fmt.Errorf("math: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	texPath := filepath.Join(dir, "eqn.tex")
	tex := mathStandaloneDoc(latex)
	if err := os.WriteFile(texPath, []byte(tex), 0o600); err != nil {
		return nil, fmt.Errorf("math: write tex: %w", err)
	}

	// tectonic compiles the standalone document to PDF in --outdir.
	tec := exec.Command("tectonic", "-X", "compile", "--outdir", dir, texPath)
	if out, err := tec.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("math: tectonic failed: %v: %s", err, truncateBytes(out, 800))
	}

	pdfPath := filepath.Join(dir, "eqn.pdf")

	// pdftoppm -png -r 300 -transp eqn.pdf eqn -> eqn-1.png (transparent).
	ppm := exec.Command("pdftoppm", "-png", "-r", "300", "-transp", pdfPath, filepath.Join(dir, "eqn"))
	if out, err := ppm.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("math: pdftoppm failed: %v: %s", err, truncateBytes(out, 800))
	}

	pngPath := filepath.Join(dir, "eqn-1.png")
	png, err := os.ReadFile(pngPath)
	if err != nil {
		return nil, fmt.Errorf("math: read png: %w", err)
	}
	return png, nil
}

// mathStandaloneDoc wraps a math expression in a standalone LaTeX document
// with the math packages needed for common notation.
func mathStandaloneDoc(latex string) string {
	return `\documentclass[12pt]{standalone}
\usepackage{amsmath}
\usepackage{amssymb}
\begin{document}
$` + latex + `$
\end{document}
`
}

// kittyTransmitSequence builds the APC sequence that transmits a PNG to the
// terminal using virtual placement (U=1) with direct transmission and
// chunking. The image is registered under id and scaled to fit the terminal
// cell grid derived from the PNG pixel dimensions (capped to maxCols).
func kittyTransmitSequence(id int, png []byte, maxCols int) string {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(png))
	if err != nil {
		// Should not happen (pdftoppm output), but guard anyway.
		cfg.Width, cfg.Height = 100, 40
	}
	cols, rows := imageToCells(cfg.Width, cfg.Height, maxCols)

	var buf bytes.Buffer
	err = kitty.EncodeGraphics(&buf, mustDecodeImage(png), &kitty.Options{
		Action:           kitty.TransmitAndPut,
		Format:           kitty.PNG,
		Transmission:     kitty.Direct,
		ID:               id,
		Columns:          cols,
		Rows:             rows,
		VirtualPlacement: true,
		Quite:            2,
		Chunk:            true,
	})
	if err != nil {
		// Fall back to a bare transmit without placement metadata.
		return bareKittyTransmit(id, png)
	}
	return buf.String()
}

// bareKittyTransmit transmits a PNG without placement metadata - used only if
// the high-level encoder fails for an unexpected reason.
func bareKittyTransmit(id int, png []byte) string {
	b64 := base64.StdEncoding.EncodeToString(png)
	opts := fmt.Sprintf("a=t,f=100,i=%d,q=2", id)
	return "\x1b_G" + opts + ";" + b64 + "\x1b\\"
}

// mustDecodeImage decodes a PNG byte slice into an image.Image. The PNG was
// produced by pdftoppm, so a decode failure is a programming error; an empty
// 1x1 image is returned as a last resort.
func mustDecodeImage(png []byte) image.Image {
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		img = image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	return img
}

// imageToCells converts pixel dimensions to terminal cell dimensions using
// the terminal's cell size (default 8x16 px when unknown, e.g. in tests or
// non-kitty terminals where the pixel geometry is not reported). The result
// is capped to maxCols columns (0 = no cap) so images never overflow the
// chat pane; the row count scales proportionally to preserve aspect ratio.
func imageToCells(pxW, pxH int, maxCols int) (cols, rows int) {
	cw, ch := terminalCellPx()
	if cw <= 0 {
		cw = 8
	}
	if ch <= 0 {
		ch = 16
	}
	cols = (pxW + cw - 1) / cw
	rows = (pxH + ch - 1) / ch
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	if maxCols > 0 && cols > maxCols {
		// Scale down proportionally.
		rows = (rows*maxCols + cols - 1) / cols
		cols = maxCols
	}
	return cols, rows
}

// kittyPlaceholderGrid builds the U+10EEEE placeholder cell grid that the
// terminal replaces with the transmitted image. The grid occupies exactly
// rows lines of cols cells each, colored with the image ID so the terminal
// can map the placeholder to the right image. Newlines separate rows; the
// caller controls surrounding whitespace. maxCols caps the width (must match
// the cap used when the image was transmitted).
func kittyPlaceholderGrid(id int, png []byte, maxCols int) string {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(png))
	if err != nil {
		cfg.Width, cfg.Height = 100, 40
	}
	cols, rows := imageToCells(cfg.Width, cfg.Height, maxCols)

	var sb strings.Builder
	// Color encodes the image ID (24-bit truecolor).
	color := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", byte(id>>16), byte(id>>8), byte(id))
	for r := 0; r < rows; r++ {
		sb.WriteString(color)
		for c := 0; c < cols; c++ {
			sb.WriteRune(kitty.Placeholder)
			sb.WriteRune(kitty.Diacritic(r))
			sb.WriteRune(kitty.Diacritic(c))
		}
		sb.WriteString("\x1b[0m")
		if r < rows-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// imageIDFor returns the image ID assigned to a cached math hash. Called only
// for hashes already present in pngCache.
func (mr *MathRenderer) imageIDFor(hash string) int {
	return mr.imageIDs[hash]
}

// FlushRaw returns the queued kitty APC sequences accumulated since the last
// call and clears the queue. The caller (the Bubble Tea update loop) should
// emit each sequence via tea.Raw so images are transmitted to the terminal.
func (mr *MathRenderer) FlushRaw() []string {
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.pendingRaw) == 0 {
		return nil
	}
	out := mr.pendingRaw
	mr.pendingRaw = nil
	return out
}

// ClearAll returns the APC sequence that deletes all kitty images placed by
// this process - used on TUI teardown to avoid leaving stale images in the
// terminal when the app exits.
func (mr *MathRenderer) ClearAll() string {
	return "\x1b_Ga=d\x1b\\"
}

// truncateBytes truncates a byte slice for error logging.
func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// splitMathSegments splits markdown content into alternating text and
// display-math segments at $$...$$ boundaries. Odd indices (1, 3, ...) are
// math bodies (without the $$ delimiters, whitespace-trimmed); even indices
// are plain markdown text (may be empty at the boundaries - parity is always
// preserved so the caller can rely on odd = math). Code spans (backtick
// fences and inline code) are skipped so math delimiters inside code are left
// untouched. An unbalanced opening $$ is treated as literal text.
func splitMathSegments(content string) []string {
	var (
		segs   []string
		text   strings.Builder
		inCode bool
		fence  byte // backtick run character (0 = not in code)
		fenceN int  // fence run length for matching close
		i, n   = 0, len(content)
	)
	// flushText appends the accumulated text segment (even when empty) so the
	// odd-index-math parity is stable even at the start/end of the content.
	flushText := func() {
		segs = append(segs, text.String())
		text.Reset()
	}

	for i < n {
		c := content[i]

		if inCode {
			// Find the matching code close: same run of ` as the opener.
			if c == fence {
				run := 0
				for i+run < n && content[i+run] == fence {
					run++
				}
				text.WriteString(content[i : i+run])
				if run == fenceN {
					inCode = false
					fence = 0
				}
				i += run
				continue
			}
			text.WriteByte(c)
			i++
			continue
		}

		// Detect code fences (``` or ~~~) - a run of 3+ at line start
		// (ignoring leading whitespace) opens a block; shorter runs are
		// inline code that also must not be split.
		if c == '`' || c == '~' {
			run := 0
			for i+run < n && content[i+run] == c {
				run++
			}
			if run >= 3 && atLineStart(content, i) {
				inCode = true
				fence = c
				fenceN = run
			} else if run >= 1 {
				// Inline code span: find its matching close and copy verbatim.
				closer := findInlineCodeClose(content, i+run, c)
				if closer >= 0 {
					text.WriteString(content[i : closer+run])
					i = closer + run
					continue
				}
			}
			text.WriteString(content[i : i+run])
			i += run
			continue
		}

		// Display math opener: $$ not preceded by a backslash.
		if c == '$' && i+1 < n && content[i+1] == '$' && !isEscapedDollar(content, i) {
			// Find the closing $$.
			close := findMathClose(content, i+2)
			if close >= 0 {
				flushText()
				segs = append(segs, strings.TrimSpace(content[i+2:close]))
				i = close + 2
				continue
			}
			// Unbalanced - treat as literal text.
			text.WriteString(content[i : i+2])
			i += 2
			continue
		}

		text.WriteByte(c)
		i++
	}
	flushText()
	return segs
}

// atLineStart reports whether position i is at the start of a line (only
// whitespace precedes it on the current line).
func atLineStart(content string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch content[j] {
		case '\n':
			return true
		case ' ', '\t', '\r':
			continue
		default:
			return false
		}
	}
	return true
}

// isEscapedDollar reports whether the $$ at position i is preceded by an odd
// number of backslashes (i.e. it is escaped and should be literal).
func isEscapedDollar(content string, i int) bool {
	backslashes := 0
	for j := i - 1; j >= 0 && content[j] == '\\'; j-- {
		backslashes++
	}
	return backslashes%2 == 1
}

// findMathClose finds the position of the closing $$ starting the search at
// from. Returns -1 if none exists.
func findMathClose(content string, from int) int {
	for j := from; j+1 < len(content); j++ {
		if content[j] == '$' && content[j+1] == '$' && !isEscapedDollar(content, j) {
			return j
		}
	}
	return -1
}

// findInlineCodeClose finds the position of the closing run of backtick (or
// tilde) character c starting the search at from, matching the opening run
// length by scanning for a run of the same character. Returns -1 if none.
func findInlineCodeClose(content string, from int, c byte) int {
	for j := from; j < len(content); j++ {
		if content[j] == c {
			run := 0
			for j+run < len(content) && content[j+run] == c {
				run++
			}
			return j
		}
	}
	return -1
}

// terminalCellPx returns the terminal cell width and height in pixels,
// derived from TIOCGWINSZ when the terminal reports pixel geometry. Returns
// 0,0 when unknown (tests, non-kitty terminals) - callers use defaults.
func terminalCellPx() (w, h int) {
	ws, err := termiosWinsize()
	if err != nil || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0, 0
	}
	if ws.Col > 0 {
		w = int(ws.Xpixel) / int(ws.Col)
	}
	if ws.Row > 0 {
		h = int(ws.Ypixel) / int(ws.Row)
	}
	return w, h
}
// termiosWinsize returns the terminal window size including pixel geometry.
// Implemented per-platform (mathrender_winsize.go); returns an error when the
// terminal does not report pixel geometry.
func termiosWinsize() (*termWinsize, error) {
	return ioctlWinsize()
}
