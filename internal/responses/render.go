package responses

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

type Renderer struct {
	FontPath string
}

var defaultFontPaths = []string{
	"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
	"/usr/share/fonts/google-noto-sans-cjk-fonts/NotoSansCJK-Regular.ttc",
	"/usr/share/fonts/google-noto-sans-cjk-vf-fonts/NotoSansCJK-VF.ttc",
	"/usr/share/fonts/adobe-source-han-sans-cn-fonts/SourceHanSansCN-Regular.otf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
}

func (r Renderer) RenderPNG(img *Image) ([]byte, int, int, error) {
	if img == nil || strings.TrimSpace(img.AltText) == "" {
		return nil, 0, 0, errors.New("response image is empty")
	}
	face, err := r.fontFace(30)
	if err != nil {
		return nil, 0, 0, err
	}
	titleFace, err := r.fontFace(38)
	if err != nil {
		return nil, 0, 0, err
	}
	lines := wrappedLines(img.Lines, 36)
	width := 900
	height := 72 + 56 + len(lines)*42 + 44
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{248, 250, 252, 255}}, image.Point{}, draw.Src)
	card := image.Rect(28, 28, width-28, height-28)
	draw.Draw(canvas, card, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	drawText(canvas, titleFace, 56, 82, img.Title, color.RGBA{15, 23, 42, 255})
	y := 136
	for _, line := range lines {
		drawText(canvas, face, 56, y, line, color.RGBA{30, 41, 59, 255})
		y += 42
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), width, height, nil
}

func (r Renderer) fontFace(size float64) (font.Face, error) {
	path := strings.TrimSpace(r.FontPath)
	if path == "" {
		for _, candidate := range defaultFontPaths {
			if fileExists(candidate) {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return nil, errors.New("no font path configured")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	collection, err := opentype.ParseCollection(data)
	if err != nil {
		return nil, err
	}
	if collection.NumFonts() == 0 {
		return nil, errors.New("font collection is empty")
	}
	parsed, err := collection.Font(0)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

func DefaultFontPathsForTest() []string {
	return append([]string(nil), defaultFontPaths...)
}

func drawText(dst *image.RGBA, face font.Face, x, y int, text string, c color.Color) {
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(drawableText(face, text))
}

func drawableText(face font.Face, text string) string {
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\t':
			out.WriteString("  ")
		case '\n':
			out.WriteRune(r)
		case '\r':
			continue
		default:
			if r < ' ' {
				out.WriteRune(' ')
				continue
			}
			if _, ok := face.GlyphAdvance(r); !ok {
				continue
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}

func wrappedLines(lines []string, maxRunes int) []string {
	out := []string{}
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		runes := []rune(line)
		for len(runes) > maxRunes {
			out = append(out, string(runes[:maxRunes]))
			runes = runes[maxRunes:]
		}
		out = append(out, string(runes))
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
