package responses

import (
	"bytes"
	"image/png"
	"testing"

	"golang.org/x/image/font/basicfont"
)

func TestRendererCreatesValidPNG(t *testing.T) {
	renderer := Renderer{FontPath: testFontPath(t)}
	img := NewTextImage("schedule", "今天课表", "今天课表：\n西区 3A204\t09:50-11:25\t数据库系统")

	data, width, height, err := renderer.RenderPNG(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || width <= 0 || height <= 0 {
		t.Fatalf("len=%d width=%d height=%d", len(data), width, height)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != width || decoded.Bounds().Dy() != height {
		t.Fatalf("bounds = %v width=%d height=%d", decoded.Bounds(), width, height)
	}
}

func TestRendererDimensionsAreStable(t *testing.T) {
	renderer := Renderer{FontPath: testFontPath(t)}
	img := NewTextImage("todo", "待办", "待办：\n1. 截止 07-08 写报告\n2. 买咖啡")

	_, width1, height1, err := renderer.RenderPNG(img)
	if err != nil {
		t.Fatal(err)
	}
	_, width2, height2, err := renderer.RenderPNG(img)
	if err != nil {
		t.Fatal(err)
	}
	if width1 != width2 || height1 != height2 {
		t.Fatalf("first=%dx%d second=%dx%d", width1, height1, width2, height2)
	}
}

func TestDrawableTextSkipsUnsupportedGlyphs(t *testing.T) {
	got := drawableText(basicfont.Face7x13, "A\t✨B")
	if got != "A  B" {
		t.Fatalf("drawableText = %q", got)
	}
}

func testFontPath(t *testing.T) string {
	t.Helper()
	for _, path := range defaultFontPaths {
		if fileExists(path) {
			return path
		}
	}
	t.Skip("no CJK font found")
	return ""
}
