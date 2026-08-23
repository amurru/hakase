package media

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// pilProvider is the always-available fallback image provider.
type pilProvider struct {
	store *Store
}

// NewPilProvider creates a pil provider.
func NewPilProvider(store *Store) Provider {
	return &pilProvider{store: store}
}

func (p *pilProvider) Name() string { return "pil" }

func (p *pilProvider) Capabilities() Capabilities {
	return Capabilities{Image: true}
}

func (p *pilProvider) GenerateImage(ctx context.Context, req ImageRequest) (*MediaResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required (1-4000 chars)")
	}
	w, h := req.ClampedSize()
	// Validate prompt length via Validate (already checks)
	if err := req.Validate(); err != nil {
		return nil, err
	}
	path, err := p.store.Allocate(".png")
	if err != nil {
		return nil, err
	}
	// Render image.
	img := renderPilImage(req.Prompt, req.Seed, w, h)
	// Encode to PNG.
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	if err := p.store.Write(path, bytes.NewReader(buf.Bytes()), 20<<20); err != nil {
		return nil, err
	}
	seedVal := req.Seed
	// Report the workspace-relative path so the web UI mediaLinks plugin
	// rewrites it to /api/files/inline (absolute paths leak the host FS into
	// the chat and 404 against the page origin).
	relPath := p.store.WorkspaceRelPath(path)
	markdown := fmt.Sprintf("![generated](%s)", relPath)
	return &MediaResult{
		Path:     relPath,
		Provider: "pil",
		Model:    "pil-v1",
		Seed:     seedVal,
		Width:    w,
		Height:   h,
		MimeType: "image/png",
		Markdown: markdown,
	}, nil
}

func (p *pilProvider) GenerateVideo(ctx context.Context, req VideoRequest) (*MediaResult, error) {
	return nil, fmt.Errorf("provider pil does not support video")
}

func (p *pilProvider) GenerateAudio(ctx context.Context, req AudioRequest) (*MediaResult, error) {
	return nil, fmt.Errorf("provider pil does not support audio")
}

// textLayout is an internal interface for text rendering (MG-004).
type textLayout interface {
	DrawString(img *image.RGBA, text string, x, y int, col color.Color) error
}

// simpleLayout uses embedded goregular font.
type simpleLayout struct {
	face font.Face
}

func newSimpleLayout(size float64) (*simpleLayout, error) {
	f, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, err
	}
	return &simpleLayout{face: face}, nil
}

func (s *simpleLayout) DrawString(img *image.RGBA, text string, x, y int, col color.Color) error {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: s.face,
		Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)},
	}
	d.DrawString(text)
	return nil
}

func renderPilImage(prompt string, seed *int64, w, h int) image.Image {
	// Background
	bg := color.RGBA{R: 0x1A, G: 0x1A, B: 0x2E, A: 0xFF}
	cardBg := color.RGBA{R: 0xF5, G: 0xF5, B: 0xF5, A: 0xFF}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)

	// Hash prompt+seed for deterministic shapes/colors.
	hsh := fnv.New64a()
	hsh.Write([]byte(prompt))
	if seed != nil {
		var b [8]byte
		b[0] = byte(*seed)
		b[1] = byte(*seed >> 8)
		b[2] = byte(*seed >> 16)
		b[3] = byte(*seed >> 24)
		b[4] = byte(*seed >> 32)
		b[5] = byte(*seed >> 40)
		b[6] = byte(*seed >> 48)
		b[7] = byte(*seed >> 56)
		hsh.Write(b[:])
	}
	hashVal := hsh.Sum64()

	// Draw rounded card (simple rect with margin).
	margin := w / 10
	cardRect := image.Rect(margin, margin, w-margin, h-margin)
	drawRect(img, cardRect, cardBg)

	// Decorative shapes derived from hash.
	for i := 0; i < 5; i++ {
		val := (hashVal >> (uint(i) * 11)) & 0x7FF
		x := margin + int(val)%(w-2*margin)
		y := margin + int((hashVal>>uint(i*7))&0x3FF)%(h-2*margin)
		r := 12 + int(hashVal>>uint(i*3))%40
		c := color.RGBA{
			R: uint8(80 + (hashVal>>uint(i*2))%120),
			G: uint8(80 + (hashVal>>uint(i*5))%120),
			B: uint8(120 + (hashVal>>uint(i*8))%120),
			A: 0x55,
		}
		drawCircle(img, x, y, r, c)
	}

	// Title from prompt (truncated to 80 runes so multi-byte characters are
	// never split mid-sequence).
	title := prompt
	if runes := []rune(title); len(runes) > 80 {
		title = string(runes[:80]) + "..."
	}
	// Remove newlines
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.ReplaceAll(title, "\r", " ")

	// Draw text centered near top of card.
	layout, _ := newSimpleLayout(float64(h) * 0.04)
	if layout != nil {
		// Estimate text width: ~0.6*fontSize per char approx
		fontSize := float64(h) * 0.04
		estWidth := int(float64(len([]rune(title))) * fontSize * 0.6)
		tx := (w - estWidth) / 2
		if tx < margin+10 {
			tx = margin + 10
		}
		ty := margin + int(fontSize) + 10
		_ = layout.DrawString(img, title, tx, ty, color.RGBA{0x1A, 0x1A, 0x2E, 0xFF})
		// Close face when done
		layout.face.Close()
	}

	// Small footer: pil-v1
	footerLayout, _ := newSimpleLayout(float64(h) * 0.025)
	if footerLayout != nil {
		footer := "pil-v1 • offline fallback"
		_ = footerLayout.DrawString(img, footer, margin+10, h-margin-10, color.RGBA{0x66, 0x66, 0x66, 0xFF})
		footerLayout.face.Close()
	}

	return img
}

// drawRect fills a rectangle with a solid color.
func drawRect(img *image.RGBA, r image.Rectangle, col color.RGBA) {
	draw.Draw(img, r, &image.Uniform{col}, image.Point{}, draw.Src)
}

func drawCircle(img *image.RGBA, cx, cy, radius int, col color.Color) {
	// Alpha blending via draw over
	for y := -radius; y <= radius; y++ {
		for x := -radius; x <= radius; x++ {
			if x*x+y*y <= radius*radius {
				px := cx + x
				py := cy + y
				if px >= 0 && px < img.Bounds().Dx() && py >= 0 && py < img.Bounds().Dy() {
					// Blend
					dst := img.RGBAAt(px, py)
					src := color.RGBAModel.Convert(col).(color.RGBA)
					// Simple alpha blending
					a := uint16(src.A)
					invA := 255 - a
					r := (uint16(src.R)*a + uint16(dst.R)*invA) / 255
					g := (uint16(src.G)*a + uint16(dst.G)*invA) / 255
					b := (uint16(src.B)*a + uint16(dst.B)*invA) / 255
					img.SetRGBA(px, py, color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 0xFF})
				}
			}
		}
	}
}
