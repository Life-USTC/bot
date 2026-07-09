package responses

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestCardThemeUsesBusAccent(t *testing.T) {
	bus := cardTheme("bus")
	schedule := cardTheme("schedule")
	if bus.Label != "校车" {
		t.Fatalf("bus label = %q", bus.Label)
	}
	if bus.Accent == schedule.Accent {
		t.Fatalf("bus accent = %#v, want distinct schedule accent", bus.Accent)
	}
}

func TestBusRenderTablesParseSeparateStationTables(t *testing.T) {
	img := testBusImage()

	tables := busRenderTables(img)
	if len(tables) != 2 {
		t.Fatalf("len(tables) = %d, want 2", len(tables))
	}
	if got := tables[0].Header; !reflect.DeepEqual(got, []string{"东区", "西区", "先研院", "高新区"}) {
		t.Fatalf("first header = %#v", got)
	}
	if got := tables[1].Header; !reflect.DeepEqual(got, []string{"东区", "北区", "西区"}) {
		t.Fatalf("second header = %#v", got)
	}
	if tables[1].Rows[0].Highlight != true {
		t.Fatalf("second first row highlight = false")
	}
	if got := tables[1].Rows[0].Cells; !reflect.DeepEqual(got, []string{"15:30", "15:35", "15:40"}) {
		t.Fatalf("highlight row cells = %#v", got)
	}
}

func TestBusRenderLayoutUsesFixedColumnsAndCompactMetadata(t *testing.T) {
	layout := busRenderLayoutFor(testBusImage(), mustTime(t, "2026-07-09T15:28:00+08:00"))

	if layout.ColumnWidth != 112 {
		t.Fatalf("column width = %d, want 112", layout.ColumnWidth)
	}
	if got := layout.TableWidths; !reflect.DeepEqual(got, []int{448, 336}) {
		t.Fatalf("table widths = %#v", got)
	}
	if got := layout.TablePositions; !reflect.DeepEqual(got, []image.Point{{X: 52, Y: 80}, {X: 528, Y: 80}}) {
		t.Fatalf("table positions = %#v", got)
	}
	if got := layout.HeaderLines; !reflect.DeepEqual(got, []string{"Life @ USTC", "校车 · 东区 → 西区"}) {
		t.Fatalf("header lines = %#v", got)
	}
	if layout.NextWait != "12 分钟" {
		t.Fatalf("next wait = %q, want 12 分钟", layout.NextWait)
	}
	if got := layout.FooterLines; !reflect.DeepEqual(got, []string{"2026-07-09 15:28（工作日）", "2026 春季学期时刻表 / 蜗壳小道消息"}) {
		t.Fatalf("footer lines = %#v", got)
	}
	if layout.FooterY-layout.TableBottom < 16 {
		t.Fatalf("footer/table spacing = %d, want at least 16", layout.FooterY-layout.TableBottom)
	}
	if layout.Height != layout.FooterY+28 {
		t.Fatalf("height = %d, want content-driven footerY+28", layout.Height)
	}
	if layout.LogoOpacity > 0.20 {
		t.Fatalf("logo opacity = %.2f, want at most 0.20 for grid watermark", layout.LogoOpacity)
	}
	if layout.LogoSize < 200 || layout.LogoSize > 250 {
		t.Fatalf("logo size = %d, want around 210 for grid watermark", layout.LogoSize)
	}
	if !layout.UsesSerifFont {
		t.Fatalf("want serif font used for bus render")
	}
	if got := layout.TableHeaders[0]; len(got) != 4 || got[0].Text != "东区" || !got[0].Emphasize || got[1].Text != "西区" || !got[1].Emphasize {
		t.Fatalf("first table headers = %#v", got)
	}
	if got := layout.TableHeaders[1]; len(got) != 3 || got[0].Text != "东区" || !got[0].Emphasize || got[2].Text != "西区" || !got[2].Emphasize {
		t.Fatalf("second table headers = %#v", got)
	}
}

func TestBusRenderLayoutWrapsAllRouteTablesWithoutShrinkingColumns(t *testing.T) {
	layout := busRenderLayoutFor(testBusAllImage(), mustTime(t, "2026-07-09T15:28:00+08:00"))

	if layout.ColumnWidth != 112 {
		t.Fatalf("column width = %d, want 112", layout.ColumnWidth)
	}
	if got := layout.TableWidths; !reflect.DeepEqual(got, []int{448, 336, 336}) {
		t.Fatalf("table widths = %#v", got)
	}
	if got := layout.TablePositions; !reflect.DeepEqual(got, []image.Point{{X: 52, Y: 80}, {X: 528, Y: 80}, {X: 52, Y: 226}}) {
		t.Fatalf("table positions = %#v", got)
	}
	if layout.TableBottom != 344 {
		t.Fatalf("table bottom = %d, want 344", layout.TableBottom)
	}
}

func TestBusRenderTitleParse(t *testing.T) {
	if got := busRenderTitle(NewTextImage("bus", "校车 东区 → 西区", "x")); got != "东区 → 西区" {
		t.Fatalf("title = %q", got)
	}
	if got := busRenderTitle(NewTextImage("bus", "校车", "x")); got != "校车" {
		t.Fatalf("title = %q", got)
	}
}

func TestBusCellIsNumeric(t *testing.T) {
	cases := []struct {
		cell string
		want bool
	}{
		{"14:30", true},
		{"15:05", true},
		{"08:00", true},
		{"东区", false},
		{"西区", false},
		{"", false},
		{"✨", false},
		{"30 分钟", true},
	}
	for _, c := range cases {
		if got := busCellIsNumeric(c.cell); got != c.want {
			t.Fatalf("busCellIsNumeric(%q) = %v, want %v", c.cell, got, c.want)
		}
	}
}

func TestDrawBusTableKeepsGridLinesAboveRowBackgrounds(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 160, 120))
	line := color.RGBA{1, 2, 3, 255}
	drawBusTable(canvas,
		[]busStopHeader{{Text: "东区"}, {Text: "西区"}},
		busRenderTable{
			Header: []string{"东区", "西区"},
			Rows: []busRenderRow{
				{Cells: []string{"14:30", "14:40"}},
				{Cells: []string{"15:30", "15:40"}},
			},
		},
		10, 10, 100, 50, 20, 30, 1,
		basicfont.Face7x13, basicfont.Face7x13, basicfont.Face7x13, basicfont.Face7x13,
		color.RGBA{240, 240, 240, 255}, color.RGBA{255, 255, 255, 255}, color.RGBA{220, 250, 220, 255}, line,
		color.RGBA{0, 0, 0, 255}, color.RGBA{80, 80, 80, 255}, color.RGBA{150, 150, 150, 255}, color.RGBA{0, 120, 0, 255})

	checks := map[string]image.Point{
		"left border over striped row":      {10, 65},
		"right border over striped row":     {109, 65},
		"vertical divider over striped row": {60, 65},
		"bottom border":                     {40, 89},
	}
	for name, p := range checks {
		if got := canvas.RGBAAt(p.X, p.Y); got != line {
			t.Fatalf("%s pixel = %#v, want %#v", name, got, line)
		}
	}
}

func TestEmbeddedBusLogoLoadsForProductionImage(t *testing.T) {
	logo := loadEmbeddedBusLogo()
	if logo == nil {
		t.Fatal("embedded bus logo is nil")
	}
	if logo.Bounds().Dx() <= 0 || logo.Bounds().Dy() <= 0 {
		t.Fatalf("embedded bus logo bounds = %v", logo.Bounds())
	}
}

func testBusImage() *Image {
	return NewTextImage("bus", "校车 东区 → 西区", strings.Join([]string{
		"东区\t西区\t先研院\t高新区",
		"14:30\t14:40\t14:52\t15:05",
		"16:00\t16:10\t16:22\t16:35",
		"",
		"东区\t北区\t西区",
		"15:30\t15:35\t15:40\t✨",
		"15:50\t15:55\t16:00",
	}, "\n"))
}

func testBusAllImage() *Image {
	return NewTextImage("bus", "校车", strings.Join([]string{
		"东区\t西区\t先研院\t高新区",
		"14:30\t14:40\t14:52\t15:05",
		"16:00\t16:10\t16:22\t16:35",
		"",
		"东区\t北区\t西区",
		"15:30\t15:35\t15:40\t✨",
		"15:50\t15:55\t16:00",
		"",
		"高新区\t先研院\t西区",
		"15:40\t15:50\t16:10",
		"16:10\t16:20\t16:40",
	}, "\n"))
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
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
